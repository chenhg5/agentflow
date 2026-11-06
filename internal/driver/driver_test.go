package driver

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
	want := []string{"claude", "--resume", "session-1", "--print", "--output-format", "json", "--permission-mode", "acceptEdits", "--", "inspect"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildCodexCommand(t *testing.T) {
	executor := Executor{Kind: "codex", Binary: "codex", Model: "gpt-5.6-sol", Sandbox: "workspace-write", SkipGitRepoCheck: true}
	got, err := BuildCommand("codex", executor, Request{Prompt: "verify", SessionID: "session-2"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "exec", "resume", "-m", "gpt-5.6-sol", "--json", "session-2", "verify"}
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
	want := []string{"codex", "exec", "-m", "gpt-5.6-sol", "-s", "workspace-write", "--skip-git-repo-check", "--ephemeral", "--json", "inspect"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSessionModesSelectNativeAndCustomForkCommands(t *testing.T) {
	if _, err := BuildCommand("codex", Executor{Kind: "codex", Ephemeral: true}, Request{SessionMode: "fork", SessionID: "parent"}); err == nil {
		t.Fatal("ephemeral session must not be forked")
	}
	codex, err := BuildCommand("codex", Executor{Kind: "codex"}, Request{SessionMode: "fork", SessionID: "parent", Prompt: "continue"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(codex, []string{"codex", "exec", "fork", "--json", "parent", "continue"}) {
		t.Fatalf("codex fork command = %v", codex)
	}
	claude, err := BuildCommand("claude-code", Executor{Kind: "claude-code"}, Request{SessionMode: "fork", SessionID: "parent", Prompt: "continue"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(claude, []string{"claude", "--resume", "parent", "--fork-session", "--print", "--output-format", "json", "--", "continue"}) {
		t.Fatalf("claude fork command = %v", claude)
	}
	custom := Executor{Kind: "command", Command: []string{"tool", "new"}, ResumeCommand: []string{"tool", "resume"}, ForkCommand: []string{"tool", "fork", "{{session_id}}"}}
	command, err := BuildCommand("custom", custom, Request{SessionMode: "fork", SessionID: "parent"})
	if err != nil || !reflect.DeepEqual(command, []string{"tool", "fork", "parent"}) {
		t.Fatalf("custom fork command = %v, %v", command, err)
	}
	custom.ForkCommand = nil
	if _, err := BuildCommand("custom", custom, Request{SessionMode: "fork", SessionID: "parent"}); err == nil {
		t.Fatal("fork must not silently resume the parent session")
	}
}

func TestSessionMetadataFromNativeLogs(t *testing.T) {
	if got := SessionFromLogs(Executor{Kind: "codex"}, "{\"type\":\"thread.started\",\"thread_id\":\"codex-123\"}\n"); got != "codex-123" {
		t.Fatalf("codex session = %q", got)
	}
	if got := SessionFromLogs(Executor{Kind: "claude-code"}, "{\"session_id\":\"claude-123\"}"); got != "claude-123" {
		t.Fatalf("claude session = %q", got)
	}
}

func TestCustomForkOnlyExecutorCanRun(t *testing.T) {
	prepared, err := Prepare("fork-only", Executor{Kind: "command", ForkCommand: []string{"tool", "fork", "{{session_id}}"}}, Invocation{Input: map[string]any{}, SessionMode: "fork", SessionID: "parent"})
	if err != nil || !reflect.DeepEqual(prepared.Command, []string{"tool", "fork", "parent"}) {
		t.Fatalf("fork-only executor = %+v, %v", prepared, err)
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

func TestCheckRelativeExecutableInExecutorWorkspace(t *testing.T) {
	directory := t.TempDir()
	name := "runner"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	status := CheckExecutor("local", Executor{Kind: "command", Workspace: directory, Command: []string{"./" + name}})
	if !status.Available {
		t.Fatalf("relative executable not resolved inside workspace: %+v", status)
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
	config := `{"executors":{"codex":{"command":["echo","{{ config.label }}"],"workspace":"{{ config.directory }}","env":{"CHANNEL":"{{ config.channel }}"}}}}`
	if err := os.WriteFile(filepath.Join(directory, "executors.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	executors, err := LoadExecutors(directory)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare("codex", executors["codex"], Invocation{Config: map[string]any{"label": "custom", "channel": "terminal", "directory": directory}})
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

func TestRunCommandClassifiesStartExitAndTimeout(t *testing.T) {
	missing := RunCommand([]string{"agentflow-command-that-does-not-exist"}, CommandOptions{Dir: t.TempDir()})
	if missing.Started || missing.Code != -1 || missing.Error == "" {
		t.Fatalf("missing executable = %+v", missing)
	}
	missingDirectory := RunCommand([]string{"sh", "-c", "exit 0"}, CommandOptions{Dir: filepath.Join(t.TempDir(), "missing")})
	if missingDirectory.Started || missingDirectory.Error == "" {
		t.Fatalf("missing cwd = %+v", missingDirectory)
	}
	exited := RunCommand([]string{"sh", "-c", "exit 75"}, CommandOptions{Dir: t.TempDir()})
	if !exited.Started || exited.Code != 75 || exited.TimedOut {
		t.Fatalf("nonzero exit = %+v", exited)
	}
	timedOut := RunCommand([]string{"sh", "-c", "sleep 1"}, CommandOptions{Dir: t.TempDir(), TimeoutMs: 20})
	if !timedOut.Started || !timedOut.TimedOut || timedOut.Code != 124 {
		t.Fatalf("timeout = %+v", timedOut)
	}
}

func TestSessionCompatibility(t *testing.T) {
	if !SessionCompatible("codex-a", Executor{Kind: "codex"}, "codex-b", Executor{Kind: "codex"}) {
		t.Fatal("native aliases of one provider should be compatible")
	}
	if SessionCompatible("codex", Executor{Kind: "codex"}, "claude", Executor{Kind: "claude-code"}) {
		t.Fatal("different native providers must be isolated")
	}
	if !SessionCompatible("custom", Executor{Kind: "command"}, "custom", Executor{Kind: "command"}) || SessionCompatible("custom-a", Executor{Kind: "command"}, "custom-b", Executor{Kind: "command"}) {
		t.Fatal("custom sessions require the same registration identity")
	}
	persisted := SessionProtocol("source", Executor{Kind: "codex"})
	if !SessionProtocolCompatible("target", Executor{Kind: "codex"}, persisted) {
		t.Fatal("persisted native protocol should allow another alias of the same provider")
	}
	if SessionProtocolCompatible("target", Executor{Kind: "claude-code"}, persisted) {
		t.Fatal("the target must not reinterpret a persisted session after source registry drift")
	}
}
