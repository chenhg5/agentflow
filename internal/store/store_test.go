package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
)

func TestFindRunByIdempotency(t *testing.T) {
	root := t.TempDir()
	workflow := model.Workflow{Version: "3", Name: "demo", Entry: "step", Nodes: []model.Node{{ID: "step", Executor: "process", Config: map[string]any{"command": []string{"echo", "ok"}}}}, Edges: []model.Transition{{From: "step", To: "$end"}}}
	created, err := CreateRun(root, workflow, map[string]string{"issue": "ABC-123"}, "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "runs", "verification"), 0o755); err != nil {
		t.Fatal(err)
	}
	found, err := FindRunByIdempotency(root, "demo", "request-1")
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.RunID != created.RunID {
		t.Fatalf("found run = %#v, want %s", found, created.RunID)
	}
	missing, err := FindRunByIdempotency(root, "demo", "request-2")
	if err != nil {
		t.Fatal(err)
	}
	if missing != nil {
		t.Fatalf("unexpected run for unknown key: %s", missing.RunID)
	}
}

func TestRunEnumerationStillReportsIncompleteContainers(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "runs", "incomplete")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "workflow.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	ids, err := RunIDs(root)
	if err != nil || len(ids) != 1 || ids[0] != "incomplete" {
		t.Fatalf("incomplete run hidden: %v, %v", ids, err)
	}
	if _, err := FindRunByIdempotency(root, "demo", "request"); err == nil {
		t.Fatal("incomplete run must fail the idempotency check")
	}
}

func TestSaveAndLoadEvaluation(t *testing.T) {
	root := t.TempDir()
	workflow := model.Workflow{Version: "3", Name: "demo", Entry: "agent", Nodes: []model.Node{{ID: "agent", Executor: "process"}}}
	run, err := CreateRun(root, workflow, map[string]string{}, "")
	if err != nil {
		t.Fatal(err)
	}
	run.State.Evaluation = &model.Evaluation{Source: "human", EvaluatedAt: "2026-10-24T00:00:00Z", Measurements: map[string]model.Measurement{"quality": {Value: 0.9, Direction: "higher"}}}
	if err := SaveEvaluation(run); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(run); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRun(root, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State.Evaluation == nil || loaded.State.Evaluation.Measurements["quality"].Value != 0.9 {
		t.Fatalf("evaluation was not persisted: %#v", loaded.State.Evaluation)
	}
}
