package schema

import (
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
)

func TestValidateWorkflowAndCycle(t *testing.T) {
	valid := model.Workflow{Version: "2", Name: "test", Entry: "a", Nodes: []model.Node{
		{ID: "a", Executor: "process", Input: map[string]any{"command": []string{"echo", "a"}}},
		{ID: "b", Input: map[string]any{"text": "approve"}, Executor: "external"},
	}, Edges: []model.Transition{{From: "a", To: "b"}, {From: "b", To: "$end"}}}
	if errors := Validate(valid); len(errors) != 0 {
		t.Fatalf("valid workflow errors: %v", errors)
	}
	cycle := valid
	cycle.Edges[1] = model.Transition{From: "b", To: "a"}
	if errors := Validate(cycle); len(errors) == 0 {
		t.Fatal("expected cycle validation error")
	}
}

func TestExecutorReferenceIsRequired(t *testing.T) {
	workflow := model.Workflow{Version: "2", Name: "executor", Entry: "step", Nodes: []model.Node{{ID: "step"}}, Edges: []model.Transition{{From: "step", To: "$end"}}}
	problems := Validate(workflow)
	if len(problems) != 1 || problems[0] != "node step needs an executor" {
		t.Fatalf("unexpected errors: %v", problems)
	}
}

func TestValidateValueContract(t *testing.T) {
	definition := map[string]any{
		"type":     "object",
		"required": []any{"count"},
		"properties": map[string]any{
			"count": map[string]any{"type": "integer", "minimum": float64(1)},
		},
		"additionalProperties": false,
	}
	if errors := ValidateValue("input", map[string]any{"count": float64(2)}, definition); len(errors) != 0 {
		t.Fatalf("valid contract errors: %v", errors)
	}
	if errors := ValidateValue("input", map[string]any{"count": float64(0), "extra": true}, definition); len(errors) < 2 {
		t.Fatalf("expected minimum and additional property errors, got %v", errors)
	}
}

func TestValidateContractDefinition(t *testing.T) {
	definition := map[string]any{"type": "object", "properties": map[string]any{"count": map[string]any{"type": "made-up"}}}
	if errors := ValidateDefinition("outputSchema", definition); len(errors) != 1 {
		t.Fatalf("expected unsupported contract type error, got %v", errors)
	}
}

func TestFullJSONSchemaKeywords(t *testing.T) {
	definition := map[string]any{
		"type":     "object",
		"required": []any{"slug"},
		"properties": map[string]any{
			"slug": map[string]any{"type": "string", "pattern": "^[a-z]+$"},
		},
		"additionalProperties": false,
	}
	if errors := ValidateValue("output", map[string]any{"slug": "valid"}, definition); len(errors) != 0 {
		t.Fatalf("valid full schema errors: %v", errors)
	}
	if errors := ValidateValue("output", map[string]any{"slug": "Not-valid", "extra": true}, definition); len(errors) == 0 {
		t.Fatal("expected pattern and additional property errors")
	}
}

func TestInputReferenceNamespaces(t *testing.T) {
	workflow := model.Workflow{Version: "2", Name: "refs", Entry: "step", Inputs: map[string]model.InputDefinition{"sample": {}}, Nodes: []model.Node{{ID: "step", Executor: "process", Input: map[string]any{"sample": "{{ input.sample }}"}}}, Edges: []model.Transition{{From: "step", To: "$end"}}}
	if problems := Validate(workflow); len(problems) == 0 {
		t.Fatal("executor input namespace must not silently resolve to null inside a workflow")
	}
	workflow.Nodes[0].Input["sample"] = "{{ inputs.sample }}"
	if problems := Validate(workflow); len(problems) != 0 {
		t.Fatal(problems)
	}
	workflow.Nodes[0].Input["sample"] = "{{ nodes.missing.result.score }}"
	if problems := Validate(workflow); len(problems) == 0 {
		t.Fatal("unknown node reference must be rejected")
	}
	workflow.Nodes[0].Input["sample"] = "{{ nodes }}"
	if problems := Validate(workflow); len(problems) != 0 {
		t.Fatal(problems)
	}
}
