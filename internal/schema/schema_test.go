package schema

import (
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
)

func TestValidateWorkflowAndCycle(t *testing.T) {
	valid := model.Workflow{Version: "1", Name: "test", Nodes: []model.Node{
		{ID: "a", Type: "shell", Command: []string{"echo", "a"}},
		{ID: "b", Type: "human", Prompt: "approve", Needs: []string{"a"}},
	}}
	if errors := Validate(valid); len(errors) != 0 {
		t.Fatalf("valid workflow errors: %v", errors)
	}
	cycle := valid
	cycle.Nodes[0].Needs = []string{"b"}
	if errors := Validate(cycle); len(errors) == 0 {
		t.Fatal("expected cycle validation error")
	}
}
