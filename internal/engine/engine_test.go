package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestAgentRequiresStructuredResult(t *testing.T) {
	workflow := model.Workflow{Version: "1", Name: "result", Nodes: []model.Node{{ID: "agent", Type: "agent", Driver: "fake", Prompt: "work"}}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(run, map[string]driver.Provider{"fake": {Command: []string{"sh", "-c", "exit 0"}}})
	if err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "failed" || !strings.Contains(run.State.Nodes["agent"].Error, "structured result is missing") {
		t.Fatalf("expected structured result failure, got status=%s error=%q", run.State.Status, run.State.Nodes["agent"].Error)
	}
}

func TestAgentStoresStructuredResultSeparatelyFromLogs(t *testing.T) {
	workflow := model.Workflow{Version: "1", Name: "result", Nodes: []model.Node{{ID: "agent", Type: "agent", Driver: "fake", Prompt: "work"}}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(run, map[string]driver.Provider{"fake": {Command: []string{"sh", "-c", "printf '{\"ok\":true}' > \"$AGENTFLOW_RESULT_FILE\"; printf 'log\\n'"}}})
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
	workflow := model.Workflow{Version: "1", Name: "contract", Nodes: []model.Node{{ID: "agent", Type: "agent", Driver: "fake", Workspace: workspace, Prompt: "work"}}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	provider := driver.Provider{Command: []string{"sh", "-c", `printf '%s' "$AGENTFLOW_PROMPT" > prompt.txt; printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}}
	if err := Execute(run, map[string]driver.Provider{"fake": provider}); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "succeeded" {
		t.Fatalf("status = %s, error = %q", run.State.Status, run.State.Nodes["agent"].Error)
	}
	prompt, err := os.ReadFile(filepath.Join(workspace, "prompt.txt"))
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(run.RunDir, "artifacts", "agent.result.json")
	if !strings.Contains(string(prompt), expected) {
		t.Fatalf("prompt did not include exact result path %q: %s", expected, prompt)
	}
}

func TestAgentResumesSessionInWorkspace(t *testing.T) {
	workspace := t.TempDir()
	workflow := model.Workflow{Version: "1", Name: "session", Nodes: []model.Node{{ID: "agent", Type: "agent", Driver: "fake", Workspace: workspace, Session: "session-123", Prompt: "continue"}}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	provider := driver.Provider{Command: []string{"sh", "-c", "exit 9"}, ResumeCommand: []string{"sh", "-c", `printf '{"session_id":"%s","workspace":"%s"}' "$AGENTFLOW_SESSION_ID" "$AGENTFLOW_WORKSPACE" > "$AGENTFLOW_RESULT_FILE"`}}
	if err := Execute(run, map[string]driver.Provider{"fake": provider}); err != nil {
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
	workflow := model.Workflow{Version: "1", Name: "session", Nodes: []model.Node{{ID: "agent", Type: "agent", Driver: "fake", Session: "session-123", Prompt: "continue"}}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, map[string]driver.Provider{"fake": {Command: []string{"true"}}}); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["agent"]
	if run.State.Status != "failed" || !strings.Contains(state.Error, "resumeCommand") {
		t.Fatalf("expected resume command failure, got status=%s error=%q", run.State.Status, state.Error)
	}
}
