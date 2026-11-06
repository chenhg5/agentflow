package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/agentflow/internal/store"
)

func TestCodeRunRecordsAScript(t *testing.T) {
	workspace := t.TempDir()
	previous, _ := os.Getwd()
	if err := os.Chdir(workspace); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(previous) })
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTFLOW_HUB_URL", "")
	if err := Run([]string{"code", "start", "nightly", "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	ids, _ := store.RunIDs(workspace)
	if len(ids) != 1 {
		t.Fatalf("runs = %v", ids)
	}
	t.Setenv(runIDEnv, ids[0])
	if err := Run([]string{"code", "step", "fetch", "--", "sh", "-c", `echo fetched; printf '{"count":3}' > "$AGENTFLOW_RESULT_FILE"`}); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"code", "step", "decide", "--output", `{"ship":true}`, "--summary", "ship it"}); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"code", "step", "check", "--", "sh", "-c", "echo broken >&2; exit 4"}); ExitCode(err) != 4 {
		t.Fatalf("a failing command's exit code passes through: %v", err)
	}
	if err := Run([]string{"code", "end", "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	run, err := store.LoadRun(workspace, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "failed" || run.State.Nodes["check"].Status != "failed" {
		t.Fatalf("status %s, check %s", run.State.Status, run.State.Nodes["check"].Status)
	}
	fetch := run.State.Nodes["fetch"]
	if fetch.Result.(map[string]any)["count"] != 3.0 || fetch.SelectedExecutor != "sh" || fetch.StdoutBytes == 0 {
		t.Fatalf("fetch = %+v", fetch)
	}
	if decide := run.State.Nodes["decide"].Result.(map[string]any); decide["ship"] != true || decide["summary"] != "ship it" {
		t.Fatalf("decide = %+v", decide)
	}
	log, _ := os.ReadFile(filepath.Join(run.RunDir, filepath.FromSlash(run.State.Nodes["check"].Artifacts["stderrLog"])))
	if !strings.Contains(string(log), "broken") {
		t.Fatalf("stderr log = %q", log)
	}
	if err := Run([]string{"run", "resume", ids[0]}); err == nil || !strings.Contains(err.Error(), "code run") {
		t.Fatalf("the engine must not resume a code run: %v", err)
	}
}
