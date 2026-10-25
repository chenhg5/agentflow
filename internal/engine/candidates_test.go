package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestPrimaryCandidateSuccessSkipsFallback(t *testing.T) {
	workspace := t.TempDir()
	workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "primary"}, {Executor: "fallback"}}, nil, 1)
	run := createCandidateRun(t, workflow)
	executors := map[string]driver.Executor{
		"primary":  {Kind: "command", Workspace: workspace, Command: []string{"sh", "-c", `printf '{"source":"primary"}' > "$AGENTFLOW_RESULT_FILE"`}},
		"fallback": {Kind: "command", Workspace: workspace, Command: []string{"sh", "-c", `touch fallback.ran; printf '{"source":"fallback"}' > "$AGENTFLOW_RESULT_FILE"`}},
	}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if run.State.Status != "succeeded" || state.SelectedExecutor != "primary" || state.Attempts != 1 {
		t.Fatalf("unexpected primary selection: %+v", state)
	}
	if _, err := os.Stat(filepath.Join(workspace, "fallback.ran")); !os.IsNotExist(err) {
		t.Fatalf("fallback ran after primary success: %v", err)
	}
}

func TestMissingExecutableRetriesThenFallsBackWithEvidence(t *testing.T) {
	workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "missing"}, {Executor: "fallback"}}, nil, 2)
	workflow.Nodes = append(workflow.Nodes, model.Node{ID: "receipt", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "receipt"}}}})
	workflow.Edges = []model.Transition{{From: "step", To: "receipt"}, {From: "receipt", To: "$end"}}
	run := createCandidateRun(t, workflow)
	workspace := t.TempDir()
	executors := map[string]driver.Executor{
		"missing":  {Kind: "command", Command: []string{"agentflow-command-that-does-not-exist"}},
		"fallback": {Kind: "command", Command: []string{"sh", "-c", `printf '{"source":"fallback"}' > "$AGENTFLOW_RESULT_FILE"`}},
		"receipt":  {Kind: "command", Workspace: workspace, Command: []string{"sh", "-c", `printf x >> receipt.count; cat "$AGENTFLOW_INPUT_FILE" > "$AGENTFLOW_RESULT_FILE"`}},
	}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if run.State.Status != "succeeded" || state.SelectedExecutor != "fallback" || state.Attempts != 3 || len(state.AttemptSequence) != 3 {
		t.Fatalf("unexpected fallback state: %+v", state)
	}
	receipt := run.State.Nodes["receipt"]
	var receiptResult map[string]any
	var resultOK bool
	if receipt != nil {
		receiptResult, resultOK = receipt.Result.(map[string]any)
	}
	if receipt == nil || !resultOK || receiptResult["source"] != "fallback" {
		t.Fatalf("downstream did not receive fallback output: %+v", receipt)
	}
	if data, err := os.ReadFile(filepath.Join(workspace, "receipt.count")); err != nil || string(data) != "x" {
		t.Fatalf("downstream did not run exactly once: %q, %v", data, err)
	}
	for index := 0; index < 2; index++ {
		attempt := state.AttemptSequence[index]
		if attempt.Failure == nil || attempt.Failure.Kind != "start_failed" || attempt.Failure.Started {
			t.Fatalf("attempt %d was not a start failure: %+v", index, attempt)
		}
		if attempt.Artifacts["input"] == state.AttemptSequence[2].Artifacts["input"] {
			t.Fatal("candidate attempts reused input artifacts")
		}
	}
	events, err := store.ReadEvents(run.RunDir)
	if err != nil || strings.Count(string(events), `"type":"node.fallback_selected"`) != 1 {
		t.Fatalf("fallback event evidence = %s, %v", events, err)
	}
}

func TestMissingWorkspaceFallsBack(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-workspace")
	run := createCandidateRun(t, candidateWorkflow([]model.ExecutorCandidate{{Executor: "primary"}, {Executor: "fallback"}}, nil, 1))
	executors := map[string]driver.Executor{
		"primary":  {Kind: "command", Workspace: missing, Command: []string{"sh", "-c", "exit 0"}},
		"fallback": {Kind: "command", Command: []string{"sh", "-c", `printf '{"source":"fallback"}' > "$AGENTFLOW_RESULT_FILE"`}},
	}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if run.State.Status != "succeeded" || state.SelectedExecutor != "fallback" || state.AttemptSequence[0].Failure == nil || state.AttemptSequence[0].Failure.Kind != "start_failed" {
		t.Fatalf("missing workspace did not select fallback: %+v", state)
	}
}

func TestFailedCandidateSessionDoesNotLeakIntoFallback(t *testing.T) {
	candidates := []model.ExecutorCandidate{
		{Executor: "missing", Session: model.SessionSpec{Mode: "fixed", ID: "wrong-provider-session"}},
		{Executor: "fallback", Session: model.SessionSpec{Mode: "new"}},
	}
	// A missing resumeCommand is a configuration error, so reaching the
	// fallback requires an explicit policy; the default only covers startups.
	policy := &model.FallbackSpec{When: "failure.kind == 'configuration_failed'"}
	run := createCandidateRun(t, candidateWorkflow(candidates, policy, 1))
	executors := map[string]driver.Executor{
		"missing":  {Kind: "command", Command: []string{"agentflow-command-that-does-not-exist"}},
		"fallback": {Kind: "command", Command: []string{"sh", "-c", `test -z "$AGENTFLOW_SESSION_ID"; printf '{"source":"fallback"}' > "$AGENTFLOW_RESULT_FILE"`}},
	}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if run.State.Status != "succeeded" || state.SelectedExecutor != "fallback" || state.SessionID != "" || state.Attempts != 2 {
		t.Fatalf("candidate session leaked into fallback: %+v", state)
	}
}

func TestStartedExitNeedsExplicitFallbackPolicy(t *testing.T) {
	executors := map[string]driver.Executor{
		"primary":  {Kind: "command", Command: []string{"sh", "-c", "exit 75"}},
		"fallback": {Kind: "command", Command: []string{"sh", "-c", `printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}},
	}
	withoutPolicy := createCandidateRun(t, candidateWorkflow([]model.ExecutorCandidate{{Executor: "primary"}, {Executor: "fallback"}}, nil, 1))
	if err := Execute(withoutPolicy, executors); err != nil {
		t.Fatal(err)
	}
	failed := withoutPolicy.State.Nodes["step"]
	if withoutPolicy.State.Status != "failed" || failed.Attempts != 1 || failed.Failure == nil || failed.Failure.Kind != "exit_code" || !failed.Failure.Started || failed.Failure.Code == nil || *failed.Failure.Code != 75 {
		t.Fatalf("started failure incorrectly fell back: %+v", failed)
	}
	policy := &model.FallbackSpec{When: "failure.kind == 'exit_code' && failure.code == 75"}
	withPolicy := createCandidateRun(t, candidateWorkflow([]model.ExecutorCandidate{{Executor: "primary"}, {Executor: "fallback"}}, policy, 1))
	if err := Execute(withPolicy, executors); err != nil {
		t.Fatal(err)
	}
	if withPolicy.State.Status != "succeeded" || withPolicy.State.Nodes["step"].SelectedExecutor != "fallback" {
		t.Fatalf("explicit exit policy did not fall back: %+v", withPolicy.State.Nodes["step"])
	}
}

func TestTimeoutAndInvalidOutputDoNotFallbackByDefault(t *testing.T) {
	fallback := driver.Executor{Kind: "command", Command: []string{"sh", "-c", `printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}}
	for name, primary := range map[string]driver.Executor{
		"timeout": {Kind: "command", Command: []string{"sh", "-c", "sleep 1"}},
		"invalid": {Kind: "command", Command: []string{"sh", "-c", `printf 'not-json' > "$AGENTFLOW_RESULT_FILE"`}},
	} {
		t.Run(name, func(t *testing.T) {
			workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "primary"}, {Executor: "fallback"}}, nil, 1)
			if name == "timeout" {
				workflow.Nodes[0].TimeoutMs = 20
			}
			run := createCandidateRun(t, workflow)
			if err := Execute(run, map[string]driver.Executor{"primary": primary, "fallback": fallback}); err != nil {
				t.Fatal(err)
			}
			state := run.State.Nodes["step"]
			if run.State.Status != "failed" || state.Attempts != 1 || state.SelectedExecutor != "primary" {
				t.Fatalf("failure incorrectly switched candidates: %+v", state)
			}
		})
	}
}

func TestCandidateConfigsAreIndependentAndStaleResultsAreRejected(t *testing.T) {
	policy := &model.FallbackSpec{When: "failure.kind == 'exit_code' && failure.code == 75"}
	candidates := []model.ExecutorCandidate{
		{Executor: "primary", Config: map[string]any{"label": "primary"}},
		{Executor: "fallback", Config: map[string]any{"label": "fallback"}},
	}
	executors := map[string]driver.Executor{
		"primary":  {Kind: "command", Env: map[string]string{"LABEL": "{{ config.label }}"}, Command: []string{"sh", "-c", `test "$LABEL" = primary; printf '{"stale":true}' > "$AGENTFLOW_RESULT_FILE"; exit 75`}},
		"fallback": {Kind: "command", Env: map[string]string{"LABEL": "{{ config.label }}"}, Command: []string{"sh", "-c", `test "$LABEL" = fallback`}},
	}
	run := createCandidateRun(t, candidateWorkflow(candidates, policy, 1))
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if run.State.Status != "failed" || state.SelectedExecutor != "fallback" || state.Failure == nil || state.Failure.Kind != "result_missing" || state.Attempts != 2 {
		t.Fatalf("stale primary result was accepted or configs leaked: %+v", state)
	}
	if state.AttemptSequence[0].Artifacts["result"] == state.AttemptSequence[1].Artifacts["result"] {
		t.Fatal("result paths were reused across candidates")
	}
}

func TestAllCandidatesFailWithFinalAttemptEvidence(t *testing.T) {
	run := createCandidateRun(t, candidateWorkflow([]model.ExecutorCandidate{{Executor: "one"}, {Executor: "two"}}, nil, 1))
	executors := map[string]driver.Executor{
		"one": {Kind: "command", Command: []string{"missing-one-agentflow"}},
		"two": {Kind: "command", Command: []string{"missing-two-agentflow"}},
	}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if run.State.Status != "failed" || state.Attempts != 2 || state.SelectedExecutor != "two" || state.Failure == nil || state.Failure.Kind != "start_failed" {
		t.Fatalf("unexpected exhausted state: %+v", state)
	}
}

func candidateWorkflow(candidates []model.ExecutorCandidate, fallback *model.FallbackSpec, attempts int) model.Workflow {
	return model.Workflow{
		Version: "3", Name: "candidates", Entry: "step",
		Nodes: []model.Node{{ID: "step", Execution: model.Execution{Candidates: candidates, Fallback: fallback}, Retry: model.Retry{MaxAttempts: attempts}}},
		Edges: []model.Transition{{From: "step", To: "$end"}},
	}
}

func createCandidateRun(t *testing.T, workflow model.Workflow) *model.Run {
	t.Helper()
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// The external adapter waits for an independent application; it cannot create,
// resume, or fork a provider session. A non-new policy must fail explicitly
// instead of waiting and later reporting an empty session.
func TestExternalCandidateRejectsUnsupportedSessionPolicies(t *testing.T) {
	for _, session := range []model.SessionSpec{{Mode: "fixed", ID: "known-session"}, {Mode: "fixed"}} {
		workflow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "external", Session: session}}, nil, 1)
		run := createCandidateRun(t, workflow)
		if err := Execute(run, map[string]driver.Executor{"external": {Kind: "external"}}); err != nil {
			t.Fatal(err)
		}
		state := run.State.Nodes["step"]
		if run.State.Status != "failed" || state == nil || state.Failure == nil {
			t.Fatalf("session mode %s did not fail: %+v", session.Mode, run.State)
		}
		if state.Failure.Kind != "configuration_failed" {
			t.Fatalf("session mode %s produced %q, want configuration_failed: %+v", session.Mode, state.Failure.Kind, state.Failure)
		}
		if state.Status != "failed" || state.SessionID != "" {
			t.Fatalf("external wait accepted session mode %s: %+v", session.Mode, state)
		}
		if len(state.AttemptSequence) != 1 || state.AttemptSequence[0].Status != "failed" {
			t.Fatalf("session mode %s left wait evidence: %+v", session.Mode, state.AttemptSequence)
		}
	}
}
