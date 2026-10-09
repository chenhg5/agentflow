package engine

import (
	"strings"
	"testing"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestAgentRequiresStructuredResult(t *testing.T) {
	workflow := model.Workflow{Version: "1", Name: "result", Nodes: []model.Node{{ID: "agent", Type: "agent", Driver: "fake", Prompt: "work"}}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(run, map[string]driver.Provider{"fake": {Command: []string{"sh", "-c", "exit 0"}}})
	if err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "failed" || !strings.Contains(run.State.Nodes["agent"].Error, "structured result is missing") {
		t.Fatalf("expected structured result failure, got status=%s error=%q", run.State.Status, run.State.Nodes["agent"].Error)
	}
}

func TestAgentStoresStructuredResultSeparatelyFromLogs(t *testing.T) {
	workflow := model.Workflow{Version: "1", Name: "result", Nodes: []model.Node{{ID: "agent", Type: "agent", Driver: "fake", Prompt: "work"}}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = Execute(run, map[string]driver.Provider{"fake": {Command: []string{"sh", "-c", "printf '{\"ok\":true}' > \"$AGENTFLOW_RESULT_FILE\"; printf 'log\\n'"}}})
	if err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "succeeded" {
		t.Fatalf("status = %s", run.State.Status)
	}
	result, ok := run.State.Nodes["agent"].Result.(map[string]any)
	if !ok || result["ok"] != true {
		t.Fatalf("unexpected result: %#v", run.State.Nodes["agent"].Result)
	}
	if _, ok := run.State.Nodes["agent"].Artifacts["stdoutLog"]; !ok {
		t.Fatal("stdout should be recorded as a log artifact")
	}
}
