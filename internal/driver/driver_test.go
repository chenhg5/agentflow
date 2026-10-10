package driver

import (
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
	got, err := BuildCommand("claude-code", Provider{Kind: "claude-code", Binary: "claude", ExtraArgs: []string{"--permission-mode", "acceptEdits"}}, Request{Prompt: "inspect", SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude", "--resume", "session-1", "--print", "--permission-mode", "acceptEdits", "inspect"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildCodexCommand(t *testing.T) {
	provider := Provider{Kind: "codex", Binary: "codex", Model: "gpt-5.6-sol", Sandbox: "workspace-write", SkipGitRepoCheck: true, Ephemeral: true}
	got, err := BuildCommand("codex", provider, Request{Prompt: "verify", SessionID: "session-2"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "exec", "resume", "-m", "gpt-5.6-sol", "--ephemeral", "session-2", "verify"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildCodexStartCommandIncludesSandbox(t *testing.T) {
	provider := Provider{Kind: "codex", Binary: "codex", Model: "gpt-5.6-sol", Sandbox: "workspace-write", SkipGitRepoCheck: true, Ephemeral: true}
	got, err := BuildCommand("codex", provider, Request{Prompt: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"codex", "exec", "-m", "gpt-5.6-sol", "-s", "workspace-write", "--skip-git-repo-check", "--ephemeral", "inspect"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuiltInProviders(t *testing.T) {
	providers := BuiltInProviders()
	if providers["claude-code"].Kind != "claude-code" || providers["codex"].Kind != "codex" {
		t.Fatalf("built-in providers = %#v", providers)
	}
}

func TestCheckProvider(t *testing.T) {
	available := CheckProvider("shell", Provider{Kind: "command", Command: []string{"sh"}})
	if !available.Available || available.Binary != "sh" {
		t.Fatalf("available provider = %#v", available)
	}
	missing := CheckProvider("missing", Provider{Kind: "command", Command: []string{"agentflow-command-that-does-not-exist"}})
	if missing.Available || missing.Error == "" {
		t.Fatalf("missing provider = %#v", missing)
	}
}

func TestBuildCommandProviderRequiresResumeTemplate(t *testing.T) {
	_, err := BuildCommand("custom", Provider{Kind: "command", Command: []string{"agent", "{{ prompt }}"}}, Request{Prompt: "continue", SessionID: "session-3"})
	if err == nil {
		t.Fatal("expected missing resume command error")
	}
}
