package workspace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestValidationPreservesSessionAlternativesThroughMultipleBridges(t *testing.T) {
	for _, depth := range []int{1, 4} {
		for _, adaptive := range []bool{false, true} {
			t.Run(fmt.Sprintf("depth-%d-adaptive-%v", depth, adaptive), func(t *testing.T) {
				root := t.TempDir()
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				// These entries are inspected for availability; no model or test
				// executable is launched by workspace validation.
				registry := map[string]any{"executors": map[string]any{
					"router": map[string]any{"kind": "command", "command": []string{executable}},
					"codex":  map[string]any{"kind": "codex", "command": []string{executable}, "resumeCommand": []string{executable}},
					"claude": map[string]any{"kind": "claude-code", "command": []string{executable}, "resumeCommand": []string{executable}},
				}}
				candidate := func(executor, from string, inherit bool) map[string]any {
					result := map[string]any{"executor": executor}
					if inherit {
						session := map[string]any{"mode": "inherit"}
						if from != "" {
							session["from"] = from
						}
						result["session"] = session
					}
					return result
				}
				node := func(id string, candidates []any, fallback bool) map[string]any {
					execution := map[string]any{"candidates": candidates}
					if fallback {
						execution["fallback"] = map[string]any{"when": "failure.kind == 'session_incompatible'"}
					}
					return map[string]any{"id": id, "execution": execution}
				}
				pool := func(from string) []any {
					return []any{candidate("codex", from, true), candidate("claude", from, true)}
				}
				nodes := []any{node("router", []any{candidate("router", "", false)}, false),
					node("a", []any{candidate("codex", "", false)}, false),
					node("b", []any{candidate("claude", "", false)}, false), node("source", pool(""), true)}
				edges := []any{map[string]any{"from": "router", "to": "a", "when": "inputs.route == 'codex'"},
					map[string]any{"from": "router", "to": "b"}, map[string]any{"from": "a", "to": "source"},
					map[string]any{"from": "b", "to": "source"}}
				previous := "source"
				for i := 0; i < depth; i++ {
					id := fmt.Sprintf("bridge-%d", i)
					nodes = append(nodes, node(id, pool(previous), true))
					edges = append(edges, map[string]any{"from": previous, "to": id})
					previous = id
				}
				target := []any{candidate("codex", previous, true)}
				if adaptive {
					target = pool(previous)
				}
				nodes = append(nodes, node("work", target, adaptive))
				edges = append(edges, map[string]any{"from": previous, "to": "work"}, map[string]any{"from": "work", "to": "$end"})
				flow := map[string]any{"version": "3", "name": "deep-source", "entry": "router", "nodes": nodes, "edges": edges,
					"inputs": map[string]any{"route": map[string]any{"type": "string", "required": true}}}
				for name, value := range map[string]any{"workflow.json": flow, "executors.json": registry} {
					data, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				for _, route := range []string{"", "codex", "claude"} {
					inputs := map[string]string{}
					if route != "" {
						inputs["route"] = route
					}
					result := ValidateWithInputs(root, "", inputs)
					if want := adaptive || route == "codex"; result.Valid != want {
						t.Fatalf("route=%q valid=%v want=%v: %+v", route, result.Valid, want, result.Issues)
					}
				}
			})
		}
	}
}

func TestValidationDoesNotTreatInterruptedRecoveryAsSourceFallback(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	registry := map[string]any{"executors": map[string]any{
		"codex":  map[string]any{"kind": "codex", "command": []string{executable}, "resumeCommand": []string{executable}},
		"claude": map[string]any{"kind": "claude-code", "command": []string{executable}, "resumeCommand": []string{executable}},
	}}
	flow := map[string]any{
		"version": "3", "name": "interrupted-source-fallback", "entry": "source",
		"nodes": []any{
			map[string]any{"id": "source", "execution": map[string]any{
				"fallback":   map[string]any{"when": "failure.kind == 'interrupted'"},
				"candidates": []any{map[string]any{"executor": "codex"}, map[string]any{"executor": "claude"}},
			}},
			map[string]any{"id": "work", "execution": map[string]any{
				"candidates": []any{map[string]any{"executor": "claude", "session": map[string]any{"mode": "inherit", "from": "source"}}},
			}},
		},
		"edges": []any{map[string]any{"from": "source", "to": "work"}, map[string]any{"from": "work", "to": "$end"}},
	}
	for name, value := range map[string]any{"workflow.json": flow, "executors.json": registry} {
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
		t.Fatalf("interrupted recovery made an unreachable cross-provider source candidate usable: %+v", result.Issues)
	}
	found := false
	for _, issue := range result.Issues {
		if issue.Path == "workflow.json:work:candidate-1:incoming-source" && issue.Severity == "warning" && issue.Message == "node work session mode inherit cannot resolve a compatible session from node source" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing cross-provider session incompatibility: %+v", result.Issues)
	}
}

func TestValidationPreservesConfigurationFallbackForInconclusiveSourceCommand(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	registry := map[string]any{"executors": map[string]any{
		"process": map[string]any{"kind": "command"},
		"claude":  map[string]any{"kind": "claude-code", "command": []string{executable}, "resumeCommand": []string{executable}},
	}}
	flow := map[string]any{
		"version": "3", "name": "inconclusive-source-configuration-fallback", "entry": "producer",
		"nodes": []any{
			map[string]any{"id": "producer", "execution": map[string]any{"candidates": []any{
				map[string]any{"executor": "process", "config": map[string]any{"command": []string{executable}}},
			}}},
			map[string]any{"id": "source", "execution": map[string]any{
				"fallback": map[string]any{"when": "failure.kind == 'configuration_failed'"},
				"candidates": []any{
					map[string]any{"executor": "process", "config": map[string]any{"command": []string{"{{ input.cmd }}"}}},
					map[string]any{"executor": "claude"},
				},
			}},
			map[string]any{"id": "work", "execution": map[string]any{"candidates": []any{
				map[string]any{"executor": "claude", "session": map[string]any{"mode": "inherit", "from": "source"}},
			}}},
		},
		"edges": []any{
			map[string]any{"from": "producer", "to": "source"},
			map[string]any{"from": "source", "to": "work"},
			map[string]any{"from": "work", "to": "$end"},
		},
	}
	for name, value := range map[string]any{"workflow.json": flow, "executors.json": registry} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result := Validate(root, "")
	if !result.Valid {
		t.Fatalf("configuration fallback from an inconclusive source command was pruned: %+v", result.Issues)
	}
}
