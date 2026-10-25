package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/agentflow/internal/driver"
)

func TestInitDryRunConflictAndExecutorScaffolding(t *testing.T) {
	root := filepath.Join(t.TempDir(), "flow")
	if _, err := Init(root, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("dry run created workspace: %v", err)
	}
	if _, err := Init(root, false); err != nil {
		t.Fatal(err)
	}
	if report := Validate(root, ""); !report.Valid {
		t.Fatalf("starter invalid: %+v", report)
	}
	if _, err := Init(root, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("existing workspace overwritten: %v", err)
	}
	if _, err := InitExecutor(root, "writer", "codex", "", false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AGENTS.md", ".agents/skills/workflow-contract/SKILL.md", ".codex/config.toml", "tools/mcp_server.py", "tools/manifest.json", "tools/read_input.py"} {
		if info, err := os.Stat(filepath.Join(root, "executors/agents/writer", name)); err != nil || info.Size() == 0 {
			t.Fatalf("missing usable agent file %s: %v", name, err)
		}
	}
	executors, err := driver.LoadExecutors(root)
	if err != nil {
		t.Fatal(err)
	}
	if executors["writer"].Workspace != "executors/agents/writer" || executors["writer"].Kind != "codex" {
		t.Fatalf("wrong registration: %+v", executors["writer"])
	}
}

func TestExistingAgentIsBoundWithoutCreatingOrOverwritingFiles(t *testing.T) {
	root, external := t.TempDir(), t.TempDir()
	marker := filepath.Join(external, "AGENTS.md")
	if err := os.WriteFile(marker, []byte("existing role"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AddExecutor(root, "existing", "codex", external, "", nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "executors")); !os.IsNotExist(err) {
		t.Fatalf("unnecessary local agent directory created: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "existing role" {
		t.Fatalf("external role changed: %v", err)
	}
	if _, err := InitExecutor(root, "existing", "codex", "", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate entry accepted: %v", err)
	}
}

func TestInitRefusesExistingSymlinkAndValidationFindsBrokenBinding(t *testing.T) {
	root, external := t.TempDir(), t.TempDir()
	if _, err := Init(root, false); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(root, "executors", "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(agents, "linked")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	if _, err := InitExecutor(root, "linked", "codex", "", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("symlink overwritten: %v", err)
	}
	entries, err := os.ReadDir(external)
	if err != nil || len(entries) != 0 {
		t.Fatalf("external directory modified: %v", err)
	}
	if _, err := AddExecutor(root, "linked", "codex", "executors/agents/linked", "", nil, false); err != nil {
		t.Fatal(err)
	}
	flow := `{"version":"3","name":"reuse","entry":"linked","nodes":[{"id":"linked","executor":"linked","execution":{"session":{"mode":"fixed","id":"saved-session"}}}],"edges":[{"from":"linked","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if report := Validate(root, ""); report.Valid {
		t.Fatalf("broken binding validated: %+v", report)
	}
}

func TestConflictPreflightCreatesNoPartialWorkspace(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "workflow.json")
	if err := os.WriteFile(file, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(root, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict not detected: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("partially initialized workspace: %v %+v", err, entries)
	}
}

func TestValidationUsesTriggerTargetsWithoutRequiringDefaultGraph(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "workflow.json"), filepath.Join(root, "morning.json")); err != nil {
		t.Fatal(err)
	}
	config := `{"triggers":[{"id":"morning","enabled":true,"source":{"kind":"cron","expression":"0 9 * * *"},"target":{"workflow":"morning.json"}}]}`
	if err := os.WriteFile(filepath.Join(root, "triggers.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if report := Validate(root, ""); !report.Valid || len(report.Bindings) != 1 {
		t.Fatalf("trigger target not validated: %+v", report)
	}
	if report := Validate(root, "missing.json"); report.Valid {
		t.Fatalf("explicit missing graph passed validation: %+v", report)
	}
}
