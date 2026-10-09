package store

import (
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
)

func TestFindRunByIdempotency(t *testing.T) {
	root := t.TempDir()
	workflow := model.Workflow{Version: "1", Name: "demo", Nodes: []model.Node{{ID: "step", Type: "shell", Command: []string{"echo", "ok"}}}}
	created, err := CreateRun(root, workflow, map[string]string{"issue": "ABC-123"}, "request-1")
	if err != nil {
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
