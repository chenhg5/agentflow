package engine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func shellNode(id, script string, needs ...string) model.Node {
	return model.Node{ID: id, Type: "shell", Command: []string{"sh", "-c", script}, Needs: needs}
}

const ok = `echo '{}' > "$AGENTFLOW_RESULT_FILE"`

func newRun(t *testing.T, nodes ...model.Node) *model.Run {
	t.Helper()
	run, err := store.CreateRun(t.TempDir(), model.Workflow{Version: "1", Name: "test", Nodes: nodes}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func mustExecute(t *testing.T, run *model.Run) {
	t.Helper()
	if err := Execute(run, map[string]driver.Provider{}); err != nil {
		t.Fatal(err)
	}
}

func statuses(run *model.Run) map[string]string {
	result := map[string]string{}
	for id, node := range run.State.Nodes {
		result[id] = node.Status
	}
	return result
}

func expect(t *testing.T, run *model.Run, runStatus string, nodes map[string]string) {
	t.Helper()
	if run.State.Status != runStatus {
		t.Fatalf("run status = %s, want %s (nodes %v)", run.State.Status, runStatus, statuses(run))
	}
	for id, want := range nodes {
		if got := run.State.Nodes[id].Status; got != want {
			t.Fatalf("node %s = %s, want %s (nodes %v)", id, got, want, statuses(run))
		}
	}
}

func TestIndependentNodesRunInParallel(t *testing.T) {
	run := newRun(t,
		shellNode("a", "sleep 0.6; "+ok),
		shellNode("b", "sleep 0.6; "+ok),
		shellNode("join", ok, "a", "b"),
	)
	started := time.Now()
	mustExecute(t, run)
	expect(t, run, "succeeded", map[string]string{"a": "succeeded", "b": "succeeded", "join": "succeeded"})
	if elapsed := time.Since(started); elapsed > 1100*time.Millisecond {
		t.Fatalf("independent nodes ran sequentially: %s", elapsed)
	}
}

func TestMaxParallelLimitsConcurrency(t *testing.T) {
	run := newRun(t, shellNode("a", "sleep 0.4; "+ok), shellNode("b", "sleep 0.4; "+ok))
	run.Workflow.MaxParallel = 1
	started := time.Now()
	mustExecute(t, run)
	if elapsed := time.Since(started); elapsed < 800*time.Millisecond {
		t.Fatalf("maxParallel 1 should serialize nodes, took %s", elapsed)
	}
}

func TestFailureOnlySkipsDependentsAndRunIfPolicies(t *testing.T) {
	notify := shellNode("notify", ok, "publish")
	notify.RunIf = "failure"
	receipt := shellNode("receipt", ok, "publish", "side")
	receipt.RunIf = "always"
	celebrate := shellNode("celebrate", ok, "side")
	celebrate.RunIf = "failure"
	run := newRun(t,
		shellNode("build", "exit 3"),
		shellNode("publish", ok, "build"),
		shellNode("side", ok),
		notify, receipt, celebrate,
	)
	mustExecute(t, run)
	expect(t, run, "failed", map[string]string{
		"build": "failed", "publish": "skipped", "side": "succeeded",
		"notify": "succeeded", "receipt": "succeeded", "celebrate": "skipped",
	})
	if run.State.Nodes["publish"].Reason != "dependency_failed" || run.State.Nodes["celebrate"].Reason != "no_upstream_failure" {
		t.Fatalf("unexpected skip reasons: %#v %#v", run.State.Nodes["publish"], run.State.Nodes["celebrate"])
	}
}

func TestHumanWaitDoesNotBlockIndependentNodes(t *testing.T) {
	run := newRun(t,
		model.Node{ID: "gate", Type: "human", Prompt: "ship?"},
		shellNode("work", ok),
		shellNode("ship", ok, "gate", "work"),
	)
	mustExecute(t, run)
	expect(t, run, "waiting_human", map[string]string{"gate": "waiting_human", "work": "succeeded", "ship": "pending"})

	if err := store.EnqueueControl(run.RunDir, model.Control{Kind: ControlApprove, NodeID: "gate", Message: "go"}); err != nil {
		t.Fatal(err)
	}
	mustExecute(t, run)
	expect(t, run, "succeeded", map[string]string{"gate": "succeeded", "ship": "succeeded"})
	if run.State.Nodes["gate"].WaitDurationMs <= 0 {
		t.Fatal("human wait time should be recorded")
	}
}

func TestRejectSkipsDependentsButRunsFailureHandlers(t *testing.T) {
	alert := shellNode("alert", ok, "ship")
	alert.RunIf = "failure"
	run := newRun(t,
		model.Node{ID: "gate", Type: "human", Prompt: "ship?"},
		shellNode("ship", ok, "gate"),
		alert,
	)
	mustExecute(t, run)
	if err := store.EnqueueControl(run.RunDir, model.Control{Kind: ControlReject, NodeID: "gate"}); err != nil {
		t.Fatal(err)
	}
	mustExecute(t, run)
	expect(t, run, "failed", map[string]string{"gate": "failed", "ship": "skipped", "alert": "succeeded"})
}

func TestApprovalIsAppliedWhileAnotherBranchIsRunning(t *testing.T) {
	run := newRun(t,
		model.Node{ID: "gate", Type: "human", Prompt: "ship?"},
		shellNode("ship", ok, "gate"),
		shellNode("slow", "sleep 1.5; "+ok),
	)
	done := make(chan error, 1)
	go func() { done <- Execute(run, map[string]driver.Provider{}) }()
	waitFor(t, func() bool { return statusOnDisk(t, run.RunDir, "gate") == "waiting_human" })

	// Another process (the CLI) finds the run busy and leaves the decision queued.
	other, err := store.LoadRun(filepath.Dir(filepath.Dir(run.RunDir)), run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueControl(other.RunDir, model.Control{Kind: ControlApprove, NodeID: "gate"}); err != nil {
		t.Fatal(err)
	}
	if err := Execute(other, map[string]driver.Provider{}); !errors.Is(err, store.ErrRunBusy) {
		t.Fatalf("expected busy executor, got %v", err)
	}
	waitFor(t, func() bool { return statusOnDisk(t, run.RunDir, "ship") == "succeeded" })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	expect(t, run, "succeeded", map[string]string{"gate": "succeeded", "ship": "succeeded", "slow": "succeeded"})
}

func TestRetryReRunsFailedNodesAndKeepsOthers(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "fixed")
	run := newRun(t,
		shellNode("prepare", `echo '{"n":1}' > "$AGENTFLOW_RESULT_FILE"`),
		shellNode("flaky", "test -f "+marker+" && "+ok, "prepare"),
		shellNode("after", ok, "flaky"),
	)
	mustExecute(t, run)
	expect(t, run, "failed", map[string]string{"prepare": "succeeded", "flaky": "failed", "after": "skipped"})
	preparedAt := run.State.Nodes["prepare"].FinishedAt

	if plan, err := RetryPlan(run, nil); err != nil || len(plan) != 2 || plan[0] != "after" || plan[1] != "flaky" {
		t.Fatalf("unexpected retry plan %v %v", plan, err)
	}
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueControl(run.RunDir, model.Control{Kind: ControlRetry}); err != nil {
		t.Fatal(err)
	}
	mustExecute(t, run)
	expect(t, run, "succeeded", map[string]string{"prepare": "succeeded", "flaky": "succeeded", "after": "succeeded"})
	if run.State.Nodes["prepare"].FinishedAt != preparedAt {
		t.Fatal("retry must keep results of nodes outside the retry plan")
	}
}

func TestResumeRecoversNodesOrphanedByACrash(t *testing.T) {
	run := newRun(t, shellNode("a", ok), shellNode("b", ok, "a"))
	run.State.Nodes["a"].Status = "running"
	if err := store.SaveState(run); err != nil {
		t.Fatal(err)
	}
	mustExecute(t, run)
	expect(t, run, "succeeded", map[string]string{"a": "succeeded", "b": "succeeded"})
}

func TestCancelStopsScheduling(t *testing.T) {
	run := newRun(t, shellNode("a", ok), shellNode("b", ok, "a"))
	if err := store.EnqueueControl(run.RunDir, model.Control{Kind: ControlCancel}); err != nil {
		t.Fatal(err)
	}
	mustExecute(t, run)
	expect(t, run, "cancelled", map[string]string{"a": "pending", "b": "pending"})
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func statusOnDisk(t *testing.T, runDir, nodeID string) string {
	t.Helper()
	run := &model.Run{RunDir: runDir}
	if err := store.ReloadState(run); err != nil {
		return ""
	}
	if node := run.State.Nodes[nodeID]; node != nil {
		return node.Status
	}
	return ""
}
