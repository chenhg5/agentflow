package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/engine"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

// Whole references retain JSON types through RenderValue; interpolated text
// stays a string. Unknown values cannot change a literal container's type or
// hide an independently known first executable.
func TestValidationPreservesKnownCommandShapeAndExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"null-argument", "interpolated-command", "missing-executable", "interpolated-argument", "whole-input-argument", "whole-input-command", "typed-number-argument", "typed-string-argument", "typed-string-no-root-type", "typed-string-nullable-root", "node-status", "node-session", "node-status-child", "node-session-child", "node-snapshot", "nested-nullable", "nested-number-alternative", "nested-nonobject", "nested-string", "node-input-unreachable", "node-input-completed", "node-input-self", "node-result-completed", "node-result-self", "node-result-untaken", "node-result-forward"} {
		for _, nativeTarget := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-native-%v", name, nativeTarget), func(t *testing.T) {
				root := t.TempDir()
				var configured any
				var inputSchema map[string]any
				sourceNative := true
				bothPossible := false
				switch name {
				case "null-argument":
					configured = []any{"{{ input.cmd }}", nil}
				case "interpolated-command":
					configured = "prefix {{ input.cmd }}"
				case "missing-executable":
					configured = []any{filepath.Join(root, "missing-binary"), "{{ input.arg }}"}
				case "interpolated-argument":
					configured = []any{executable, "prefix {{ input.arg }}"}
					sourceNative = false
				case "whole-input-argument":
					configured = []any{executable, "{{ input }}"}
				case "whole-input-command":
					configured = "{{ input }}"
				case "typed-number-argument":
					configured = []any{executable, "{{ input.count }}"}
					inputSchema = map[string]any{"type": "object", "required": []any{"count"}, "properties": map[string]any{"count": map[string]any{"type": "number"}}}
				case "typed-string-argument", "typed-string-no-root-type", "typed-string-nullable-root":
					configured = []any{executable, "{{ input.text }}"}
					inputSchema = map[string]any{"type": "object", "required": []any{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}}}
					if name == "typed-string-no-root-type" {
						delete(inputSchema, "type")
					} else if name == "typed-string-nullable-root" {
						inputSchema["type"] = []any{"object", "null"}
					}
					sourceNative = false
				case "node-status":
					configured = []any{executable, "{{ nodes.producer.status }}"}
					sourceNative = false
				case "node-session":
					configured = []any{executable, "{{ nodes.producer.sessionId }}"}
					sourceNative = false
				case "node-snapshot":
					configured = []any{executable, "{{ nodes.producer }}"}
				case "node-status-child":
					configured = []any{executable, "{{ nodes.producer.status.missing }}"}
				case "node-session-child":
					configured = []any{executable, "{{ nodes.producer.sessionId.missing }}"}
				case "node-input-unreachable":
					configured = []any{executable, "{{ nodes.later.input.text }}"}
				case "node-input-completed":
					configured = []any{executable, "{{ nodes.producer.input.text }}"}
					sourceNative = false
				case "node-input-self":
					configured = []any{executable, "{{ nodes.source.input.text }}"}
					inputSchema = map[string]any{"type": "object", "required": []any{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}}}
					sourceNative = false
				case "node-result-completed":
					configured = []any{executable, "{{ nodes.producer.result.text }}"}
					sourceNative = false
				case "node-result-self":
					configured = []any{executable, "{{ nodes.source.result.text }}"}
				case "node-result-untaken", "node-result-forward":
					configured = []any{executable, "{{ nodes.later.result.text }}"}
				case "nested-nullable", "nested-number-alternative", "nested-nonobject", "nested-string":
					configured = []any{executable, "{{ input.parent.command }}"}
					var parentType any = "object"
					bothPossible = name == "nested-nullable" || name == "nested-number-alternative"
					if name == "nested-nullable" {
						parentType = []any{"object", "null"}
					} else if name == "nested-number-alternative" {
						parentType = []any{"object", "number"}
					} else if name == "nested-nonobject" {
						parentType = "number"
					}
					if name == "nested-string" {
						sourceNative = false
					}
					parent := map[string]any{"type": parentType, "required": []any{"command"}, "properties": map[string]any{"command": map[string]any{"type": "string"}}}
					inputSchema = map[string]any{"type": "object", "required": []any{"parent"}, "properties": map[string]any{"parent": parent}}
				}
				command := []string{executable}
				registry := map[string]any{"executors": map[string]any{
					"producer": map[string]any{"kind": "command", "command": command},
					"worker":   map[string]any{"kind": "command", "workspace": root, "resumeCommand": command},
					"native":   map[string]any{"kind": "claude-code", "command": command, "resumeCommand": command},
				}}
				target := "worker"
				if nativeTarget {
					target = "native"
				}
				flow := map[string]any{"version": "3", "name": "partial-command", "entry": "producer",
					"nodes": []any{
						map[string]any{"id": "producer", "execution": map[string]any{"candidates": []any{map[string]any{"executor": "producer"}}}},
						map[string]any{"id": "source", "execution": map[string]any{"candidates": []any{
							map[string]any{"executor": "worker", "config": map[string]any{"command": configured}}, map[string]any{"executor": "native"}},
							"fallback": map[string]any{"when": "failure.kind == 'configuration_failed'"}}},
						map[string]any{"id": "work", "execution": map[string]any{"candidates": []any{
							map[string]any{"executor": target, "session": map[string]any{"mode": "inherit", "from": "source"}}}}},
					}, "edges": []any{
						map[string]any{"from": "producer", "to": "source"}, map[string]any{"from": "source", "to": "work"}, map[string]any{"from": "work", "to": "$end"},
					}}
				if inputSchema != nil {
					flow["nodes"].([]any)[1].(map[string]any)["inputSchema"] = inputSchema
				}
				if name == "node-input-completed" || name == "node-input-unreachable" {
					knownInput := map[string]any{"type": "object", "required": []any{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}}}
					if name == "node-input-completed" {
						flow["nodes"].([]any)[0].(map[string]any)["inputSchema"] = knownInput
					} else {
						flow["nodes"] = append(flow["nodes"].([]any), map[string]any{"id": "later", "inputSchema": knownInput, "execution": map[string]any{"candidates": []any{map[string]any{"executor": "producer"}}}})
						flow["inputs"] = map[string]any{"route": map[string]any{"type": "string"}}
						flow["edges"] = []any{
							map[string]any{"from": "producer", "to": "later", "when": "inputs.route == 'later'"},
							map[string]any{"from": "producer", "to": "source"}, map[string]any{"from": "source", "to": "work"},
							map[string]any{"from": "work", "to": "$end"}, map[string]any{"from": "later", "to": "$end"},
						}
					}
				}
				if name == "node-result-completed" || name == "node-result-self" || name == "node-result-untaken" || name == "node-result-forward" {
					knownOutput := map[string]any{"required": []any{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}}}
					if name == "node-result-completed" {
						flow["nodes"].([]any)[0].(map[string]any)["outputSchema"] = knownOutput
					} else if name == "node-result-self" {
						flow["nodes"].([]any)[1].(map[string]any)["outputSchema"] = knownOutput
					} else {
						flow["nodes"] = append(flow["nodes"].([]any), map[string]any{"id": "later", "outputSchema": knownOutput, "execution": map[string]any{"candidates": []any{map[string]any{"executor": "producer"}}}})
						if name == "node-result-untaken" {
							flow["inputs"] = map[string]any{"route": map[string]any{"type": "string"}}
							flow["edges"] = []any{
								map[string]any{"from": "producer", "to": "later", "when": "inputs.route == 'later'"},
								map[string]any{"from": "producer", "to": "source"}, map[string]any{"from": "source", "to": "work"},
								map[string]any{"from": "work", "to": "$end"}, map[string]any{"from": "later", "to": "$end"},
							}
						} else {
							flow["edges"] = []any{map[string]any{"from": "producer", "to": "source"}, map[string]any{"from": "source", "to": "work"}, map[string]any{"from": "work", "to": "later"}, map[string]any{"from": "later", "to": "$end"}}
						}
					}
				}
				for file, value := range map[string]any{"executors.json": registry, "workflow.json": flow} {
					data, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, file), data, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				result := Validate(root, "")
				if name == "node-input-unreachable" || name == "node-result-untaken" {
					result = ValidateWithInputs(root, "", map[string]string{"route": "source"})
				}
				if want := bothPossible || nativeTarget == sourceNative; result.Valid != want {
					t.Fatalf("valid=%v want=%v: %+v", result.Valid, want, result.Issues)
				}
			})
		}
	}
}

func TestStaticTemplateTypesForNodeState(t *testing.T) {
	flow := model.Workflow{Entry: "producer", Nodes: []model.Node{{ID: "producer"}, {ID: "source"}}}
	node := model.Node{ID: "source"}
	for _, test := range []struct {
		value string
		want  staticJSONTypes
	}{
		{"{{ nodes.producer }}", staticOtherType},
		{"{{ nodes.producer.status }}", staticStringType},
		{"{{ nodes.producer.sessionId }}", staticStringType},
	} {
		t.Run(test.value, func(t *testing.T) {
			if got := staticTemplateTypes("", test.value, flow, node, nil, nil, nil, nil); got != test.want {
				t.Fatalf("staticTemplateTypes(%q) = %b, want %b", test.value, got, test.want)
			}
		})
	}
}

func TestStaticTemplateTypesForNodeInputActivationRoutes(t *testing.T) {
	inputSchema := map[string]any{
		"type":     "object",
		"required": []any{"provider"},
		"properties": map[string]any{
			"provider": map[string]any{"type": "string"},
		},
	}
	flow := model.Workflow{
		Entry: "producer",
		Inputs: map[string]model.InputDefinition{
			"route": {Required: true},
		},
		Nodes: []model.Node{{ID: "producer"}, {ID: "prepared", InputSchema: inputSchema}, {ID: "target"}},
		Edges: []model.Transition{
			{From: "producer", To: "prepared", When: "input.route == 'prepared'"},
			{From: "producer", To: "target"},
			{From: "prepared", To: "target"},
		},
	}
	node := workflowNode(flow, "target")
	preparedRoute, bypassRoute := "prepared", "producer"
	for _, test := range []struct {
		name     string
		incoming *string
		want     staticJSONTypes
	}{
		{name: "selected-source-route", incoming: &preparedRoute, want: staticStringType},
		{name: "selected-bypass-route", incoming: &bypassRoute, want: staticStringType | staticOtherType},
		{name: "unresolved-route", want: staticStringType | staticOtherType},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := staticTemplateTypes("", "{{ nodes.prepared.input.provider }}", flow, node, nil, test.incoming, nil, nil); got != test.want {
				t.Fatalf("staticTemplateTypes() = %b, want %b", got, test.want)
			}
		})
	}
}

func TestValidationMatchesRuntimeForNodeStateCommandReference(t *testing.T) {
	root := t.TempDir()
	executors := commandReferenceParityExecutors()
	flow := commandReferenceParityWorkflow(
		"node-state-reference",
		`printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`,
		"{{ nodes.producer.status }}",
		nil,
	)
	writeCommandReferenceParityWorkspace(t, root, flow, executors)

	if result := Validate(root, ""); result.Valid {
		t.Fatalf("validation allowed a fallback that the string-valued node status cannot select: %+v", result.Issues)
	}
	run, err := store.CreateRun(root, flow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	source, work := run.State.Nodes["source"], run.State.Nodes["work"]
	if source.SelectedExecutor != "command-source" {
		t.Fatalf("runtime selected source executor %q, want command-source: %+v", source.SelectedExecutor, source)
	}
	if run.State.Status != "failed" || work.Failure == nil || work.Failure.Kind != "session_incompatible" {
		t.Fatalf("runtime did not expose the incompatible command session: run=%s source=%+v work=%+v", run.State.Status, source, work)
	}
}

func TestValidationMatchesRuntimeForNonObjectSchemaAncestors(t *testing.T) {
	for _, test := range []struct {
		name           string
		ancestorTypes  []any
		producerResult string
	}{
		{name: "nullable", ancestorTypes: []any{"object", "null"}, producerResult: `{"parent":null}`},
		{name: "non-object", ancestorTypes: []any{"object", "string"}, producerResult: `{"parent":"not-an-object"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			executors := commandReferenceParityExecutors()
			inputSchema := map[string]any{
				"type":     "object",
				"required": []any{"parent"},
				"properties": map[string]any{
					"parent": map[string]any{
						"type":       test.ancestorTypes,
						"required":   []any{"command"},
						"properties": map[string]any{"command": map[string]any{"type": "string"}},
					},
				},
			}
			flow := commandReferenceParityWorkflow(
				"non-object-ancestor-"+test.name,
				fmt.Sprintf(`printf '%%s' '%s' > "$AGENTFLOW_RESULT_FILE"`, test.producerResult),
				"{{ input.parent.command }}",
				inputSchema,
			)
			writeCommandReferenceParityWorkspace(t, root, flow, executors)

			if result := Validate(root, ""); !result.Valid {
				t.Fatalf("validation pruned the runtime configuration fallback: %+v", result.Issues)
			}
			run, err := store.CreateRun(root, flow, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := engine.Execute(run, executors); err != nil {
				t.Fatal(err)
			}
			source, work := run.State.Nodes["source"], run.State.Nodes["work"]
			if run.State.Status != "succeeded" || source.SelectedExecutor != "native-source" || work.SelectedExecutor != "native-source" {
				t.Fatalf("runtime did not follow the compatible fallback session: run=%s source=%+v work=%+v", run.State.Status, source, work)
			}
			if len(source.AttemptSequence) != 2 || source.AttemptSequence[0].Failure == nil || source.AttemptSequence[0].Failure.Kind != "configuration_failed" {
				t.Fatalf("runtime source did not fail over on the missing nested value: %+v", source)
			}
		})
	}
}

func TestValidationMatchesRuntimeForStrictNonObjectSchemaAncestor(t *testing.T) {
	root := t.TempDir()
	executors := commandReferenceParityExecutors()
	inputSchema := map[string]any{
		"type":     "object",
		"required": []any{"parent"},
		"properties": map[string]any{
			"parent": map[string]any{
				"type":       "number",
				"required":   []any{"command"},
				"properties": map[string]any{"command": map[string]any{"type": "string"}},
			},
		},
	}
	flow := commandReferenceParityWorkflow(
		"strict-non-object-ancestor",
		`printf '{"parent":42}' > "$AGENTFLOW_RESULT_FILE"`,
		"{{ input.parent.command }}",
		inputSchema,
	)
	flow.Nodes[2].Execution.Candidates[0].Executor = "command-source"
	writeCommandReferenceParityWorkspace(t, root, flow, executors)

	if result := Validate(root, ""); result.Valid {
		t.Fatalf("validation allowed the impossible nested string route: %+v", result.Issues)
	}
	run, err := store.CreateRun(root, flow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	source, work := run.State.Nodes["source"], run.State.Nodes["work"]
	if source.SelectedExecutor != "native-source" || len(source.AttemptSequence) != 2 || source.AttemptSequence[0].Failure == nil || source.AttemptSequence[0].Failure.Kind != "configuration_failed" {
		t.Fatalf("runtime did not select the native fallback after the impossible nested lookup: %+v", source)
	}
	if run.State.Status != "failed" || work.Failure == nil || work.Failure.Kind != "session_incompatible" {
		t.Fatalf("runtime did not expose the incompatible fallback session: run=%s work=%+v", run.State.Status, work)
	}
}

func TestValidationMatchesRuntimeForUninitializedForwardNodeInput(t *testing.T) {
	root := t.TempDir()
	executors := commandReferenceParityExecutors()
	flow := commandReferenceParityWorkflow(
		"uninitialized-forward-node-input",
		`printf '{"ready":true}' > "$AGENTFLOW_RESULT_FILE"`,
		"{{ nodes.work.input.provider }}",
		nil,
	)
	flow.Nodes[2].InputSchema = map[string]any{
		"type":     "object",
		"required": []any{"provider"},
		"properties": map[string]any{
			"provider": map[string]any{"type": "string"},
		},
	}
	writeCommandReferenceParityWorkspace(t, root, flow, executors)

	if result := Validate(root, ""); !result.Valid {
		t.Fatalf("validation pruned the configuration fallback for an uninitialized future node input: %+v", result.Issues)
	}
	run, err := store.CreateRun(root, flow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	source, work := run.State.Nodes["source"], run.State.Nodes["work"]
	if run.State.Status != "succeeded" || source.SelectedExecutor != "native-source" || work.SelectedExecutor != "native-source" {
		t.Fatalf("runtime did not follow the compatible native fallback: run=%s source=%+v work=%+v", run.State.Status, source, work)
	}
	if len(source.AttemptSequence) != 2 || source.AttemptSequence[0].Failure == nil || source.AttemptSequence[0].Failure.Kind != "configuration_failed" {
		t.Fatalf("runtime source did not fail over on the uninitialized node input: %+v", source)
	}
}

func TestValidationMatchesRuntimeForSameWaveSiblingInput(t *testing.T) {
	root := t.TempDir()
	executors := commandReferenceParityExecutors()
	providerSchema := map[string]any{
		"type":     "object",
		"required": []any{"provider"},
		"properties": map[string]any{
			"provider": map[string]any{"type": "string"},
		},
	}
	flow := model.Workflow{
		Version: "3", Name: "same-wave-sibling-input", Entry: "router",
		Nodes: []model.Node{
			{ID: "router", Dispatch: "all", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "producer", Config: map[string]any{"command": []any{"sh", "-c", `printf '{"provider":"command"}' > "$AGENTFLOW_RESULT_FILE"`}},
			}}}},
			{ID: "sibling", InputSchema: providerSchema, Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "producer", Config: map[string]any{"command": []any{"sh", "-c", `printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}},
			}}}},
			{ID: "source", Execution: model.Execution{
				Candidates: []model.ExecutorCandidate{
					{Executor: "command-source", Config: map[string]any{"command": []any{
						"sh", "-c", `printf 'command-session' > "$AGENTFLOW_SESSION_FILE"; printf '{"provider":"command"}' > "$AGENTFLOW_RESULT_FILE"`, "source", "{{ nodes.sibling.input.provider }}",
					}}},
					{Executor: "native-source"},
				},
				Fallback: &model.FallbackSpec{When: "failure.kind == 'configuration_failed'"},
			}},
			{ID: "work", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "native-source", Session: model.SessionSpec{Mode: "inherit", From: "source"},
			}}}},
		},
		Edges: []model.Transition{
			{From: "router", To: "sibling"}, {From: "router", To: "source"},
			{From: "sibling", To: "$end"}, {From: "source", To: "work"}, {From: "work", To: "$end"},
		},
	}
	writeCommandReferenceParityWorkspace(t, root, flow, executors)

	if result := Validate(root, ""); result.Valid {
		t.Fatalf("validation invented a configuration fallback for an initialized same-wave sibling input: %+v", result.Issues)
	}
	run, err := store.CreateRun(root, flow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	source, work := run.State.Nodes["source"], run.State.Nodes["work"]
	if source.SelectedExecutor != "command-source" || len(source.AttemptSequence) != 1 {
		t.Fatalf("runtime did not use the string-valued sibling input on the first candidate: %+v", source)
	}
	if run.State.Status != "failed" || work.Failure == nil || work.Failure.Kind != "session_incompatible" {
		t.Fatalf("runtime did not expose the incompatible command session: run=%s source=%+v work=%+v", run.State.Status, source, work)
	}
}

func TestValidationMatchesRuntimeForTwoParentSameWaveInput(t *testing.T) {
	root := t.TempDir()
	executors := commandReferenceParityExecutors()
	providerSchema := map[string]any{
		"type":     "object",
		"required": []any{"provider"},
		"properties": map[string]any{
			"provider": map[string]any{"type": "string"},
		},
	}
	flow := model.Workflow{
		Version: "3", Name: "two-parent-same-wave-input", Entry: "router",
		Nodes: []model.Node{
			{ID: "router", Dispatch: "all", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "producer", Config: map[string]any{"command": []any{"sh", "-c", `printf '{}' > "$AGENTFLOW_RESULT_FILE"`}},
			}}}},
			{ID: "left", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "producer", Config: map[string]any{"command": []any{"sh", "-c", `printf '{"provider":"command"}' > "$AGENTFLOW_RESULT_FILE"`}},
			}}}},
			{ID: "right", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "producer", Config: map[string]any{"command": []any{"sh", "-c", `printf '{}' > "$AGENTFLOW_RESULT_FILE"`}},
			}}}},
			{ID: "sibling", InputSchema: providerSchema, Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "producer", Config: map[string]any{"command": []any{"sh", "-c", `printf '{}' > "$AGENTFLOW_RESULT_FILE"`}},
			}}}},
			{ID: "source", Execution: model.Execution{
				Candidates: []model.ExecutorCandidate{
					{Executor: "command-source", Config: map[string]any{"command": []any{
						"sh", "-c", `printf 'command-session' > "$AGENTFLOW_SESSION_FILE"; printf '{"provider":"command"}' > "$AGENTFLOW_RESULT_FILE"`, "source", "{{ nodes.sibling.input.provider }}",
					}}},
					{Executor: "native-source"},
				},
				Fallback: &model.FallbackSpec{When: "failure.kind == 'configuration_failed'"},
			}},
			{ID: "work", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "native-source", Session: model.SessionSpec{Mode: "inherit", From: "source"},
			}}}},
		},
		Edges: []model.Transition{
			{From: "router", To: "left"}, {From: "router", To: "right"},
			{From: "left", To: "sibling"}, {From: "right", To: "source"},
			{From: "sibling", To: "$end"}, {From: "source", To: "work"}, {From: "work", To: "$end"},
		},
	}
	writeCommandReferenceParityWorkspace(t, root, flow, executors)

	if result := Validate(root, ""); result.Valid {
		t.Fatalf("validation invented a fallback for an input initialized by another same-wave parent: %+v", result.Issues)
	}
	run, err := store.CreateRun(root, flow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	source, work := run.State.Nodes["source"], run.State.Nodes["work"]
	if source.SelectedExecutor != "command-source" || len(source.AttemptSequence) != 1 {
		t.Fatalf("runtime did not use the initialized input on the first candidate: %+v", source)
	}
	if run.State.Status != "failed" || work.Failure == nil || work.Failure.Kind != "session_incompatible" {
		t.Fatalf("runtime did not expose the incompatible command session: run=%s source=%+v work=%+v", run.State.Status, source, work)
	}
}

func TestValidationMatchesRuntimeForResultClearedByReactivationFanout(t *testing.T) {
	root := t.TempDir()
	executors := commandReferenceParityExecutors()
	providerSchema := map[string]any{
		"type":     "object",
		"required": []any{"provider"},
		"properties": map[string]any{
			"provider": map[string]any{"type": "string"},
		},
	}
	flow := model.Workflow{
		Version: "3", Name: "result-cleared-by-reactivation", Entry: "source",
		Inputs: map[string]model.InputDefinition{"phase": {Required: true}},
		Nodes: []model.Node{
			{ID: "source", OutputSchema: providerSchema, Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "command-source", Config: map[string]any{"command": []any{
					"sh", "-c", `printf 'command-session' > "$AGENTFLOW_SESSION_FILE"; printf '{"provider":"command"}' > "$AGENTFLOW_RESULT_FILE"`,
				}},
			}}}},
			{ID: "router", Dispatch: "all", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "producer", Config: map[string]any{"command": []any{"sh", "-c", `printf '{"phase":"repeat"}' > "$AGENTFLOW_RESULT_FILE"`}},
			}}}},
			{ID: "candidate", Execution: model.Execution{
				Candidates: []model.ExecutorCandidate{
					{Executor: "command-source", Config: map[string]any{"command": []any{
						"sh", "-c", `printf 'command-session' > "$AGENTFLOW_SESSION_FILE"; printf '{"provider":"command"}' > "$AGENTFLOW_RESULT_FILE"`, "candidate", "{{ nodes.source.result.provider }}",
					}}},
					{Executor: "native-source"},
				},
				Fallback: &model.FallbackSpec{When: "failure.kind == 'configuration_failed'"},
			}},
			{ID: "work", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "native-source", Session: model.SessionSpec{Mode: "inherit", From: "candidate"},
			}}}},
		},
		Edges: []model.Transition{
			{From: "source", To: "router", When: "input.phase == 'initial'", MaxIterations: 1},
			{From: "source", To: "$end"},
			{From: "router", To: "source", MaxIterations: 1}, {From: "router", To: "candidate"},
			{From: "candidate", To: "work"}, {From: "work", To: "$end"},
		},
	}
	writeCommandReferenceParityWorkspace(t, root, flow, executors)
	inputs := map[string]string{"phase": "initial"}

	if result := ValidateWithInputs(root, "", inputs); !result.Valid {
		t.Fatalf("validation pruned the configuration fallback after the source result was cleared: %+v", result.Issues)
	}
	run, err := store.CreateRun(root, flow, inputs, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	candidate, work := run.State.Nodes["candidate"], run.State.Nodes["work"]
	if run.State.Status != "succeeded" || candidate.SelectedExecutor != "native-source" || work.SelectedExecutor != "native-source" {
		t.Fatalf("runtime did not use the compatible fallback after reactivation cleared the result: run=%s candidate=%+v work=%+v", run.State.Status, candidate, work)
	}
	if len(candidate.AttemptSequence) != 2 || candidate.AttemptSequence[0].Failure == nil || candidate.AttemptSequence[0].Failure.Kind != "configuration_failed" {
		t.Fatalf("runtime candidate did not fail over on the cleared source result: %+v", candidate)
	}
}

func TestValidationMatchesRuntimeForTwoParentResultReactivation(t *testing.T) {
	root := t.TempDir()
	executors := commandReferenceParityExecutors()
	providerSchema := map[string]any{
		"type":     "object",
		"required": []any{"provider"},
		"properties": map[string]any{
			"provider": map[string]any{"type": "string"},
		},
	}
	flow := model.Workflow{
		Version: "3", Name: "two-parent-result-reactivation", Entry: "source",
		Inputs: map[string]model.InputDefinition{"phase": {Required: true}},
		Nodes: []model.Node{
			{ID: "source", OutputSchema: providerSchema, Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "command-source", Config: map[string]any{"command": []any{
					"sh", "-c", `printf 'command-session' > "$AGENTFLOW_SESSION_FILE"; printf '{"provider":"command"}' > "$AGENTFLOW_RESULT_FILE"`,
				}},
			}}}},
			{ID: "router", Dispatch: "all", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "producer", Config: map[string]any{"command": []any{"sh", "-c", `printf '{}' > "$AGENTFLOW_RESULT_FILE"`}},
			}}}},
			{ID: "left", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "producer", Config: map[string]any{"command": []any{"sh", "-c", `printf '{"phase":"repeat"}' > "$AGENTFLOW_RESULT_FILE"`}},
			}}}},
			{ID: "right", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "producer", Config: map[string]any{"command": []any{"sh", "-c", `printf '{}' > "$AGENTFLOW_RESULT_FILE"`}},
			}}}},
			{ID: "candidate", Execution: model.Execution{
				Candidates: []model.ExecutorCandidate{
					{Executor: "command-source", Config: map[string]any{"command": []any{
						"sh", "-c", `printf 'command-session' > "$AGENTFLOW_SESSION_FILE"; printf '{"provider":"command"}' > "$AGENTFLOW_RESULT_FILE"`, "candidate", "{{ nodes.source.result.provider }}",
					}}},
					{Executor: "native-source"},
				},
				Fallback: &model.FallbackSpec{When: "failure.kind == 'configuration_failed'"},
			}},
			{ID: "work", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "native-source", Session: model.SessionSpec{Mode: "inherit", From: "candidate"},
			}}}},
		},
		Edges: []model.Transition{
			{From: "source", To: "router", When: "input.phase == 'initial'", MaxIterations: 1},
			{From: "source", To: "$end"},
			{From: "router", To: "left"}, {From: "router", To: "right"},
			{From: "left", To: "source", MaxIterations: 1}, {From: "right", To: "candidate"},
			{From: "candidate", To: "work"}, {From: "work", To: "$end"},
		},
	}
	writeCommandReferenceParityWorkspace(t, root, flow, executors)
	inputs := map[string]string{"phase": "initial"}

	if result := ValidateWithInputs(root, "", inputs); !result.Valid {
		t.Fatalf("validation pruned the fallback after another same-wave parent cleared the result: %+v", result.Issues)
	}
	run, err := store.CreateRun(root, flow, inputs, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	candidate, work := run.State.Nodes["candidate"], run.State.Nodes["work"]
	if run.State.Status != "succeeded" || candidate.SelectedExecutor != "native-source" || work.SelectedExecutor != "native-source" {
		t.Fatalf("runtime did not use the compatible fallback after reactivation cleared the result: run=%s candidate=%+v work=%+v", run.State.Status, candidate, work)
	}
	if len(candidate.AttemptSequence) != 2 || candidate.AttemptSequence[0].Failure == nil || candidate.AttemptSequence[0].Failure.Kind != "configuration_failed" {
		t.Fatalf("runtime candidate did not fail over on the cleared source result: %+v", candidate)
	}
}

func TestValidationMatchesRuntimeForExternalFallbackWaitingResult(t *testing.T) {
	root := t.TempDir()
	executors := commandReferenceParityExecutors()
	executors["external"] = driver.Executor{Kind: "external"}
	providerSchema := map[string]any{
		"type":     "object",
		"required": []any{"provider"},
		"properties": map[string]any{
			"provider": map[string]any{"type": "string"},
		},
	}
	flow := model.Workflow{
		Version: "3", Name: "external-fallback-waiting-result", Entry: "seed",
		Nodes: []model.Node{
			{ID: "seed", Dispatch: "all", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "producer", Config: map[string]any{"command": []any{"sh", "-c", `printf '{}' > "$AGENTFLOW_RESULT_FILE"`}},
			}}}},
			{ID: "waiting", OutputSchema: providerSchema, Execution: model.Execution{Candidates: []model.ExecutorCandidate{
				{Executor: "command-source", Config: map[string]any{"command": []any{filepath.Join(root, "missing-executable")}}},
				{Executor: "external"},
			}}},
			{ID: "branch", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "producer", Config: map[string]any{"command": []any{"sh", "-c", `printf '{}' > "$AGENTFLOW_RESULT_FILE"`}},
			}}}},
			{ID: "source", Execution: model.Execution{
				Candidates: []model.ExecutorCandidate{
					{Executor: "command-source", Config: map[string]any{"command": []any{
						"sh", "-c", `printf 'command-session' > "$AGENTFLOW_SESSION_FILE"; printf '{"provider":"command"}' > "$AGENTFLOW_RESULT_FILE"`, "source", "{{ nodes.waiting.result.provider }}",
					}}},
					{Executor: "native-source"},
				},
				Fallback: &model.FallbackSpec{When: "failure.kind == 'configuration_failed'"},
			}},
			{ID: "work", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "native-source", Session: model.SessionSpec{Mode: "inherit", From: "source"},
			}}}},
		},
		Edges: []model.Transition{
			{From: "seed", To: "waiting"}, {From: "seed", To: "branch"},
			{From: "waiting", To: "$end"}, {From: "branch", To: "source"},
			{From: "source", To: "work"}, {From: "work", To: "$end"},
		},
	}
	writeCommandReferenceParityWorkspace(t, root, flow, executors)

	if result := Validate(root, ""); !result.Valid {
		t.Fatalf("validation pruned the consumer fallback while an external fallback can leave the result absent: %+v", result.Issues)
	}
	run, err := store.CreateRun(root, flow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	waiting, source, work := run.State.Nodes["waiting"], run.State.Nodes["source"], run.State.Nodes["work"]
	if run.State.Status != "waiting_input" || waiting.SelectedExecutor != "external" || waiting.Status != "waiting_input" {
		t.Fatalf("runtime did not park on the reachable external fallback: run=%s waiting=%+v", run.State.Status, waiting)
	}
	if len(waiting.AttemptSequence) != 2 || waiting.AttemptSequence[0].Failure == nil || waiting.AttemptSequence[0].Failure.Kind != "start_failed" || waiting.AttemptSequence[1].Status != "waiting_input" {
		t.Fatalf("runtime did not preserve the primary startup failure: %+v", waiting)
	}
	if source.SelectedExecutor != "native-source" || work.SelectedExecutor != "native-source" || work.Status != "succeeded" {
		t.Fatalf("runtime consumer did not use the compatible fallback while the result was absent: source=%+v work=%+v", source, work)
	}
}

func commandReferenceParityExecutors() map[string]driver.Executor {
	return map[string]driver.Executor{
		"producer":       {Kind: "command"},
		"command-source": {Kind: "command"},
		"native-source": {
			Kind:          "claude-code",
			Command:       []string{"sh", "-c", `printf 'native-session' > "$AGENTFLOW_SESSION_FILE"; printf '{"provider":"native"}' > "$AGENTFLOW_RESULT_FILE"`},
			ResumeCommand: []string{"sh", "-c", `test "$AGENTFLOW_SESSION_ID" = native-session; printf '{"resumed":true}' > "$AGENTFLOW_RESULT_FILE"`},
		},
	}
}

func commandReferenceParityWorkflow(name, producerCommand, reference string, inputSchema map[string]any) model.Workflow {
	return model.Workflow{
		Version: "3", Name: name, Entry: "producer",
		Nodes: []model.Node{
			{ID: "producer", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "producer", Config: map[string]any{"command": []any{"sh", "-c", producerCommand}}}}}},
			{ID: "source", InputSchema: inputSchema, Execution: model.Execution{
				Candidates: []model.ExecutorCandidate{
					{Executor: "command-source", Config: map[string]any{"command": []any{
						"sh", "-c", `printf 'command-session' > "$AGENTFLOW_SESSION_FILE"; printf '{"provider":"command"}' > "$AGENTFLOW_RESULT_FILE"`, "source", reference,
					}}},
					{Executor: "native-source"},
				},
				Fallback: &model.FallbackSpec{When: "failure.kind == 'configuration_failed'"},
			}},
			{ID: "work", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{
				Executor: "native-source", Session: model.SessionSpec{Mode: "inherit", From: "source"},
			}}}},
		},
		Edges: []model.Transition{{From: "producer", To: "source"}, {From: "source", To: "work"}, {From: "work", To: "$end"}},
	}
}

func writeCommandReferenceParityWorkspace(t *testing.T, root string, flow model.Workflow, executors map[string]driver.Executor) {
	t.Helper()
	for name, value := range map[string]any{
		"workflow.json":  flow,
		"executors.json": map[string]any{"executors": executors},
	} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
