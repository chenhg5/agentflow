package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestValidationDistinguishesPriorWaveResultFromWaiting(t *testing.T) {
	for _, mode := range []string{"waiting", "process", "disabled-waiting", "interrupted-waiting", "configuration-waiting"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := []string{executable}
			registry := map[string]any{"executors": map[string]any{
				"producer": map[string]any{"kind": "command", "command": command},
				"channel":  map[string]any{"kind": "external"},
				"worker":   map[string]any{"kind": "command", "resumeCommand": command},
				"native":   map[string]any{"kind": "claude-code", "command": command, "resumeCommand": command},
			}}
			waitingExecution := map[string]any{"candidates": []any{map[string]any{"executor": "channel"}}}
			if mode != "waiting" {
				waitingExecution["candidates"] = []any{map[string]any{"executor": "producer"}}
				if mode != "process" {
					waitingExecution["candidates"] = append(waitingExecution["candidates"].([]any), map[string]any{"executor": "channel"})
					condition := "false"
					if mode == "interrupted-waiting" {
						condition = "failure.kind == 'interrupted'"
					} else if mode == "configuration-waiting" {
						condition = "failure.kind == 'configuration_failed'"
					}
					waitingExecution["fallback"] = map[string]any{"when": condition}
				}
			}
			flow := map[string]any{"version": "3", "name": "parallel-result", "entry": "seed",
				"nodes": []any{
					map[string]any{"id": "seed", "dispatch": "all", "execution": map[string]any{"candidates": []any{map[string]any{"executor": "producer"}}}},
					map[string]any{"id": "waiting", "execution": waitingExecution, "outputSchema": map[string]any{"type": "object", "required": []any{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
					map[string]any{"id": "branch", "execution": map[string]any{"candidates": []any{map[string]any{"executor": "producer"}}}},
					map[string]any{"id": "source", "execution": map[string]any{"candidates": []any{
						map[string]any{"executor": "worker", "config": map[string]any{"command": []any{executable, "{{ nodes.waiting.result.text }}"}}}, map[string]any{"executor": "native"}},
						"fallback": map[string]any{"when": "failure.kind == 'configuration_failed'"}}},
					map[string]any{"id": "work", "execution": map[string]any{"candidates": []any{map[string]any{"executor": "native", "session": map[string]any{"mode": "inherit", "from": "source"}}}}},
				}, "edges": []any{
					map[string]any{"from": "seed", "to": "waiting"}, map[string]any{"from": "seed", "to": "branch"},
					map[string]any{"from": "waiting", "to": "$end"}, map[string]any{"from": "branch", "to": "source"},
					map[string]any{"from": "source", "to": "work"}, map[string]any{"from": "work", "to": "$end"},
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
			if want := mode == "waiting"; result.Valid != want {
				t.Fatalf("valid=%v want=%v: %+v", result.Valid, want, result.Issues)
			}
		})
	}
}
