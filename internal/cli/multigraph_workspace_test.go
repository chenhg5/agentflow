package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/agentflow/internal/store"
	"github.com/chenhg5/agentflow/internal/workspace"
)

func TestMultiGraphWorkspaceSharesRegistryAndRootEvidence(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, restore := chdirForTest(t, root)
	defer restore()
	if _, err := captureRunOutput(t, []string{"workspace", "init", "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(root, "workflow.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		var flow map[string]any
		if err := json.Unmarshal(source, &flow); err != nil {
			t.Fatal(err)
		}
		flow["name"] = name
		data, err := json.Marshal(flow)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "workflows", name+".json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Join(root, "workflow.json")); err != nil {
		t.Fatal(err)
	}
	triggers := `{"triggers":[
		{"id":"first","enabled":true,"source":{"kind":"cron","expression":"0 9 * * *"},"target":{"workflow":"workflows/first.json"}},
		{"id":"second","enabled":true,"source":{"kind":"cron","expression":"0 10 * * *"},"target":{"workflow":"workflows/second.json"}}
	]}`
	if err := os.WriteFile(filepath.Join(root, "triggers.json"), []byte(triggers), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := captureRunOutput(t, []string{"workspace", "validate", "--format", "json"})
	if err != nil {
		t.Fatalf("multi graph validation: %v: %s", err, output)
	}
	var report workspace.Validation
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatal(err)
	}
	if !report.Valid || len(report.Bindings) != 2 || report.RunStore != filepath.Join(root, "runs") {
		t.Fatalf("both trigger targets must use one root store: %+v", report)
	}
	for _, name := range []string{"first", "second"} {
		output, err := captureRunOutput(t, []string{"run", "start", "workflows/" + name + ".json", "--input", "value=" + name, "--format", "json"})
		if err != nil {
			t.Fatalf("run %s: %v: %s", name, err, output)
		}
		var started struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(output, &started); err != nil {
			t.Fatal(err)
		}
		run, err := store.LoadRun(root, started.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if run.State.Status != "succeeded" || run.Workflow.Name != name || run.State.Nodes["step"].Result.(map[string]any)["value"] != name {
			t.Fatalf("graph/input identity lost: %+v", run.State)
		}
		if run.State.Nodes["step"].Workspace != filepath.Join(root, "executors", "programs", "step") {
			t.Fatalf("executor resolved against graph directory: %+v", run.State.Nodes["step"])
		}
		if _, err := os.Stat(filepath.Join(root, "runs", started.RunID, "workflow.json")); err != nil {
			t.Fatalf("missing root run snapshot: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "workflows", "runs")); !os.IsNotExist(err) {
		t.Fatalf("graph directory became a separate workspace: %v", err)
	}
	// A missing second trigger target must not be hidden by the valid first graph.
	if err := os.Remove(filepath.Join(root, "workflows", "second.json")); err != nil {
		t.Fatal(err)
	}
	if report := workspace.Validate(root, ""); report.Valid {
		t.Fatal("missing trigger target accepted")
	}
}
