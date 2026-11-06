package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidationChecksIndependentLaunchRequirements(t *testing.T) {
	for _, unknownWorkspace := range []bool{false, true} {
		for _, targetNative := range []bool{false, true} {
			for _, disabled := range []bool{false, true} {
				t.Run(fmt.Sprintf("unknown-workspace-%v-native-%v-disabled-%v", unknownWorkspace, targetNative, disabled), func(t *testing.T) {
					root := t.TempDir()
					executable, err := os.Executable()
					if err != nil {
						t.Fatal(err)
					}
					command := []string{executable}
					worker := map[string]any{"kind": "command", "workspace": "{{ config.dir }}", "resumeCommand": command}
					config := map[string]any{"dir": filepath.Join(root, "missing"), "command": "{{ input.cmd }}"}
					condition := "failure.kind == 'configuration_failed'"
					if unknownWorkspace {
						worker["command"] = []string{filepath.Join(root, "missing-binary")}
						config = map[string]any{"dir": "{{ input.dir }}"}
						condition = "failure.kind == 'start_failed'"
					}
					if disabled {
						condition = "false"
					}
					target := "worker"
					if targetNative {
						target = "native"
					}
					registry := map[string]any{"executors": map[string]any{
						"producer": map[string]any{"kind": "command", "command": command},
						"worker":   worker,
						"native":   map[string]any{"kind": "claude-code", "command": command, "resumeCommand": command},
					}}
					flow := map[string]any{"version": "3", "name": "independent-launch", "entry": "producer",
						"nodes": []any{
							map[string]any{"id": "producer", "execution": map[string]any{"candidates": []any{map[string]any{"executor": "producer"}}}},
							map[string]any{"id": "source", "execution": map[string]any{"candidates": []any{
								map[string]any{"executor": "worker", "config": config}, map[string]any{"executor": "native"}},
								"fallback": map[string]any{"when": condition}}},
							map[string]any{"id": "work", "execution": map[string]any{"candidates": []any{
								map[string]any{"executor": target, "config": map[string]any{"dir": root}, "session": map[string]any{"mode": "inherit", "from": "source"}}}}},
						}, "edges": []any{
							map[string]any{"from": "producer", "to": "source"}, map[string]any{"from": "source", "to": "work"}, map[string]any{"from": "work", "to": "$end"},
						}}
					for name, value := range map[string]any{"executors.json": registry, "workflow.json": flow} {
						data, err := json.Marshal(value)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
							t.Fatal(err)
						}
					}
					result := Validate(root, "")
					if want := targetNative && !disabled; result.Valid != want {
						t.Fatalf("valid=%v want=%v: %+v", result.Valid, want, result.Issues)
					}
				})
			}
		}
	}
}

func TestValidationProbesLaunchForUnresolvedFixedSessionID(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	registry := map[string]any{"executors": map[string]any{
		"agent": map[string]any{
			"kind": "codex", "workspace": filepath.Join(root, "missing"),
			"command": []string{executable}, "resumeCommand": []string{executable},
		},
	}}
	flow := map[string]any{
		"version": "3", "name": "unresolved-fixed-launch", "entry": "work",
		"inputs": map[string]any{"sid": map[string]any{"type": "string", "required": true}},
		"nodes": []any{map[string]any{
			"id": "work", "execution": map[string]any{"candidates": []any{map[string]any{
				"executor": "agent", "session": map[string]any{"mode": "fixed", "id": "{{ inputs.sid }}"},
			}}},
		}},
		"edges": []any{map[string]any{"from": "work", "to": "$end"}},
	}
	for name, value := range map[string]any{"executors.json": registry, "workflow.json": flow} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result := Validate(root, "")
	if result.Valid {
		t.Fatalf("an unresolved fixed session masked a known missing workspace: %+v", result.Issues)
	}
	foundWorkspace := false
	for _, issue := range result.Issues {
		if issue.Path == "workflow.json:work:candidate-1" && issue.Severity == "warning" && strings.Contains(issue.Message, "workspace unavailable") {
			foundWorkspace = true
		}
	}
	if !foundWorkspace {
		t.Fatalf("the non-empty session branch was not launch-probed: %+v", result.Issues)
	}
}

func TestValidationDoesNotInventSourceSessionForUnresolvedFixedID(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	registry := map[string]any{"executors": map[string]any{
		"source-agent": map[string]any{
			"kind": "codex", "workspace": filepath.Join(root, "missing"),
			"command": []string{executable}, "resumeCommand": []string{executable},
		},
		"target-agent": map[string]any{
			"kind": "codex", "workspace": root,
			"command": []string{executable}, "resumeCommand": []string{executable},
		},
	}}
	flow := map[string]any{
		"version": "3", "name": "unresolved-fixed-source", "entry": "source",
		"inputs": map[string]any{"sid": map[string]any{"type": "string", "required": true}},
		"nodes": []any{
			map[string]any{"id": "source", "execution": map[string]any{
				"fallback": map[string]any{"when": "failure.kind == 'session_incompatible' || failure.kind == 'start_failed'"},
				"candidates": []any{
					map[string]any{"executor": "source-agent", "session": map[string]any{"mode": "fixed", "id": "{{ inputs.sid }}"}},
					map[string]any{"executor": "external"},
				},
			}},
			map[string]any{"id": "work", "execution": map[string]any{"candidates": []any{
				map[string]any{"executor": "target-agent", "session": map[string]any{"mode": "inherit", "from": "source"}},
			}}},
		},
		"edges": []any{
			map[string]any{"from": "source", "to": "work"},
			map[string]any{"from": "work", "to": "$end"},
		},
	}
	for name, value := range map[string]any{"executors.json": registry, "workflow.json": flow} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result := Validate(root, "")
	if result.Valid {
		t.Fatalf("an unlaunchable source candidate was fabricated as a provider session: %+v", result.Issues)
	}
	foundSessionFailure := false
	for _, issue := range result.Issues {
		if issue.Path == "workflow.json:work:candidate-1:incoming-source" && strings.Contains(issue.Message, "cannot resolve a compatible session") {
			foundSessionFailure = true
		}
	}
	if !foundSessionFailure {
		t.Fatalf("the stateless source fallback was treated as session-capable: %+v", result.Issues)
	}
}
