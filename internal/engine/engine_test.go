package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestAgentRequiresStructuredResult(t *testing.T) {
	workflow := model.Workflow{Version: "2", Name: "result", Nodes: []model.Node{{ID: "agent", Input: map[string]any{"prompt": "work"}, Executor: "fake"}}}
	run, err := store.CreateRun(t.TempDir(), linearWorkflow(workflow), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(run, map[string]driver.Executor{"fake": {Kind: "codex", Command: []string{"sh", "-c", "exit 0"}}})
	if err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "failed" || !strings.Contains(run.State.Nodes["agent"].Error, "structured result is missing") {
		t.Fatalf("expected structured result failure, got status=%s error=%q", run.State.Status, run.State.Nodes["agent"].Error)
	}
}

func TestAgentStoresStructuredResultSeparatelyFromLogs(t *testing.T) {
	workflow := model.Workflow{Version: "2", Name: "result", Nodes: []model.Node{{ID: "agent", Input: map[string]any{"prompt": "work"}, Executor: "fake"}}}
	run, err := store.CreateRun(t.TempDir(), linearWorkflow(workflow), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(run, map[string]driver.Executor{"fake": {Kind: "codex", Command: []string{"sh", "-c", "printf '{\"ok\":true}' > \"$AGENTFLOW_RESULT_FILE\"; printf 'log\\n'"}}})
	if err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "succeeded" {
		t.Fatalf("status = %s", run.State.Status)
	}
	result, ok := run.State.Nodes["agent"].Result.(map[string]any)
	if !ok || result["ok"] != true {
		t.Fatalf("unexpected result: %#v", run.State.Nodes["agent"].Result)
	}
	if _, ok := run.State.Nodes["agent"].Artifacts["stdoutLog"]; !ok {
		t.Fatal("stdout should be recorded as a log artifact")
	}
	state := run.State.Nodes["agent"]
	if state.Attempts != 1 || state.OutputBytes <= 0 || state.StdoutBytes <= 0 {
		t.Fatalf("expected execution metrics, got attempts=%d output_bytes=%d stdout_bytes=%d", state.Attempts, state.OutputBytes, state.StdoutBytes)
	}
}

func TestAgentPromptIncludesExactResultPath(t *testing.T) {
	workspace := t.TempDir()
	workflow := model.Workflow{Version: "2", Name: "contract", Nodes: []model.Node{{ID: "agent", Input: map[string]any{"prompt": "work", "workspace": workspace}, Executor: "fake"}}}
	run, err := store.CreateRun(t.TempDir(), linearWorkflow(workflow), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	executor := driver.Executor{Kind: "codex", Command: []string{"sh", "-c", `printf '%s' "$AGENTFLOW_PROMPT" > prompt.txt; printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}}
	if err := Execute(run, map[string]driver.Executor{"fake": executor}); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "succeeded" {
		t.Fatalf("status = %s, error = %q", run.State.Status, run.State.Nodes["agent"].Error)
	}
	prompt, err := os.ReadFile(filepath.Join(workspace, "prompt.txt"))
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(run.RunDir, "artifacts", "agent.1.result.json")
	if !strings.Contains(string(prompt), expected) {
		t.Fatalf("prompt did not include exact result path %q: %s", expected, prompt)
	}
}

func TestAgentResumesSessionInWorkspace(t *testing.T) {
	workspace := t.TempDir()
	workflow := model.Workflow{Version: "2", Name: "session", Nodes: []model.Node{{ID: "agent", Input: map[string]any{"prompt": "continue", "workspace": workspace, "session": "session-123"}, Executor: "fake"}}}
	run, err := store.CreateRun(t.TempDir(), linearWorkflow(workflow), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	executor := driver.Executor{Kind: "codex", Command: []string{"sh", "-c", "exit 9"}, ResumeCommand: []string{"sh", "-c", `printf '{"session_id":"%s","workspace":"%s"}' "$AGENTFLOW_SESSION_ID" "$AGENTFLOW_WORKSPACE" > "$AGENTFLOW_RESULT_FILE"`}}
	if err := Execute(run, map[string]driver.Executor{"fake": executor}); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "succeeded" {
		t.Fatalf("status = %s, error = %q", run.State.Status, run.State.Nodes["agent"].Error)
	}
	state := run.State.Nodes["agent"]
	if state.SessionID != "session-123" || state.Workspace != workspace {
		t.Fatalf("binding = session %q workspace %q", state.SessionID, state.Workspace)
	}
	result, ok := state.Result.(map[string]any)
	if !ok || result["session_id"] != "session-123" {
		t.Fatalf("unexpected result: %#v", state.Result)
	}
}

func TestAgentSessionRequiresResumeCommand(t *testing.T) {
	workflow := model.Workflow{Version: "2", Name: "session", Nodes: []model.Node{{ID: "agent", Input: map[string]any{"prompt": "continue", "session": "session-123"}, Executor: "fake"}}}
	run, err := store.CreateRun(t.TempDir(), linearWorkflow(workflow), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, map[string]driver.Executor{"fake": {Kind: "codex", Command: []string{"true"}}}); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["agent"]
	if run.State.Status != "failed" || !strings.Contains(state.Error, "resumeCommand") {
		t.Fatalf("expected resume command failure, got status=%s error=%q", run.State.Status, state.Error)
	}
}

func TestNodeContractsPersistInputAndValidateOutput(t *testing.T) {
	workflow := model.Workflow{Version: "2", Name: "contracts", Inputs: map[string]model.InputDefinition{"name": {Required: true}}, Nodes: []model.Node{{
		ID:           "step",
		Input:        map[string]any{"name": "{{ inputs.name }}", "command": []string{"sh", "-c", `printf '{"greeting":"hello"}' > "$AGENTFLOW_RESULT_FILE"`}},
		InputSchema:  map[string]any{"type": "object", "required": []any{"name"}, "properties": map[string]any{"name": map[string]any{"type": "string"}}},
		OutputSchema: map[string]any{"type": "object", "required": []any{"greeting"}, "properties": map[string]any{"greeting": map[string]any{"type": "string"}}}, Executor: "process",
	}},
	}
	run, err := store.CreateRun(t.TempDir(), linearWorkflow(workflow), map[string]string{"name": "Ada"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, nil); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "succeeded" || run.State.Nodes["step"].Input.(map[string]any)["name"] != "Ada" {
		t.Fatalf("unexpected contract run: status=%s input=%#v error=%q", run.State.Status, run.State.Nodes["step"].Input, run.State.Nodes["step"].Error)
	}
}

func TestExplicitInputPersistsArtifact(t *testing.T) {
	workflow := model.Workflow{Version: "2", Name: "handoff"}
	workflow.Nodes = []model.Node{
		{ID: "produce", Executor: "process", Input: map[string]any{"command": []string{"sh", "-c", `printf '{"summary":"ok","count":2,"ignored":true}' > "$AGENTFLOW_RESULT_FILE"`}}},
		{ID: "consume", Input: map[string]any{"summary": "{{ nodes.produce.result.summary }}", "count": "{{ nodes.produce.result.count }}", "command": []string{"sh", "-c", `printf '{"done":true}' > "$AGENTFLOW_RESULT_FILE"`}}, Executor: "process"},
	}
	run, err := store.CreateRun(t.TempDir(), linearWorkflow(workflow), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, nil); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "succeeded" {
		t.Fatalf("status=%s error=%q", run.State.Status, run.State.Nodes["consume"].Error)
	}
	input, ok := run.State.Nodes["consume"].Input.(map[string]any)
	if !ok || input["summary"] != "ok" || input["count"] != float64(2) {
		t.Fatalf("unexpected propagated input: %#v", run.State.Nodes["consume"].Input)
	}
	data, err := os.ReadFile(filepath.Join(run.RunDir, "artifacts", "consume.1.input.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(data, &persisted); err != nil || persisted["summary"] != "ok" {
		t.Fatalf("unexpected input artifact: %s", data)
	}
}

func TestExplicitInputPreservesTypes(t *testing.T) {
	workflow := model.Workflow{Version: "2", Name: "mapping"}
	workflow.Nodes = []model.Node{
		{ID: "produce", Executor: "process", Input: map[string]any{"command": []string{"sh", "-c", `printf '{"count":3}' > "$AGENTFLOW_RESULT_FILE"`}}},
		{ID: "consume", Input: map[string]any{"count": "{{ nodes.produce.result.count }}", "command": []string{"sh", "-c", `printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}}, InputSchema: map[string]any{"type": "object", "required": []any{"count"}, "properties": map[string]any{"count": map[string]any{"type": "integer"}}}, Executor: "process"},
	}
	run, err := store.CreateRun(t.TempDir(), linearWorkflow(workflow), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, nil); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "succeeded" {
		t.Fatalf("status=%s error=%q", run.State.Status, run.State.Nodes["consume"].Error)
	}
	if got := run.State.Nodes["consume"].Input.(map[string]any)["count"]; got != float64(3) {
		t.Fatalf("mapping changed type: %#v", got)
	}
}

func linearWorkflow(workflow model.Workflow) model.Workflow {
	workflow.Entry = workflow.Nodes[0].ID
	workflow.Edges = nil
	for i, node := range workflow.Nodes {
		target := "$end"
		if i+1 < len(workflow.Nodes) {
			target = workflow.Nodes[i+1].ID
		}
		workflow.Edges = append(workflow.Edges, model.Transition{From: node.ID, To: target})
	}
	return workflow
}
