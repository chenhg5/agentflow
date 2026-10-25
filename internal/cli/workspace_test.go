package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/agentflow/internal/store"
)

func TestInitializedWorkspaceRunsAndStoresNodeEvidenceAtRoot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, restore := chdirForTest(t, root)
	defer restore()
	if _, err := captureRunOutput(t, []string{"workspace", "init", "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := captureRunOutput(t, []string{"workspace", "validate", "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	output, err := captureRunOutput(t, []string{"run", "start", "workflow.json", "--input", "value=sample", "--format", "json"})
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	var result struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	run, err := store.LoadRun(root, result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if run.State.Status != "succeeded" || state.Result.(map[string]any)["value"] != "sample" {
		t.Fatalf("starter did not pass through JSON: %+v", state)
	}
	for _, path := range state.Artifacts {
		if _, err := os.Stat(filepath.Join(run.RunDir, filepath.FromSlash(path))); err != nil {
			t.Fatalf("node artifact stored at wrong path: %s: %v", path, err)
		}
	}
	if state.Workspace != filepath.Join(root, "executors", "programs", "step") {
		t.Fatalf("wrong process workspace: %s", state.Workspace)
	}
}

func TestRunListIgnoresNonRunEvidenceDirectories(t *testing.T) {
	root := t.TempDir()
	_, restore := chdirForTest(t, root)
	defer restore()
	if err := os.MkdirAll(filepath.Join(root, "runs", "verification"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := captureRunOutput(t, []string{"run", "list", "--page-all", "--format", "json"}); err != nil {
		t.Fatalf("unrelated directory broke run listing: %v", err)
	}
}

func TestConfigInitAndDoctorUseWorkspaceRoot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, restore := chdirForTest(t, root)
	defer restore()
	if _, err := captureRunOutput(t, []string{"config", "init", "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "executors.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agentflow")); !os.IsNotExist(err) {
		t.Fatalf("unexpected hidden workspace: %v", err)
	}
	output, err := captureRunOutput(t, []string{"doctor", "--format", "json"})
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		RunStore string `json:"run_store"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.RunStore != filepath.Join(root, "runs") {
		t.Fatalf("run store = %q", result.RunStore)
	}
}

func TestStandaloneWorkspaceResumesAgentThroughSymlink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	existing := t.TempDir()
	link := filepath.Join(root, "executors", "agents", "existing")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(existing, link); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existing, "existing-work.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := map[string]any{"executors": map[string]any{"existing-agent": map[string]any{
		"kind": "command", "workspace": "executors/agents/existing",
		"command":       []string{"sh", "-c", "exit 25"},
		"resumeCommand": []string{"sh", "-c", `test "$AGENTFLOW_SESSION_ID" = existing-session && test "$AGENTFLOW_ROOT" = "$EXPECTED_ROOT" && test "$(cat existing-work.txt)" = keep && printf '{"reused":true}' > "$AGENTFLOW_RESULT_FILE"`},
		"env":           map[string]string{"EXPECTED_ROOT": root},
	}}}
	data, _ := json.Marshal(registry)
	if err := os.WriteFile(filepath.Join(root, "executors.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	flow := `{"version":"3","name":"external-workspace","entry":"continue","nodes":[{"id":"continue","executor":"existing-agent","execution":{"session":{"mode":"fixed","id":"existing-session"}}}],"edges":[{"from":"continue","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	_, restore := chdirForTest(t, root)
	defer restore()
	output, err := captureRunOutput(t, []string{"run", "start", "workflow.json", "--format", "json"})
	if err != nil {
		t.Fatalf("run: %v: %s", err, output)
	}
	var result struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	run, err := store.LoadRun(root, result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["continue"]
	if run.State.Status != "succeeded" || state.SessionID != "existing-session" || state.Workspace != link {
		t.Fatalf("unexpected reused agent: %+v", state)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("existing directory link replaced: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(existing, "existing-work.txt")); err != nil || string(content) != "keep" {
		t.Fatalf("existing workspace changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agentflow")); !os.IsNotExist(err) {
		t.Fatalf("unexpected hidden runtime directory: %v", err)
	}
}
