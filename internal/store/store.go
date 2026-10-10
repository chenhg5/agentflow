package store

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
)

func EnsureDir(directory string) error { return os.MkdirAll(directory, 0o755) }

func CreateRun(root string, workflow model.Workflow, inputs map[string]string, idempotencyKey string) (*model.Run, error) {
	if err := EnsureDir(filepath.Join(root, "runs")); err != nil {
		return nil, err
	}
	timestamp := time.Now().UTC().Format("20060102150405")
	runID := fmt.Sprintf("run_%s_%s", timestamp, randomSuffix())
	runDir := filepath.Join(root, "runs", runID)
	if err := EnsureDir(filepath.Join(runDir, "artifacts")); err != nil {
		return nil, err
	}
	nodes := map[string]*model.NodeState{}
	for _, node := range workflow.Nodes {
		nodes[node.ID] = &model.NodeState{Status: "pending", Artifacts: map[string]string{}}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	run := &model.Run{RunID: runID, RunDir: runDir, Workflow: workflow, State: model.RunState{
		RunID: runID, WorkflowName: workflow.Name, Status: "pending", Inputs: inputs, IdempotencyKey: idempotencyKey, Nodes: nodes, CreatedAt: now, UpdatedAt: now,
	}}
	if err := writeJSON(filepath.Join(runDir, "workflow.json"), workflow); err != nil {
		return nil, err
	}
	if err := SaveState(run); err != nil {
		return nil, err
	}
	if err := AppendEvent(runDir, map[string]any{"type": "run.created", "runId": runID, "inputs": inputs}); err != nil {
		return nil, err
	}
	return run, nil
}

func FindRunByIdempotency(root, workflowName, idempotencyKey string) (*model.Run, error) {
	if idempotencyKey == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(filepath.Join(root, "runs"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		run, err := LoadRun(root, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("cannot inspect run %s while checking idempotency: %w", entry.Name(), err)
		}
		if run.State.WorkflowName == workflowName && run.State.IdempotencyKey == idempotencyKey {
			return run, nil
		}
	}
	return nil, nil
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
	return &model.Run{RunID: runID, RunDir: runDir, Workflow: workflow, State: state}, nil
}

func SaveState(run *model.Run) error {
	run.State.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return writeJSON(filepath.Join(run.RunDir, "state.json"), run.State)
}

func AppendEvent(runDir string, event map[string]any) error {
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
