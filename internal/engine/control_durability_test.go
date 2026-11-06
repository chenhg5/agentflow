package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

// A control is acknowledged only after the completed node state is durable. A
// failed save must leave the request queued, keep the run waiting, and emit no
// submission event; a later executor pass must then deliver it exactly once.
func TestFailedSubmissionSaveKeepsControlQueued(t *testing.T) {
	run := graphRun(t, 2)
	executeTestRun(t, run)
	if run.State.Status != "waiting_input" {
		t.Fatalf("expected waiting run: %+v", run.State)
	}
	control := model.Control{Kind: ControlSubmit, NodeID: "review", Token: run.State.Nodes["review"].LeaseToken, Output: map[string]any{"decision": "happy", "message": "go"}}
	if err := store.EnqueueControl(run.RunDir, control); err != nil {
		t.Fatal(err)
	}
	// Block the atomic state write at its temporary-file step.
	temporary := filepath.Join(run.RunDir, "state.json.tmp")
	if err := os.Mkdir(temporary, 0o755); err != nil {
		t.Fatal(err)
	}
	err := Execute(run, map[string]driver.Executor{})
	if err == nil {
		t.Fatalf("execute succeeded despite an unwritable state file")
	}
	if err := os.RemoveAll(temporary); err != nil {
		t.Fatal(err)
	}

	queued, err := store.PeekControls(run.RunDir)
	if err != nil || len(queued) != 1 || queued[0].Control.NodeID != "review" {
		t.Fatalf("failed delivery acknowledged the control: queued=%+v err=%v", queued, err)
	}
	data, err := os.ReadFile(filepath.Join(run.RunDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var durable model.RunState
	if err := json.Unmarshal(data, &durable); err != nil {
		t.Fatal(err)
	}
	if durable.Status != "waiting_input" || durable.Nodes["review"].Status != "waiting_input" || durable.Nodes["review"].Result != nil {
		t.Fatalf("durable state changed without a completed checkpoint: %+v", durable.Nodes["review"])
	}
	events, err := os.ReadFile(filepath.Join(run.RunDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(events, []byte(`"type":"node.submitted"`)) {
		t.Fatalf("submission event was emitted before the state checkpoint: %s", events)
	}

	// Once the file is writable the retained control is delivered normally.
	executeTestRun(t, run)
	if run.State.Status != "succeeded" || run.State.Nodes["review"].Result.(map[string]any)["decision"] != "happy" {
		t.Fatalf("retained control was not delivered: %+v", run.State)
	}
	queued, err = store.PeekControls(run.RunDir)
	if err != nil || len(queued) != 0 {
		t.Fatalf("delivered control was not acknowledged: queued=%+v err=%v", queued, err)
	}
	events, err = os.ReadFile(filepath.Join(run.RunDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(events), `"type":"node.submitted"`) != 1 {
		t.Fatalf("expected exactly one submission event: %s", events)
	}
}

func TestLateCancelDoesNotRewriteTerminalRun(t *testing.T) {
	started := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	finished := started.Add(2 * time.Second)
	for _, test := range []struct {
		status  string
		outcome string
	}{{status: "succeeded", outcome: "completed"}, {status: "failed", outcome: "error"}} {
		t.Run(test.status, func(t *testing.T) {
			run := createCandidateRun(t, candidateWorkflow([]model.ExecutorCandidate{{Executor: "worker"}}, nil, 1))
			run.State.Status = test.status
			run.State.Outcome = test.outcome
			run.State.StartedAt = started.Format(time.RFC3339Nano)
			run.State.FinishedAt = finished.Format(time.RFC3339Nano)
			run.State.DurationMs = 2000
			reason, err := applyCancel(run, model.Control{Kind: ControlCancel, At: finished.Add(time.Nanosecond).Format(time.RFC3339Nano)})
			if err != nil || reason != "run is terminal" {
				t.Fatalf("late cancel result = %q, %v", reason, err)
			}
			if run.State.Status != test.status || run.State.Outcome != test.outcome || run.State.FinishedAt != finished.Format(time.RFC3339Nano) || run.State.DurationMs != 2000 {
				t.Fatalf("late cancel rewrote terminal state: %+v", run.State)
			}
		})
	}
}

func TestCancelQueuedBeforeTerminalCheckpointWinsCoherently(t *testing.T) {
	started := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	finished := started.Add(2 * time.Second)
	run := createCandidateRun(t, candidateWorkflow([]model.ExecutorCandidate{{Executor: "worker"}}, nil, 1))
	run.State.Status = "succeeded"
	run.State.Outcome = "completed"
	run.State.StartedAt = started.Format(time.RFC3339Nano)
	run.State.FinishedAt = finished.Format(time.RFC3339Nano)
	run.State.DurationMs = 2000
	reason, err := applyCancel(run, model.Control{Kind: ControlCancel, At: finished.Add(-time.Nanosecond).Format(time.RFC3339Nano)})
	if err != nil || reason != "" {
		t.Fatalf("pre-terminal cancel result = %q, %v", reason, err)
	}
	if run.State.Status != "cancelled" || run.State.Outcome != "cancelled" || run.State.FinishedAt != finished.Format(time.RFC3339Nano) || run.State.DurationMs != 2000 {
		t.Fatalf("pre-terminal cancel produced incoherent state: %+v", run.State)
	}
}

func TestQueuedLateCancelLeavesTerminalCheckpointUntouched(t *testing.T) {
	started := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	finished := started.Add(2 * time.Second)
	for _, parallel := range []bool{false, true} {
		for _, terminal := range []struct {
			status  string
			outcome string
		}{{status: "succeeded", outcome: "completed"}, {status: "failed", outcome: "error"}} {
			name := terminal.status
			if parallel {
				name += "-parallel"
			}
			t.Run(name, func(t *testing.T) {
				workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "worker"}}, nil, 1)
				if parallel {
					workflow.Nodes[0].Dispatch = "all"
				}
				run := createCandidateRun(t, workflow)
				run.State.Status = terminal.status
				run.State.Outcome = terminal.outcome
				run.State.StartedAt = started.Format(time.RFC3339Nano)
				run.State.FinishedAt = finished.Format(time.RFC3339Nano)
				run.State.DurationMs = 2000
				run.State.Nodes["step"].Status = terminal.status
				if terminal.status == "succeeded" {
					run.State.ActiveNode = ""
					run.State.ActiveNodes = nil
				} else if parallel {
					run.State.ActiveNodes = []string{"step"}
					run.State.ActiveNode = "step"
				}
				if err := store.SaveState(run); err != nil {
					t.Fatal(err)
				}
				statePath := filepath.Join(run.RunDir, "state.json")
				before, err := os.ReadFile(statePath)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.EnqueueControl(run.RunDir, model.Control{Kind: ControlCancel, At: finished.Add(time.Nanosecond).Format(time.RFC3339Nano)}); err != nil {
					t.Fatal(err)
				}
				executors := map[string]driver.Executor{"worker": {Kind: "command", Command: []string{"sh", "-c", "exit 0"}}}
				if err := Execute(run, executors); err != nil {
					t.Fatal(err)
				}
				after, err := os.ReadFile(statePath)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before, after) {
					t.Fatalf("queued late cancel rewrote %s terminal checkpoint\nbefore=%s\nafter=%s", terminal.status, before, after)
				}
			})
		}
	}
}

// Every candidate attempt installs a fresh lease token in the same durable
// checkpoint that exposes it. A delayed callback retained by a failed
// candidate therefore cannot submit as the fallback that replaced it.
func TestLeaseTokenRotatesForEveryCandidateAttempt(t *testing.T) {
	policy := &model.FallbackSpec{When: "failure.kind == 'exit_code'"}
	workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "primary"}, {Executor: "external"}}, policy, 1)
	run := createCandidateRun(t, workflow)
	executors := map[string]driver.Executor{
		"primary": {Kind: "command", Command: []string{"sh", "-c", `printf '%s' "$AGENTFLOW_NODE_TOKEN"; exit 75`}},
	}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if state.Status != "waiting_input" || len(state.AttemptSequence) != 2 {
		t.Fatalf("expected external fallback to wait: run=%s state=%+v", run.State.Status, state)
	}
	primary := state.AttemptSequence[0]
	stdoutArtifact := strings.TrimPrefix(primary.Artifacts["stdoutLog"], "artifacts/")
	data, err := os.ReadFile(filepath.Join(run.RunDir, "artifacts", filepath.FromSlash(stdoutArtifact)))
	if err != nil {
		t.Fatal(err)
	}
	leaked := string(data)
	if leaked == "" || leaked == state.LeaseToken {
		t.Fatalf("failed candidate and fallback shared a lease token: leaked=%q current=%q", leaked, state.LeaseToken)
	}
	output := map[string]any{"ok": true}
	if reason, err := applySubmission(run, model.Control{Kind: ControlSubmit, NodeID: "step", Token: leaked, Output: output}); err != nil || reason == "" {
		t.Fatalf("stale candidate token was accepted: reason=%q err=%v", reason, err)
	}
	if state.Status != "waiting_input" {
		t.Fatalf("stale submission changed the waiting node: %+v", state)
	}
	if reason, err := applySubmission(run, model.Control{Kind: ControlSubmit, NodeID: "step", Token: state.LeaseToken, Output: output}); err != nil || reason != "" {
		t.Fatalf("current token was rejected: reason=%q err=%v", reason, err)
	}
}

// node.Attempts counts the current visit, while the completed attempt's
// sequence is durable across visits. Submission events must report the latter
// so they correlate with state, metrics and artifacts.
func TestSubmissionEventUsesDurableAttemptCoordinates(t *testing.T) {
	workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "worker"}}, nil, 1)
	run := createCandidateRun(t, workflow)
	if err := Execute(run, map[string]driver.Executor{"worker": {Kind: "command", Command: []string{"sh", "-c", "exit 75"}}}); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "failed" {
		t.Fatalf("expected a failed first visit: %+v", run.State)
	}
	if reason, err := applyRetry(run, model.Control{Kind: ControlRetry}); err != nil || reason != "" {
		t.Fatalf("retry failed: reason=%q err=%v", reason, err)
	}
	if err := Execute(run, map[string]driver.Executor{"worker": {Kind: "external"}}); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if state.Status != "waiting_input" || state.Attempts != 1 || len(state.AttemptSequence) != 1 || state.AttemptSequence[0].Sequence != 2 {
		t.Fatalf("retried visit did not expose the durable sequence: %+v", state)
	}
	if reason, err := applySubmission(run, model.Control{Kind: ControlSubmit, NodeID: "step", Token: state.LeaseToken, Output: map[string]any{"ok": true}}); err != nil || reason != "" {
		t.Fatalf("submission failed: reason=%q err=%v", reason, err)
	}
	events, err := readEvents(run.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event["type"] != "node.submitted" {
			continue
		}
		found = true
		if event["attempt"] != float64(2) || event["candidate"] != float64(0) || event["candidateAttempt"] != float64(1) {
			t.Fatalf("submission event does not name the completed attempt: %+v", event)
		}
	}
	if !found {
		t.Fatal("no submission event was recorded")
	}
}

// Recovery mutations must be durable before their events are emitted. If the
// checkpoint cannot be saved, durable state still says running and no event
// may claim the attempt was archived or resumed.
func TestFailedRecoverySaveEmitsNoRecoveryEvents(t *testing.T) {
	workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "worker"}}, nil, 1)
	run := createCandidateRun(t, workflow)
	now := time.Now().UTC()
	started := now.Add(-time.Second).Format(time.RFC3339Nano)
	run.State.Status = "running"
	run.State.ActiveNode = "step"
	run.State.Nodes["step"] = &model.NodeState{
		Status: "running", SelectedExecutor: "worker", StartedAt: started,
		Attempts: 1, Input: map[string]any{}, Artifacts: map[string]string{},
		AttemptSequence: []model.AttemptEvidence{{
			Sequence: 1, Candidate: 0, CandidateAttempt: 1, Executor: "worker", Status: "running",
			StartedAt: started, Artifacts: map[string]string{},
		}},
	}
	if err := store.SaveState(run); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(run.RunDir, "state.json.tmp")
	if err := os.Mkdir(temporary, 0o755); err != nil {
		t.Fatal(err)
	}
	executors := map[string]driver.Executor{"worker": {Kind: "command", Command: []string{"sh", "-c", `printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}}}
	if err := Execute(run, executors); err == nil {
		t.Fatalf("execute succeeded despite an unwritable recovery checkpoint")
	}
	if err := os.RemoveAll(temporary); err != nil {
		t.Fatal(err)
	}
	events, err := readEvents(run.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event["type"] == "node.orphaned" || event["type"] == "node.continuation_resumed" || strings.HasPrefix(event["type"].(string), "node.recovered_") {
			t.Fatalf("recovery event was emitted before its durable checkpoint: %+v", event)
		}
	}
	durable := &model.Run{RunID: run.RunID, RunDir: run.RunDir, Workflow: run.Workflow}
	if err := store.ReloadState(durable); err != nil {
		t.Fatal(err)
	}
	orphan := durable.State.Nodes["step"]
	if orphan.Status != "running" || len(orphan.AttemptSequence) != 1 || orphan.AttemptSequence[0].Status != "running" {
		t.Fatalf("failed checkpoint changed durable recovery state: %+v", orphan)
	}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	events, err = readEvents(run.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	orphans := 0
	for _, event := range events {
		if event["type"] == "node.orphaned" {
			orphans++
		}
	}
	if orphans != 1 {
		t.Fatalf("expected exactly one recovery event after a successful checkpoint: %+v", events)
	}
}

func readEvents(runDir string) ([]map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(runDir, "events.jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	events := []map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(line, &event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}
