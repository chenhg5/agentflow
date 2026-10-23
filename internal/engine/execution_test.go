package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func graphRun(t *testing.T, limit int) *model.Run {
	t.Helper()
	workflow := model.Workflow{Version: "2", Name: "review", Entry: "draft", Nodes: []model.Node{
		{ID: "draft", Executor: "process", Input: map[string]any{"command": []string{"sh", "-c", `printf '{"summary":"draft"}' > "$AGENTFLOW_RESULT_FILE"`}}},
		{ID: "review", Input: map[string]any{"text": "Review {{ nodes.draft.result.summary }}"}, Executor: "external"},
		{ID: "finalize", Executor: "process", Input: map[string]any{"command": []string{"sh", "-c", `printf '{}' > "$AGENTFLOW_RESULT_FILE"`}}},
	}, Edges: []model.Transition{
		{From: "draft", To: "review"},
		{From: "review", To: "finalize", When: "nodes.review.result.decision == 'happy'"},
		{From: "review", To: "draft", When: "nodes.review.result.decision == 'again'", MaxIterations: limit},
		{From: "review", To: "$end", When: "nodes.review.result.decision == 'sad'"},
		{From: "finalize", To: "$end"},
	}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func executeTestRun(t *testing.T, run *model.Run) {
	t.Helper()
	if err := Execute(run, map[string]driver.Executor{}); err != nil {
		t.Fatal(err)
	}
}

func decide(t *testing.T, run *model.Run, kind, message string) {
	t.Helper()
	if err := store.EnqueueControl(run.RunDir, model.Control{Kind: ControlSubmit, Output: map[string]any{"decision": kind, "message": message}, NodeID: "review"}); err != nil {
		t.Fatal(err)
	}
	executeTestRun(t, run)
}

func TestApproveFollowsNamedEdge(t *testing.T) {
	run := graphRun(t, 2)
	executeTestRun(t, run)
	if run.State.Status != "waiting_input" || run.State.ActiveNode != "review" {
		t.Fatalf("unexpected wait: %+v", run.State)
	}
	decide(t, run, "happy", "go")
	if run.State.Status != "succeeded" || run.State.Outcome != "completed" || run.State.Nodes["finalize"].Status != "succeeded" {
		t.Fatalf("approve did not reach finalize: %+v", run.State)
	}
}

func TestRequestChangesRevisitsDraftAndKeepsHistory(t *testing.T) {
	run := graphRun(t, 2)
	executeTestRun(t, run)
	decide(t, run, "again", "please improve")
	if run.State.Status != "waiting_input" || run.State.Visits["draft"] != 2 || run.State.Visits["review"] != 2 {
		t.Fatalf("request changes did not return to review: %+v", run.State)
	}
	if len(run.State.History) != 3 || run.State.History[1].State.Result.(map[string]any)["message"] != "please improve" {
		t.Fatalf("visit history missing review feedback: %+v", run.State.History)
	}
	for _, name := range []string{"draft.1.result.json", "draft.2.result.json"} {
		if _, err := os.Stat(filepath.Join(run.RunDir, "artifacts", name)); err != nil {
			t.Fatal(err)
		}
	}
	decide(t, run, "happy", "good")
	if run.State.Status != "succeeded" || run.State.Nodes["finalize"].Status != "succeeded" {
		t.Fatalf("approve after rework failed: %+v", run.State)
	}
}

func TestRejectIsBusinessOutcome(t *testing.T) {
	run := graphRun(t, 2)
	executeTestRun(t, run)
	decide(t, run, "sad", "no")
	if run.State.Status != "succeeded" || run.State.Outcome != "completed" || run.State.Nodes["finalize"].Status != "skipped" {
		t.Fatalf("reject followed wrong edge: %+v", run.State)
	}
}

func TestReworkBoundStopsRun(t *testing.T) {
	run := graphRun(t, 1)
	executeTestRun(t, run)
	decide(t, run, "again", "first")
	decide(t, run, "again", "second")
	if run.State.Status != "failed" || run.State.Outcome != "max_iterations" || run.State.Visits["draft"] != 2 {
		t.Fatalf("bound not enforced: %+v", run.State)
	}
}

func TestProgramResultRoutesByNumericCondition(t *testing.T) {
	workflow := model.Workflow{Version: "2", Name: "program", Entry: "check", Nodes: []model.Node{
		{ID: "check", Input: map[string]any{"threshold": 7, "command": []string{"sh", "-c", `test -f "$AGENTFLOW_INPUT_FILE" && printf '{"score":8}' > "$AGENTFLOW_RESULT_FILE"`}}, Executor: "process"},
		{ID: "high", Executor: "process", Input: map[string]any{"command": []string{"sh", "-c", `printf '{}' > "$AGENTFLOW_RESULT_FILE"`}}},
	}, Edges: []model.Transition{
		{From: "check", To: "high", When: "nodes.check.result.score > 7"},
		{From: "check", To: "$end"},
		{From: "high", To: "$end"},
	}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	executeTestRun(t, run)
	if run.State.Status != "succeeded" || run.State.Nodes["high"].Status != "succeeded" {
		t.Fatalf("program did not route: %+v", run.State)
	}
}

func TestHumanAdapterProgramAdvancesWithoutManualControl(t *testing.T) {
	workflow := model.Workflow{Version: "2", Name: "adapter", Entry: "review", Nodes: []model.Node{
		{ID: "review", Input: map[string]any{"text": "choose", "command": []string{"sh", "-c", `printf '{"decision":"happy","message":"ok"}' > "$AGENTFLOW_RESULT_FILE"`}}, Executor: "process"},
	}, Edges: []model.Transition{{From: "review", To: "$end", When: "nodes.review.result.decision == 'happy'"}}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	executeTestRun(t, run)
	if run.State.Status != "succeeded" || run.State.Nodes["review"].Status != "succeeded" {
		t.Fatalf("adapter did not advance: %+v", run.State)
	}
}

func TestEmptyObjectInputSurvivesExternalResume(t *testing.T) {
	root := t.TempDir()
	workflow := model.Workflow{Version: "2", Name: "empty-input", Entry: "wait", Nodes: []model.Node{{ID: "wait", Executor: "external", Input: map[string]any{}, InputSchema: map[string]any{"type": "object"}}}, Edges: []model.Transition{{From: "wait", To: "wait", When: "nodes.wait.result.again", MaxIterations: 1}, {From: "wait", To: "$end"}}}
	run, err := store.CreateRun(root, workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = Execute(run, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadRun(root, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.EnqueueControl(loaded.RunDir, model.Control{Kind: ControlSubmit, NodeID: "wait", Output: map[string]any{"again": true}}); err != nil {
		t.Fatal(err)
	}
	if err = Execute(loaded, nil); err != nil {
		t.Fatal(err)
	}
	if loaded.State.Status != "waiting_input" || loaded.State.Visits["wait"] != 2 {
		t.Fatalf("empty JSON object changed on resume: %+v", loaded.State)
	}
}
