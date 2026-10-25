package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/metrics"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestManualRetryArchivesEvidenceAndResumesFixedSession(t *testing.T) {
	root := t.TempDir()
	workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "worker", Session: model.SessionSpec{Mode: "fixed"}}}, nil, 1)
	run, err := store.CreateRun(root, workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	executor := driver.Executor{
		Kind:          "command",
		Command:       []string{"sh", "-c", `printf 'retry-session' > "$AGENTFLOW_SESSION_FILE"; printf '{"partial":true}' > "$AGENTFLOW_RESULT_FILE"; exit 75`},
		ResumeCommand: []string{"sh", "-c", `test "$AGENTFLOW_SESSION_ID" = retry-session; printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`},
	}
	if err := Execute(run, map[string]driver.Executor{"worker": executor}); err != nil {
		t.Fatal(err)
	}
	failed := cloneState(run.State.Nodes["step"])
	if run.State.Status != "failed" || failed.SessionID != "retry-session" {
		t.Fatalf("initial failure did not retain its session: %+v", failed)
	}
	oldInput := strings.TrimPrefix(failed.AttemptSequence[0].Artifacts["input"], "artifacts/")
	oldData, err := os.ReadFile(filepath.Join(run.RunDir, "artifacts", oldInput))
	if err != nil {
		t.Fatal(err)
	}
	if reason, err := applyRetry(run, model.Control{Kind: ControlRetry}); reason != "" || err != nil {
		t.Fatalf("retry failed: reason=%q err=%v", reason, err)
	}
	if run.State.Visits["step"] != 2 || len(run.State.History) != 1 || run.State.History[0].State.SessionID != "retry-session" {
		t.Fatalf("retry did not archive visit: visits=%v history=%+v", run.State.Visits, run.State.History)
	}
	if err := store.SaveState(run); err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, map[string]driver.Executor{"worker": executor}); err != nil {
		t.Fatal(err)
	}
	current := run.State.Nodes["step"]
	if run.State.Status != "succeeded" || current.SessionID != "retry-session" || current.AttemptSequence[0].Sequence != 2 {
		t.Fatalf("fixed retry did not resume archived session: %+v", current)
	}
	if !strings.Contains(current.AttemptSequence[0].Artifacts["input"], "step.2.candidate-1.attempt-1") {
		t.Fatalf("retry reused the old artifact namespace: %+v", current.AttemptSequence[0].Artifacts)
	}
	if after, err := os.ReadFile(filepath.Join(run.RunDir, "artifacts", oldInput)); err != nil || string(after) != string(oldData) {
		t.Fatalf("retry changed archived evidence: %q, %v", after, err)
	}
	snapshot := metrics.Snapshot(run, time.Now().UTC())
	if snapshot.Totals.Attempts != 2 || snapshot.Totals.OutputBytes != failed.OutputBytes+current.OutputBytes || len(snapshot.Nodes) != 2 {
		t.Fatalf("metrics dropped an archived retry visit: %+v", snapshot)
	}
}

func TestExternalFallbackWaitStartsAtExternalAttemptAndKeepsOutputBytes(t *testing.T) {
	policy := &model.FallbackSpec{When: "failure.kind == 'exit_code' && failure.code == 75"}
	workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "primary"}, {Executor: "external"}}, policy, 1)
	root := t.TempDir()
	run, err := store.CreateRun(root, workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	executors := map[string]driver.Executor{
		"primary": {Kind: "command", Command: []string{"sh", "-c", `sleep 0.12; printf '{"partial":true}' > "$AGENTFLOW_RESULT_FILE"; exit 75`}},
	}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	run, err = store.LoadRun(root, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := metrics.Snapshot(run, time.Now().UTC())
	state := run.State.Nodes["step"]
	if run.State.Status != "waiting_input" || state.WaitingStartedAt == "" || snapshot.ActiveMs < 90 || snapshot.WaitingMs >= snapshot.ActiveMs {
		t.Fatalf("fallback work was counted as external wait: snapshot=%+v state=%+v", snapshot, state)
	}
	primaryBytes := state.OutputBytes
	if primaryBytes == 0 {
		t.Fatal("failed primary result bytes were not recorded")
	}
	output := map[string]any{"ok": true}
	if reason, err := applySubmission(run, model.Control{Kind: ControlSubmit, NodeID: "step", Output: output}); err != nil || reason != "" {
		t.Fatalf("submission failed: reason=%q err=%v", reason, err)
	}
	if state.OutputBytes <= primaryBytes {
		t.Fatalf("submission replaced cumulative output bytes: before=%d after=%d", primaryBytes, state.OutputBytes)
	}
}

func TestPersistedSessionProtocolRejectsRegistryDrift(t *testing.T) {
	workspace := t.TempDir()
	workflow := model.Workflow{Version: "3", Name: "protocol-drift", Entry: "source", Nodes: []model.Node{
		{ID: "source", Execution: testExecution("source-alias", nil, model.SessionSpec{Mode: "new"})},
		{ID: "gate", Execution: testExecution("external", nil, model.SessionSpec{})},
		{ID: "target", Execution: testExecution("target", nil, model.SessionSpec{Mode: "inherit", From: "source"})},
	}, Edges: []model.Transition{{From: "source", To: "gate"}, {From: "gate", To: "target"}, {From: "target", To: "$end"}}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	firstRegistry := map[string]driver.Executor{
		"source-alias": {Kind: "codex", Command: []string{"sh", "-c", `printf 'codex-session' > "$AGENTFLOW_SESSION_FILE"; printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}},
		"target":       {Kind: "claude-code"},
	}
	if err := Execute(run, firstRegistry); err != nil {
		t.Fatal(err)
	}
	if reason, err := applySubmission(run, model.Control{Kind: ControlSubmit, NodeID: "gate", Output: map[string]any{"ok": true}}); err != nil || reason != "" {
		t.Fatalf("gate submission failed: reason=%q err=%v", reason, err)
	}
	if err := store.SaveState(run); err != nil {
		t.Fatal(err)
	}
	changedRegistry := map[string]driver.Executor{
		"source-alias": {Kind: "claude-code"},
		"target": {
			Kind:          "claude-code",
			ResumeCommand: []string{"sh", "-c", `touch target-ran; printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`},
			Workspace:     workspace,
		},
	}
	if err := Execute(run, changedRegistry); err != nil {
		t.Fatal(err)
	}
	target := run.State.Nodes["target"]
	if run.State.Status != "failed" || target.Failure == nil || target.Failure.Kind != "session_incompatible" || !strings.Contains(target.Error, "native:codex") {
		t.Fatalf("mutable registry changed persisted compatibility: %+v", target)
	}
	if _, err := os.Stat(filepath.Join(workspace, "target-ran")); !os.IsNotExist(err) {
		t.Fatalf("incompatible target was launched: %v", err)
	}
}

func TestRecoveryDoesNotRerunCompletedFailedAttempt(t *testing.T) {
	workspace := t.TempDir()
	workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "worker"}}, nil, 1)
	run := createCandidateRun(t, workflow)
	now := time.Now().UTC()
	started := now.Add(-time.Second).Format(time.RFC3339Nano)
	finished := now.Add(-500 * time.Millisecond).Format(time.RFC3339Nano)
	code := 75
	failure := &model.ExecutionFailure{Kind: "exit_code", Started: true, Code: &code, Message: "command exited with code 75", Executor: "worker", Attempt: 1}
	run.State.Status = "running"
	run.State.Nodes["step"] = &model.NodeState{
		Status: "running", SelectedExecutor: "worker", Workspace: workspace, StartedAt: started,
		Attempts: 1, Failure: failure, Input: map[string]any{}, Artifacts: map[string]string{},
		AttemptSequence: []model.AttemptEvidence{{Sequence: 1, Candidate: 0, CandidateAttempt: 1, Executor: "worker", Status: "failed", StartedAt: started, FinishedAt: finished, Failure: failure, Artifacts: map[string]string{}}},
	}
	if err := store.SaveState(run); err != nil {
		t.Fatal(err)
	}
	executor := driver.Executor{Kind: "command", Workspace: workspace, Command: []string{"sh", "-c", `touch reran; printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}}
	if err := Execute(run, map[string]driver.Executor{"worker": executor}); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if run.State.Status != "failed" || state.Status != "failed" || state.Failure == nil || state.Failure.Kind != "exit_code" || state.AttemptSequence[0].Failure.Kind != "exit_code" {
		t.Fatalf("completed failure was replaced during recovery: run=%s state=%+v", run.State.Status, state)
	}
	if run.State.Visits["step"] != 1 || len(run.State.History) != 0 {
		t.Fatalf("completed failure was archived for automatic rerun: visits=%v history=%+v", run.State.Visits, run.State.History)
	}
	if _, err := os.Stat(filepath.Join(workspace, "reran")); !os.IsNotExist(err) {
		t.Fatalf("completed failed command ran again: %v", err)
	}
}

func TestRecoveryResumesRecordedFallbackCheckpoint(t *testing.T) {
	workspace := t.TempDir()
	policy := &model.FallbackSpec{When: "failure.kind == 'exit_code'"}
	workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "primary"}, {Executor: "secondary"}}, policy, 1)
	run := createCandidateRun(t, workflow)
	now := time.Now().UTC()
	code := 75
	failure := &model.ExecutionFailure{Kind: "exit_code", Started: true, Code: &code, Message: "command exited with code 75", Executor: "primary", Attempt: 1}
	run.State.Status = "running"
	run.State.ActiveNode = "step"
	run.State.Nodes["step"] = &model.NodeState{
		Status: "running", SelectedExecutor: "primary", StartedAt: now.Add(-time.Second).Format(time.RFC3339Nano),
		Attempts: 1, Failure: failure, Input: map[string]any{}, Artifacts: map[string]string{},
		AttemptSequence: []model.AttemptEvidence{{Sequence: 1, Candidate: 0, CandidateAttempt: 1, Executor: "primary", Status: "failed", StartedAt: now.Add(-time.Second).Format(time.RFC3339Nano), FinishedAt: now.Add(-500 * time.Millisecond).Format(time.RFC3339Nano), Failure: failure, Artifacts: map[string]string{}}},
		Continuation:    &model.NodeContinuation{Kind: continuationFallback, Candidate: 1, CandidateAttempt: 1, Failure: failure, At: now.Format(time.RFC3339Nano)},
	}
	if err := store.SaveState(run); err != nil {
		t.Fatal(err)
	}
	executors := map[string]driver.Executor{
		"primary":   {Kind: "command", Workspace: workspace, Command: []string{"sh", "-c", `touch primary-reran`}},
		"secondary": {Kind: "command", Workspace: workspace, Command: []string{"sh", "-c", `printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}},
	}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if run.State.Status != "succeeded" || state.Status != "succeeded" || len(state.AttemptSequence) != 2 {
		t.Fatalf("recorded fallback was not resumed: run=%s state=%+v", run.State.Status, state)
	}
	if state.AttemptSequence[1].Candidate != 1 || state.AttemptSequence[1].Sequence != 2 || state.Continuation != nil {
		t.Fatalf("fallback checkpoint was not consumed: %+v", state)
	}
	if _, err := os.Stat(filepath.Join(workspace, "primary-reran")); !os.IsNotExist(err) {
		t.Fatalf("completed primary attempt replayed: %v", err)
	}
}

func TestExecuteRejectsUnregisteredCandidateBeforeAttempt(t *testing.T) {
	policy := &model.FallbackSpec{When: "failure.kind == 'configuration_failed'"}
	workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "typo"}, {Executor: "process", Config: map[string]any{"command": []string{"sh", "-c", `printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}}}}, policy, 1)
	run := createCandidateRun(t, workflow)
	err := Execute(run, nil)
	if err == nil || !strings.Contains(err.Error(), `unregistered executor "typo"`) {
		t.Fatalf("unknown executor was fallback-visible: %v", err)
	}
	state := run.State.Nodes["step"]
	if state.Status != "pending" || len(state.AttemptSequence) != 0 {
		t.Fatalf("unknown executor created attempt evidence: %+v", state)
	}
}

func TestRunningAttemptIsObservableAndOrphanGetsFreshEvidence(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "worker"}}, nil, 1)
	run, err := store.CreateRun(root, workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	executor := driver.Executor{Kind: "command", Workspace: workspace, Command: []string{"sh", "-c", `touch started; while test ! -f release; do sleep 0.01; done; printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}}
	done := make(chan error, 1)
	go func() { done <- Execute(run, map[string]driver.Executor{"worker": executor}) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(workspace, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("executor did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	loaded, err := store.LoadRun(root, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	live := loaded.State.Nodes["step"]
	if live.Status != "running" || live.SelectedExecutor != "worker" || live.Workspace != workspace || len(live.AttemptSequence) != 1 || live.AttemptSequence[0].Status != "running" || live.AttemptSequence[0].SessionProtocol != "command:worker" {
		t.Fatalf("running attempt metadata was not durable: %+v", live)
	}
	if err := os.WriteFile(filepath.Join(workspace, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	orphan := cloneState(run.State.Nodes["step"])
	orphan.Status = "running"
	orphan.Result = nil
	orphan.Error = ""
	orphan.FinishedAt = ""
	orphan.AttemptSequence[0].Status = "running"
	orphan.AttemptSequence[0].FinishedAt = ""
	orphan.AttemptSequence[0].Failure = nil
	orphan.AttemptSequence[0].Artifacts["stdoutLog"] = "artifacts/logs/old-interrupted.log"
	orphan.Artifacts = copyArtifacts(orphan.AttemptSequence[0].Artifacts)
	oldEvidence := []byte("old evidence")
	if err := store.WriteArtifact(run.RunDir, "logs/old-interrupted.log", oldEvidence); err != nil {
		t.Fatal(err)
	}
	run.State.Nodes["step"] = &orphan
	run.State.ActiveNode = "step"
	run.State.ActiveNodes = []string{"step"}
	run.State.Status = "running"
	run.State.History = nil
	if err := store.SaveState(run); err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, map[string]driver.Executor{"worker": {Kind: "command", Workspace: workspace, Command: []string{"sh", "-c", `printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}}}); err != nil {
		t.Fatal(err)
	}
	if run.State.Visits["step"] != 2 || len(run.State.History) != 2 {
		t.Fatalf("orphan was not archived as a visit: visits=%v history=%+v", run.State.Visits, run.State.History)
	}
	archivedAttempt := run.State.History[0].State.AttemptSequence[0]
	currentAttempt := run.State.Nodes["step"].AttemptSequence[0]
	if archivedAttempt.Status != "failed" || archivedAttempt.Failure == nil || archivedAttempt.Failure.Kind != "interrupted" || currentAttempt.Sequence != 2 || !strings.Contains(currentAttempt.Artifacts["input"], "step.2.candidate-1.attempt-1") {
		t.Fatalf("orphan recovery did not preserve and advance evidence: archived=%+v current=%+v", archivedAttempt, currentAttempt)
	}
	if data, err := os.ReadFile(filepath.Join(run.RunDir, "artifacts", "logs/old-interrupted.log")); err != nil || string(data) != string(oldEvidence) {
		t.Fatalf("orphan recovery overwrote evidence: %q, %v", data, err)
	}
}

func TestOrphanRecoveryResumesOnlyTheInterruptedAttempt(t *testing.T) {
	workspace := t.TempDir()
	policy := &model.FallbackSpec{When: "failure.kind == 'exit_code'"}
	workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "first"}, {Executor: "second"}, {Executor: "third"}}, policy, 1)
	run := createCandidateRun(t, workflow)
	now := time.Now().UTC()
	code := 75
	firstFailure := &model.ExecutionFailure{Kind: "exit_code", Started: true, Code: &code, Message: "first failed", Executor: "first", Attempt: 1}
	run.State.Status = "running"
	run.State.ActiveNode = "step"
	run.State.Nodes["step"] = &model.NodeState{
		Status: "running", SelectedExecutor: "second", Workspace: workspace, StartedAt: now.Add(-time.Second).Format(time.RFC3339Nano),
		Attempts: 2, Input: map[string]any{}, Artifacts: map[string]string{},
		AttemptSequence: []model.AttemptEvidence{
			{Sequence: 1, Candidate: 0, CandidateAttempt: 1, Executor: "first", Status: "failed", StartedAt: now.Add(-time.Second).Format(time.RFC3339Nano), FinishedAt: now.Add(-800 * time.Millisecond).Format(time.RFC3339Nano), Failure: firstFailure, Artifacts: map[string]string{}},
			{Sequence: 2, Candidate: 1, CandidateAttempt: 1, Executor: "second", Status: "running", StartedAt: now.Add(-700 * time.Millisecond).Format(time.RFC3339Nano), Workspace: workspace, Artifacts: map[string]string{}},
		},
	}
	if err := store.SaveState(run); err != nil {
		t.Fatal(err)
	}
	executors := map[string]driver.Executor{
		"first":  {Kind: "command", Workspace: workspace, Command: []string{"sh", "-c", `touch first-reran`}},
		"second": {Kind: "command", Workspace: workspace, Command: []string{"sh", "-c", `touch second-ran; printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}},
		"third":  {Kind: "command", Workspace: workspace, Command: []string{"sh", "-c", `touch third-reran`}},
	}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if run.State.Status != "succeeded" || state.Status != "succeeded" || len(state.AttemptSequence) != 1 {
		t.Fatalf("interrupted attempt was not resumed: run=%s state=%+v", run.State.Status, state)
	}
	resumed := state.AttemptSequence[0]
	if resumed.Candidate != 1 || resumed.CandidateAttempt != 1 || resumed.Sequence != 3 {
		t.Fatalf("resume restarted earlier candidates instead of the interrupted position: %+v", resumed)
	}
	if _, err := os.Stat(filepath.Join(workspace, "second-ran")); err != nil {
		t.Fatalf("interrupted candidate was not replayed: %v", err)
	}
	for _, marker := range []string{"first-reran", "third-reran"} {
		if _, err := os.Stat(filepath.Join(workspace, marker)); !os.IsNotExist(err) {
			t.Fatalf("acknowledged or later candidate ran during resume: %s (%v)", marker, err)
		}
	}
}

func TestMissingResumeCommandIsConfigurationFailure(t *testing.T) {
	candidates := []model.ExecutorCandidate{
		{Executor: "worker", Session: model.SessionSpec{Mode: "fixed", ID: "fixed-session"}},
		{Executor: "fallback"},
	}
	executors := func(worker driver.Executor) map[string]driver.Executor {
		return map[string]driver.Executor{
			"worker":   worker,
			"fallback": {Kind: "command", Command: []string{"sh", "-c", `printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}},
		}
	}
	// An unavailable base command is never started for a fixed-session visit,
	// so it cannot turn the missing resume command into a startup failure.
	for name, worker := range map[string]driver.Executor{
		"unavailable base": {Kind: "command", Command: []string{"agentflow-command-that-does-not-exist"}},
		"available base":   {Kind: "command", Command: []string{"sh", "-c", "exit 0"}},
	} {
		run := createCandidateRun(t, candidateWorkflow(candidates, nil, 1))
		if err := Execute(run, executors(worker)); err != nil {
			t.Fatal(err)
		}
		state := run.State.Nodes["step"]
		if run.State.Status != "failed" || len(state.AttemptSequence) != 1 || state.AttemptSequence[0].Failure == nil || state.AttemptSequence[0].Failure.Kind != "configuration_failed" {
			t.Fatalf("%s: missing resumeCommand did not stay a configuration failure: run=%s state=%+v", name, run.State.Status, state)
		}
	}
	// A configured but unavailable resume executable is actually launched and
	// remains a startup failure, so the default fallback may select it.
	run := createCandidateRun(t, candidateWorkflow(candidates, nil, 1))
	if err := Execute(run, executors(driver.Executor{Kind: "command", Command: []string{"sh", "-c", "exit 0"}, ResumeCommand: []string{"agentflow-command-that-does-not-exist"}})); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if run.State.Status != "succeeded" || len(state.AttemptSequence) != 2 || state.AttemptSequence[0].Failure == nil || state.AttemptSequence[0].Failure.Kind != "start_failed" {
		t.Fatalf("unavailable resume command did not stay a startup failure: run=%s state=%+v", run.State.Status, state)
	}
}

func TestFallbackExpressionErrorKeepsExecutorAttemptEvidence(t *testing.T) {
	policy := &model.FallbackSpec{When: "failure.code > 0"}
	workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "worker"}, {Executor: "fallback"}}, policy, 1)
	run := createCandidateRun(t, workflow)
	executors := map[string]driver.Executor{
		"worker":   {Kind: "command", Command: []string{"agentflow-command-that-does-not-exist"}},
		"fallback": {Kind: "command", Command: []string{"sh", "-c", `printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}},
	}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if run.State.Status != "failed" || len(state.AttemptSequence) != 1 {
		t.Fatalf("expression error did not fail terminally: run=%s state=%+v", run.State.Status, state)
	}
	attempt := state.AttemptSequence[0]
	if attempt.Failure == nil || attempt.Failure.Kind != "start_failed" || attempt.Failure.Started {
		t.Fatalf("attempt evidence was rewritten by the expression error: %+v", attempt)
	}
	if state.Failure == nil || state.Failure.Kind != "fallback_expression" {
		t.Fatalf("node did not record the policy evaluation failure: %+v", state.Failure)
	}
	events, err := store.ReadEvents(run.RunDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(events), `"type":"node.attempt.failed"`) || !strings.Contains(string(events), `"kind":"start_failed"`) {
		t.Fatalf("attempt failure event lost the executor classification: %s", events)
	}
}
