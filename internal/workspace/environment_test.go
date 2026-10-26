package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironmentPolicyDiagnosticsWithoutValues(t *testing.T) {
	root := t.TempDir()
	flow := `{"version":"3","name":"environment","entry":"work","nodes":[{"id":"work","execution":{"candidates":[{"executor":"worker"}]}}],"edges":[{"from":"work","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := `{"executors":{"worker":{"kind":"command","command":["sh","-c","exit 0"],"environment":{"mode":"allowlist","inherit":["PATH"]},"env":{"LOCAL_SYNTHETIC":"synthetic-diagnostic-canary"}}}}`
	if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	result := Validate(root, "workflow.json")
	if !result.Valid || len(result.Bindings) != 1 {
		t.Fatalf("policy validation failed: %v", result.Issues)
	}
	policy, ok := result.Bindings[0]["environment"].(map[string]any)
	if !ok || policy["mode"] != "allowlist" {
		t.Fatal("environment policy missing in diagnostics")
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "synthetic-diagnostic-canary") {
		t.Fatal("environment value appeared in diagnostics")
	}
	for _, bad := range []string{`null`, `{}`, `{"mode":"allowlist","inherit":null}`, `{"mode":"allowlist","inherit":["AGENTFLOW_RESULT_FILE"]}`, `{"mode":"empty","inherit":["PATH"]}`, `{"mode":"unknown"}`, `{"mode":"empty","unexpected":true}`} {
		registry := `{"executors":{"worker":{"kind":"command","command":["sh","-c","exit 0"],"environment":` + bad + `}}}`
		if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o600); err != nil {
			t.Fatal(err)
		}
		if result := Validate(root, "workflow.json"); result.Valid {
			t.Fatalf("invalid policy accepted: %s", bad)
		}
	}
	registry = `{"executors":{"worker":{"kind":"command","command":["sh","-c","exit 0"],"environment":{"mode":"empty"},"ENVIRONMENT":null}}}`
	if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := Validate(root, "workflow.json"); result.Valid {
		t.Fatal("case-aliased environment silently disabled the policy")
	}
}
