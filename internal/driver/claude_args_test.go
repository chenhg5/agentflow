package driver

import (
	"reflect"
	"testing"
)

func TestClaudePromptBoundaryAndModel(t *testing.T) {
	command, err := BuildCommand("reviewer", Executor{Kind: "claude-code", Model: "chosen-model", ExtraArgs: []string{"--allowedTools", "Read,Write"}}, Request{Prompt: "--task-data"})
	expected := []string{"claude", "--print", "--output-format", "json", "--model", "chosen-model", "--allowedTools", "Read,Write", "--", "--task-data"}
	if err != nil || !reflect.DeepEqual(command, expected) {
		t.Fatalf("task can be consumed as a variadic tool option: %v %v", command, err)
	}
}

func TestClaudeStdinPromptIsNotAlsoAnArgument(t *testing.T) {
	prepared, err := Prepare("reviewer", Executor{Kind: "claude-code", PromptMode: "stdin"}, Invocation{Input: map[string]any{"value": "sample"}, Config: map[string]any{"instruction": "Inspect data"}, ResultFile: "/result.json"})
	if err != nil || prepared.Stdin == "" {
		t.Fatalf("missing prompt transport: %+v %v", prepared, err)
	}
	for _, arg := range prepared.Command {
		if arg == prepared.Stdin {
			t.Fatal("stdin prompt also leaked into argv")
		}
	}
}
