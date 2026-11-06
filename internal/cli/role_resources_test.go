package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWorkspaceCLIResolvesAgentResourcesWithRepresentativeInput(t *testing.T) {
	root := t.TempDir()
	_, restore := chdirForTest(t, root)
	defer restore()
	for _, args := range [][]string{{"workspace", "init", "--format", "json"}, {"executor", "init", "specialist", "--kind", "claude-code", "--format", "json"}} {
		if output, err := captureRunOutput(t, args); err != nil {
			t.Fatalf("%v: %s", err, output)
		}
	}
	flow := `{"version":"3","name":"roles","entry":"assess","inputs":{"directory":{"type":"string","required":true}},"nodes":[{"id":"assess","execution":{"candidates":[{"executor":"specialist","config":{"workspace":"{{ inputs.directory }}"}}]}}],"edges":[{"from":"assess","to":"$end"}]}`
	if err := os.WriteFile("workflow.json", []byte(flow), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, _ := json.Marshal(map[string]any{"executors": map[string]any{"specialist": map[string]any{"kind": "claude-code", "workspace": "{{ config.workspace }}"}}})
	if err := os.WriteFile("executors.json", registry, 0o600); err != nil {
		t.Fatal(err)
	}
	// The fake executable is only a discovery fixture. Validation must not run it.
	bin := t.TempDir()
	name := "claude"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 91\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := captureRunOutput(t, []string{"workspace", "validate", "--input", "directory=executors/agents/specialist", "--format", "json"})
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	var report struct {
		Valid          bool `json:"valid"`
		AgentResources []struct {
			Status       string `json:"workspace_status"`
			Instructions []any  `json:"local_instructions"`
			Verified     bool   `json:"effective_context_verified"`
		} `json:"agent_resources"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatal(err)
	}
	if !report.Valid || len(report.AgentResources) != 1 || report.AgentResources[0].Status != "inspected" || len(report.AgentResources[0].Instructions) != 1 || report.AgentResources[0].Verified {
		t.Fatalf("incorrect resources report: %s", output)
	}
}
