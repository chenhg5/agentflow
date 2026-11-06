package schema

import (
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
)

func TestValidateWorkflowAndCycle(t *testing.T) {
	valid := model.Workflow{Version: "3", Name: "test", Entry: "a", Nodes: []model.Node{
		{ID: "a", Execution: candidateExecution("process", map[string]any{"command": []string{"echo", "a"}})},
		{ID: "b", Execution: candidateExecution("external", map[string]any{"text": "approve"})},
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

func TestExecutorCandidatesAreRequired(t *testing.T) {
	workflow := model.Workflow{Version: "3", Name: "executor", Entry: "step", Nodes: []model.Node{{ID: "step"}}, Edges: []model.Transition{{From: "step", To: "$end"}}}
	problems := Validate(workflow)
	if len(problems) != 1 || problems[0] != "node step execution.candidates must be non-empty" {
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
	workflow := model.Workflow{Version: "3", Name: "refs", Entry: "step", Inputs: map[string]model.InputDefinition{"sample": {}}, Nodes: []model.Node{{ID: "step", Execution: candidateExecution("process", map[string]any{"sample": "{{ input.sample }}"})}}, Edges: []model.Transition{{From: "step", To: "$end"}}}
	if problems := Validate(workflow); len(problems) != 0 {
		t.Fatal(problems)
	}
	workflow.Nodes[0].Execution.Candidates[0].Config["sample"] = "{{ inputs.sample }}"
	if problems := Validate(workflow); len(problems) != 0 {
		t.Fatal(problems)
	}
	workflow.Nodes[0].Execution.Candidates[0].Config["sample"] = "{{ nodes.missing.result.score }}"
	if problems := Validate(workflow); len(problems) == 0 {
		t.Fatal("unknown node reference must be rejected")
	}
	workflow.Nodes[0].Execution.Candidates[0].Config["sample"] = "{{ nodes }}"
	if problems := Validate(workflow); len(problems) != 0 {
		t.Fatal(problems)
	}
}

func TestEdgeMapReferences(t *testing.T) {
	workflow := model.Workflow{Version: "3", Name: "edge-map", Entry: "first", Inputs: map[string]model.InputDefinition{"seed": {}}, Nodes: []model.Node{
		{ID: "first", Execution: candidateExecution("process", nil)}, {ID: "second", Execution: candidateExecution("process", nil)},
	}, Edges: []model.Transition{
		{From: "first", To: "second", Map: testEdgeMap(map[string]any{"a": "{{ input.seed }}", "b": "{{ output.result }}", "c": "{{ nodes.first.result }}"})},
		{From: "second", To: "$end"},
	}}
	if problems := Validate(workflow); len(problems) != 0 {
		t.Fatal(problems)
	}
	(*workflow.Edges[0].Map)["bad"] = "{{ other.result }}"
	if problems := Validate(workflow); len(problems) == 0 {
		t.Fatal("unknown edge map reference must be rejected")
	}
	delete(*workflow.Edges[0].Map, "bad")
	workflow.Edges[0].Map = testEdgeMap(map[string]any{})
	if problems := Validate(workflow); len(problems) != 0 {
		t.Fatalf("empty edge map should produce an empty input: %v", problems)
	}
}

func TestCandidateValidationAndFallbackExpression(t *testing.T) {
	workflow := model.Workflow{Version: "3", Name: "candidates", Entry: "step", Nodes: []model.Node{{
		ID: "step", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "primary"}, {Executor: "primary"}}},
	}}, Edges: []model.Transition{{From: "step", To: "$end"}}}
	if problems := Validate(workflow); len(problems) == 0 {
		t.Fatal("duplicate candidate executors must be rejected")
	}
	workflow.Nodes[0].Execution.Candidates[1].Executor = "fallback"
	workflow.Nodes[0].Execution.Fallback = &model.FallbackSpec{When: "failure.kind =="}
	if problems := Validate(workflow); len(problems) == 0 {
		t.Fatal("invalid fallback expression must be rejected")
	}
	workflow.Nodes[0].Execution.Fallback.When = "failure.kind == 'exit_code' && failure.code == 75"
	if problems := Validate(workflow); len(problems) != 0 {
		t.Fatalf("valid fallback expression rejected: %v", problems)
	}
	workflow.Nodes[0].Execution.Fallback.When = " failure.code == 75 "
	if problems := Validate(workflow); len(problems) != 0 {
		t.Fatalf("fallback expression whitespace rejected: %v", problems)
	}
}

func testEdgeMap(value map[string]any) *map[string]any { return &value }

func candidateExecution(executor string, config map[string]any) model.Execution {
	return model.Execution{Candidates: []model.ExecutorCandidate{{Executor: executor, Config: config}}}
}

func TestValidateWorkflowJSONRejectsExplicitNullsButKeepsBusinessNulls(t *testing.T) {
	base := `{"version":"3","name":"definition","entry":"work","nodes":[{"id":"work","execution":{"candidates":[{"executor":"external"}]}}],"edges":[{"from":"work","to":"$end"}]}`
	if problems := ValidateWorkflowJSON([]byte(base)); len(problems) != 0 {
		t.Fatalf("baseline workflow rejected: %v", problems)
	}
	for _, document := range []string{
		`{"version":"3","name":"n","entry":"work","nodes":[{"id":"work","execution":{"candidates":[{"executor":"external","config":null}]}}],"edges":[{"from":"work","to":"$end"}]}`,
		`{"version":"3","name":"n","entry":"work","nodes":[{"id":"work","execution":{"candidates":[{"executor":"external","session":null}]}}],"edges":[{"from":"work","to":"$end"}]}`,
		`{"version":"3","name":"n","entry":"work","nodes":[{"id":"work","execution":{"candidates":[{"executor":"external"}],"fallback":null}}],"edges":[{"from":"work","to":"$end"}]}`,
		`{"version":"3","name":"n","entry":"work","nodes":[{"id":"work","retry":null,"execution":{"candidates":[{"executor":"external"}]}}],"edges":[{"from":"work","to":"$end"}]}`,
		`{"version":"3","name":"n","entry":"work","nodes":[{"id":"work","join":null,"execution":{"candidates":[{"executor":"external"}]}}],"edges":[{"from":"work","to":"$end"}]}`,
		`{"version":"3","name":"n","entry":"work","nodes":[{"id":"work","dispatch":null,"execution":{"candidates":[{"executor":"external"}]}}],"edges":[{"from":"work","to":"$end"}]}`,
		`{"version":"3","name":"n","entry":"work","inputs":null,"nodes":[{"id":"work","execution":{"candidates":[{"executor":"external"}]}}],"edges":[{"from":"work","to":"$end"}]}`,
	} {
		if problems := ValidateWorkflowJSON([]byte(document)); len(problems) == 0 {
			t.Fatalf("explicit null agreed with the published schema: %s", document)
		}
	}
	businessNull := `{"version":"3","name":"n","entry":"work","nodes":[{"id":"work","execution":{"candidates":[{"executor":"external","config":{"user_data":null}}]}}],"edges":[{"from":"work","to":"$end"}]}`
	if problems := ValidateWorkflowJSON([]byte(businessNull)); len(problems) != 0 {
		t.Fatalf("opaque candidate config lost its business null: %v", problems)
	}
}

func TestValidateWorkflowJSONRequiresOneDocument(t *testing.T) {
	base := `{"version":"3","name":"definition","entry":"work","nodes":[{"id":"work","execution":{"candidates":[{"executor":"external"}]}}],"edges":[{"from":"work","to":"$end"}]}`
	for _, document := range []string{
		base + `{"second":"document"}`,
		base + ` true`,
		base + ` {}`,
		base + ` not-json`,
	} {
		if problems := ValidateWorkflowJSON([]byte(document)); len(problems) == 0 {
			t.Fatalf("trailing document accepted: %s", document)
		}
	}
	if problems := ValidateWorkflowJSON([]byte(base + "\n  \t\n")); len(problems) != 0 {
		t.Fatalf("trailing whitespace rejected: %v", problems)
	}
}
