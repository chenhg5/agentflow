package store

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
)

func EnsureDir(directory string) error { return os.MkdirAll(directory, 0o755) }

func CreateRun(root string, workflow model.Workflow, inputs map[string]string, idempotencyKey string) (*model.Run, error) {
	result, err := CreateOrGetRun(context.Background(), root, CreateRequest{Workflow: workflow, Inputs: inputs, IdempotencyKey: idempotencyKey})
	return result.Run, err
}

func FindRunByIdempotency(root, workflowName, idempotencyKey string) (*model.Run, error) {
	if idempotencyKey == "" {
		return nil, nil
	}
	ids, err := RunIDs(root)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		run, err := LoadRun(root, id)
		if err != nil {
			return nil, fmt.Errorf("cannot inspect run %s while checking idempotency: %w", id, err)
		}
		if run.State.WorkflowName == workflowName && run.State.IdempotencyKey == idempotencyKey {
			return run, nil
		}
	}
	return nil, nil
}

// RunIDs enumerates run containers, ignoring unrelated directories. Either
// engine metadata file identifies a container, so incomplete/corrupt runs still
// reach LoadRun and surface their errors instead of being silently hidden.
func RunIDs(root string) ([]string, error) {
	directory := filepath.Join(root, "runs")
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		for _, marker := range []string{"workflow.json", "state.json"} {
			if _, err := os.Stat(filepath.Join(directory, entry.Name(), marker)); !os.IsNotExist(err) {
				ids = append(ids, entry.Name())
				break
			}
		}
	}
	return ids, nil
}

func LoadRun(root, runID string) (*model.Run, error) {
	runDir := filepath.Join(root, "runs", runID)
	var workflow model.Workflow
	var state model.RunState
	if err := readJSON(filepath.Join(runDir, "workflow.json"), &workflow); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(runDir, "state.json"), &state); err != nil {
		return nil, err
	}
	// Evaluation is also kept as a sidecar so it can be inspected or copied
	// without rewriting the complete run state. Older runs may not have it.
	if err := readJSON(filepath.Join(runDir, "evaluation.json"), &state.Evaluation); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return &model.Run{RunID: runID, RunDir: runDir, Workflow: workflow, State: state}, nil
}

func SaveState(run *model.Run) error {
	run.State.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return writeJSON(filepath.Join(run.RunDir, "state.json"), run.State)
}

func SaveEvaluation(run *model.Run) error {
	if run.State.Evaluation == nil {
		err := os.Remove(filepath.Join(run.RunDir, "evaluation.json"))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return writeJSON(filepath.Join(run.RunDir, "evaluation.json"), run.State.Evaluation)
}

// eventMu keeps concurrent node workers from interleaving event lines.
var eventMu sync.Mutex

func AppendEvent(runDir string, event map[string]any) error {
	eventMu.Lock()
	defer eventMu.Unlock()
	event["at"] = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(runDir, "events.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = fmt.Fprintf(file, "%s\n", data)
	return err
}

func WriteArtifact(runDir, relativePath string, content []byte) error {
	target, err := ArtifactPath(runDir, relativePath)
	if err != nil {
		return err
	}
	if err := EnsureDir(filepath.Dir(target)); err != nil {
		return err
	}
	return os.WriteFile(target, content, 0o644)
}

func ArtifactPath(runDir, relativePath string) (string, error) {
	base, err := filepath.Abs(filepath.Join(runDir, "artifacts"))
	if err != nil {
		return "", err
	}
	target, err := filepath.Abs(filepath.Join(base, relativePath))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(base, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("artifact path escapes run directory: %s", relativePath)
	}
	return target, nil
}

func ReadEvents(runDir string) ([]byte, error) {
	return os.ReadFile(filepath.Join(runDir, "events.jsonl"))
}

func NewToken() string { return randomSuffix() + randomSuffix() }

func writeJSON(file string, value any) error {
	if err := EnsureDir(filepath.Dir(file)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary := file + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(temporary, file)
}

func readJSON(file string, value any) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func randomSuffix() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err == nil {
		return fmt.Sprintf("%x", buffer)
	}
	return fmt.Sprintf("%08x", time.Now().UnixNano()&0xffffffff)
}

// ReloadState replaces the in-memory run state with the persisted one.
func ReloadState(run *model.Run) error {
	var state model.RunState
	if err := readJSON(filepath.Join(run.RunDir, "state.json"), &state); err != nil {
		return err
	}
	run.State = state
	return nil
}
