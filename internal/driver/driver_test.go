package driver

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExpandCommandIncludesSessionAndWorkspace(t *testing.T) {
	got := ExpandCommand([]string{"agent", "{{ session_id }}", "{{ workspace }}", "{{ prompt }}", "{{result_file}}"}, "fix it", "/repo", "session-123", "/run/result.json", "run-1", "node-1")
	want := []string{"agent", "session-123", "/repo", "fix it", "/run/result.json"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestBuildClaudeCommand(t *testing.T) {
	got, err := BuildCommand("claude-code", Executor{Kind: "claude-code", Binary: "claude", ExtraArgs: []string{"--permission-mode", "acceptEdits"}}, Request{Prompt: "inspect", SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "--resume", "session-1", "--print", "--permission-mode", "acceptEdits", "inspect"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildCodexCommand(t *testing.T) {
	executor := Executor{Kind: "codex", Binary: "codex", Model: "gpt-5.6-sol", Sandbox: "workspace-write", SkipGitRepoCheck: true, Ephemeral: true}
	got, err := BuildCommand("codex", executor, Request{Prompt: "verify", SessionID: "session-2"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "exec", "resume", "-m", "gpt-5.6-sol", "--ephemeral", "session-2", "verify"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildCodexStartCommandIncludesSandbox(t *testing.T) {
	executor := Executor{Kind: "codex", Binary: "codex", Model: "gpt-5.6-sol", Sandbox: "workspace-write", SkipGitRepoCheck: true, Ephemeral: true}
	got, err := BuildCommand("codex", executor, Request{Prompt: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "exec", "-m", "gpt-5.6-sol", "-s", "workspace-write", "--skip-git-repo-check", "--ephemeral", "inspect"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuiltInExecutors(t *testing.T) {
	executors := BuiltInExecutors()
	if executors["claude-code"].Kind != "claude-code" || executors["codex"].Kind != "codex" {
		t.Fatalf("built-in executors = %#v", executors)
	}
}

func TestCheckExecutor(t *testing.T) {
	available := CheckExecutor("shell", Executor{Kind: "command", Command: []string{"sh"}})
	if !available.Available || available.Binary != "sh" {
		t.Fatalf("available executor = %#v", available)
	}
	missing := CheckExecutor("missing", Executor{Kind: "command", Command: []string{"agentflow-command-that-does-not-exist"}})
	if missing.Available || missing.Error == "" {
		t.Fatalf("missing executor = %#v", missing)
	}
}

func TestBuildCommandExecutorRequiresResumeTemplate(t *testing.T) {
	_, err := BuildCommand("custom", Executor{Kind: "command", Command: []string{"agent", "{{ prompt }}"}}, Request{Prompt: "continue", SessionID: "session-3"})
	if err == nil {
		t.Fatal("expected missing resume command error")
	}
}

func TestCustomExecutorEntryDoesNotInferBehaviorFromItsName(t *testing.T) {
	directory := t.TempDir()
	if err := os.MkdirAll(filepath.Join(directory, ".agentflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{"executors":{"codex":{"command":["echo","{{ input.label }}"],"workspace":"{{ input.directory }}","env":{"CHANNEL":"{{ input.channel }}"}}}}`
	if err := os.WriteFile(filepath.Join(directory, ".agentflow", "executors.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	executors, err := LoadExecutors(directory)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare("codex", executors["codex"], Invocation{Input: map[string]any{"label": "custom", "channel": "terminal", "directory": directory}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prepared.Command, []string{"echo", "custom"}) || prepared.Env["CHANNEL"] != "terminal" || prepared.Workspace != directory {
		t.Fatalf("incorrect entry binding: %+v", prepared)
	}
	if _, present := prepared.Env["AGENTFLOW_PROMPT"]; present {
		t.Fatal("generic program unexpectedly required agent prompt semantics")
	}
}
