package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestRunEvaluateAndCompare(t *testing.T) {
	workspace := t.TempDir()
	previous, restore := chdirForTest(t, workspace)
	defer restore()
	_ = previous
	root := workspace
	workflow := model.Workflow{Version: "3", Name: "demo", Entry: "agent", Nodes: []model.Node{{ID: "agent", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "process"}}}}}}
	makeRun := func(idempotency string) *model.Run {
		run, err := store.CreateRun(root, workflow, map[string]string{}, idempotency)
		if err != nil {
			t.Fatal(err)
		}
		run.State.Status = "succeeded"
		run.State.StartedAt = "2026-10-24T00:00:00Z"
		run.State.FinishedAt = "2026-10-24T00:00:01Z"
		run.State.DurationMs = 1000
		run.State.CreatedAt = "2026-10-24T00:00:0" + idempotency + "Z"
		if err := store.SaveState(run); err != nil {
			t.Fatal(err)
		}
		return run
	}
	baseline := makeRun("1")
	current := makeRun("2")

	if _, err := captureRunOutput(t, []string{"run", "evaluate", baseline.RunID, "--evaluation", `{"source":"human","measurements":{"quality":{"value":0.7,"direction":"higher"}}}`, "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := captureRunOutput(t, []string{"run", "evaluate", current.RunID, "--evaluation", `{"source":"human","measurements":{"quality":{"value":0.9,"direction":"higher"}}}`, "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	output, err := captureRunOutput(t, []string{"run", "compare", current.RunID, baseline.RunID, "--format", "json"})
	if err != nil {
		t.Fatal(err)
	}
	var comparison struct {
		Measurements map[string]struct {
			Improved *bool `json:"improved"`
		} `json:"measurements"`
	}
	if err := json.Unmarshal(output, &comparison); err != nil {
		t.Fatal(err)
	}
	if comparison.Measurements["quality"].Improved == nil || !*comparison.Measurements["quality"].Improved {
		t.Fatalf("quality comparison missing improvement: %s", output)
	}
	output, err = captureRunOutput(t, []string{"run", "compare", current.RunID, "--format", "json"})
	if err != nil {
		t.Fatal(err)
	}
	var automatic struct {
		Baseline struct {
			RunID string `json:"run_id"`
		} `json:"baseline"`
	}
	if err := json.Unmarshal(output, &automatic); err != nil {
		t.Fatal(err)
	}
	if automatic.Baseline.RunID != baseline.RunID {
		t.Fatalf("automatic baseline = %q, want %q: %s", automatic.Baseline.RunID, baseline.RunID, output)
	}
}

func chdirForTest(t *testing.T, directory string) (string, func()) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	return previous, func() { _ = os.Chdir(previous) }
}

func captureRunOutput(t *testing.T, args []string) ([]byte, error) {
	t.Helper()
	// Run writes synchronously. A file avoids blocking on a full pipe before
	// the test has begun reading, especially with Windows' smaller pipe buffer.
	output, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	previous := os.Stdout
	defer func() { os.Stdout = previous }()
	os.Stdout = output
	runErr := Run(args)
	os.Stdout = previous
	if _, err := output.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return bytes.TrimSpace(data), runErr
}
