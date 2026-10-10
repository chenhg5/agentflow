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

func TestSessionIsOnlyValidOnAgentNodes(t *testing.T) {
	workflow := model.Workflow{Version: "1", Name: "session", Nodes: []model.Node{{ID: "shell", Type: "shell", Session: "session-123", Command: []string{"true"}}}}
	errors := Validate(workflow)
	if len(errors) != 1 || errors[0] != "session is only supported on agent node shell" {
		t.Fatalf("unexpected validation errors: %v", errors)
	}
}
