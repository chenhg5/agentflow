package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chenhg5/agentflow/internal/store"
)

func TestCreationCLIProcess(t *testing.T) {
	if os.Getenv("AF_CREATION_CLI_CHILD") != "1" {
		return
	}
	var args []string
	if json.Unmarshal([]byte(os.Getenv("AF_CREATION_CLI_ARGS")), &args) != nil {
		os.Exit(90)
	}
	err := Run(args)
	if err == nil {
		os.Exit(0)
	}
	fmt.Fprintln(os.Stderr, err)
	var command *CommandError
	if errors.As(err, &command) {
		os.Exit(command.Code)
	}
	os.Exit(91)
}

func TestCreationExecutorProcess(t *testing.T) {
	if os.Getenv("AF_CREATION_EXECUTOR") != "1" {
		return
	}
	file, err := os.OpenFile(filepath.Join(os.Getenv("AGENTFLOW_WORKSPACE"), "starts"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(92)
	}
	_, err = file.WriteString("x")
	_ = file.Close()
	if err != nil {
		os.Exit(93)
	}
	if os.WriteFile(os.Getenv("AGENTFLOW_RESULT_FILE"), []byte(`{"ok":true}`), 0o600) != nil {
		os.Exit(94)
	}
	os.Exit(0)
}

func TestRunCreationAtomicAcrossCLIProcesses(t *testing.T) {
	root := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writeFixture := func(name string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFixture("workflow.json", map[string]any{"version": "3", "name": "concurrent-creation", "entry": "work", "nodes": []any{map[string]any{"id": "work", "execution": map[string]any{"candidates": []any{map[string]any{"executor": "fixture"}}}}}, "edges": []any{map[string]any{"from": "work", "to": "$end"}}})
	writeFixture("executors.json", map[string]any{"executors": map[string]any{"fixture": map[string]any{"kind": "command", "command": []string{binary, "-test.run=^TestCreationExecutorProcess$"}, "workspace": root, "env": map[string]string{"AF_CREATION_EXECUTOR": "1"}}}})
	writeFixture("triggers.json", map[string]any{"triggers": []any{map[string]any{"id": "fixture", "enabled": true, "source": map[string]any{"kind": "cron", "expression": "0 0 * * *"}, "target": map[string]any{"workflow": "workflow.json"}, "policy": map[string]any{"concurrency": "allow"}}}})
	invoke := func(args []string) ([]byte, int) {
		encoded, _ := json.Marshal(args)
		cmd := exec.Command(binary, "-test.run=^TestCreationCLIProcess$")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "AF_CREATION_CLI_CHILD=1", "AF_CREATION_CLI_ARGS="+string(encoded))
		data, err := cmd.CombinedOutput()
		if err != nil && cmd.ProcessState == nil {
			return []byte(err.Error()), -1
		}
		return data, cmd.ProcessState.ExitCode()
	}
	for trial := 0; trial < 12; trial++ {
		mixed := trial >= 10
		key := fmt.Sprintf("request-%d", trial)
		delivery := fmt.Sprintf("delivery-%d", trial)
		if mixed {
			data, _ := json.Marshal([]string{"fixture", delivery})
			hash := sha256.Sum256(data)
			key = hex.EncodeToString(hash[:])
		}
		type result struct {
			data []byte
			code int
		}
		rows := make([]result, 32)
		var group sync.WaitGroup
		for i := range rows {
			group.Add(1)
			go func(i int) {
				defer group.Done()
				args := []string{"run", "start", "workflow.json", "--idempotency-key", key, "--format", "json"}
				if mixed && i%2 == 1 {
					args = []string{"trigger", "fire", "fixture", "--delivery-id", delivery, "--format", "json"}
				}
				rows[i].data, rows[i].code = invoke(args)
			}(i)
		}
		group.Wait()
		ids := map[string]bool{}
		for _, row := range rows {
			if row.code != 0 {
				t.Fatalf("trial %d exit %d: %s", trial, row.code, row.data)
			}
			var payload map[string]any
			if err := json.Unmarshal(row.data, &payload); err != nil {
				t.Fatalf("invalid process output: %s", row.data)
			}
			id, _ := payload["run_id"].(string)
			if id == "" {
				trig, _ := payload["trigger"].(map[string]any)
				run, _ := trig["run"].(map[string]any)
				id, _ = run["run_id"].(string)
			}
			if id == "" {
				t.Fatalf("response had no run: %s", row.data)
			}
			ids[id] = true
		}
		if len(ids) != 1 {
			t.Fatalf("trial %d produced %d runs", trial, len(ids))
		}
		stored, err := store.RunIDs(root)
		if err != nil || len(stored) != trial+1 {
			t.Fatalf("trial %d stored %d runs: %v", trial, len(stored), err)
		}
		starts, err := os.ReadFile(filepath.Join(root, "starts"))
		if err != nil || len(starts) != trial+1 {
			t.Fatalf("trial %d executor starts=%d error=%v", trial, len(starts), err)
		}
		if !mixed {
			data, code := invoke([]string{"run", "start", "workflow.json", "--idempotency-key", key, "--input", "changed=yes", "--format", "json"})
			if code != ExitConflict {
				t.Fatalf("changed input exit %d: %s", code, data)
			}
		}
	}
	// Every contender submits a distinct request under the same key. Exactly
	// one may create a run; the other processes must report stable conflicts.
	codes := make([]int, 32)
	var contenders sync.WaitGroup
	for i := range codes {
		contenders.Add(1)
		go func(i int) {
			defer contenders.Done()
			_, codes[i] = invoke([]string{"run", "start", "workflow.json", "--idempotency-key", "contended", "--input", fmt.Sprintf("contender=%d", i), "--format", "json"})
		}(i)
	}
	contenders.Wait()
	winners := 0
	for _, code := range codes {
		if code == 0 {
			winners++
		} else if code != ExitConflict {
			t.Fatalf("changed concurrent request exit %d", code)
		}
	}
	if winners != 1 {
		t.Fatalf("changed concurrent requests had %d winners", winners)
	}
	starts, err := os.ReadFile(filepath.Join(root, "starts"))
	if err != nil || len(starts) != 13 {
		t.Fatalf("conflicting requests launched additional executors: %d, %v", len(starts), err)
	}
}
