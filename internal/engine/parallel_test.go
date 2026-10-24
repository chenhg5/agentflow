package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func parallelWorkflowFixture(workspace string, second model.Node) model.Workflow {
	return model.Workflow{Version: "3", Name: "parallel", Entry: "start", Nodes: []model.Node{
		{ID: "start", Executor: "process", Dispatch: "all", Config: map[string]any{"command": []string{"sh", "-c", `printf '{"seed":"ok"}' > "$AGENTFLOW_RESULT_FILE"`}}},
		{ID: "a", Executor: "process", Config: map[string]any{"command": []string{"sh", "-c", `touch "$AGENTFLOW_WORKSPACE/a.ready"; i=0; while [ ! -f "$AGENTFLOW_WORKSPACE/b.ready" ] && [ "$i" -lt 100 ]; do sleep 0.02; i=$((i+1)); done; test -f "$AGENTFLOW_WORKSPACE/b.ready" || exit 4; printf '{"value":"a"}' > "$AGENTFLOW_RESULT_FILE"`}}},
		second,
		{ID: "join", Executor: "process", Join: &model.JoinSpec{From: []string{"a", "b"}}, Config: map[string]any{"command": []string{"sh", "-c", `cat "$AGENTFLOW_INPUT_FILE" > "$AGENTFLOW_RESULT_FILE"`}}},
	}, Edges: []model.Transition{
		{From: "start", To: "a", Map: testEdgeMap(map[string]any{"seed": "{{ output.seed }}"})},
		{From: "start", To: "b", Map: testEdgeMap(map[string]any{"seed": "{{ output.seed }}"})},
		{From: "a", To: "join"}, {From: "b", To: "join"}, {From: "join", To: "$end"},
	}}
}

func TestParallelNodesRunConcurrentlyAndJoinInputs(t *testing.T) {
	workspace := t.TempDir()
	second := model.Node{ID: "b", Executor: "process", Config: map[string]any{"command": []string{"sh", "-c", `touch "$AGENTFLOW_WORKSPACE/b.ready"; i=0; while [ ! -f "$AGENTFLOW_WORKSPACE/a.ready" ] && [ "$i" -lt 100 ]; do sleep 0.02; i=$((i+1)); done; test -f "$AGENTFLOW_WORKSPACE/a.ready" || exit 4; printf '{"value":"b"}' > "$AGENTFLOW_RESULT_FILE"`}}}
	workflow := parallelWorkflowFixture(workspace, second)
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, map[string]driver.Executor{"process": {Kind: "command", Workspace: workspace}}); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "succeeded" || len(run.State.ActiveNodes) != 0 {
		t.Fatalf("parallel graph did not complete: %+v", run.State)
	}
	joined := run.State.Nodes["join"].Input.(map[string]any)
	if joined["a"].(map[string]any)["value"] != "a" || joined["b"].(map[string]any)["value"] != "b" {
		t.Fatalf("wrong join input: %#v", joined)
	}
	if run.State.Nodes["a"].Input.(map[string]any)["seed"] != "ok" || run.State.Nodes["b"].Input.(map[string]any)["seed"] != "ok" {
		t.Fatal("fork did not map source output to both branches")
	}
}

func TestParallelWaitPersistsPartialJoinAndResumes(t *testing.T) {
	workspace := t.TempDir()
	second := model.Node{ID: "b", Executor: "external"}
	workflow := parallelWorkflowFixture(workspace, second)
	workflow.Nodes[1].Config = map[string]any{"command": []string{"sh", "-c", `printf '{"value":"a"}' > "$AGENTFLOW_RESULT_FILE"`}}
	workflow.Edges[1].Map = testEdgeMap(map[string]any{"seed": "{{ output.seed }}"})
	root := t.TempDir()
	run, err := store.CreateRun(root, workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	executors := map[string]driver.Executor{"process": {Kind: "command", Workspace: workspace}}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "waiting_input" || run.State.ActiveNode != "b" || run.State.JoinInputs["join"]["a"] == nil {
		t.Fatalf("partial join was not persisted: %+v", run.State)
	}
	loaded, err := store.LoadRun(root, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnqueueControl(loaded.RunDir, model.Control{Kind: ControlSubmit, NodeID: "b", Output: map[string]any{"value": "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := Execute(loaded, executors); err != nil {
		t.Fatal(err)
	}
	if loaded.State.Status != "succeeded" || loaded.State.Visits["a"] != 1 || loaded.State.Visits["b"] != 1 {
		t.Fatalf("parallel resume failed: %+v", loaded.State)
	}
}

func TestParallelRetryOnlyFailedBranch(t *testing.T) {
	workspace := t.TempDir()
	second := model.Node{ID: "b", Executor: "process", Config: map[string]any{"command": []string{"sh", "-c", `if [ ! -f "$AGENTFLOW_WORKSPACE/attempted" ]; then touch "$AGENTFLOW_WORKSPACE/attempted"; exit 5; fi; printf '{"value":"b"}' > "$AGENTFLOW_RESULT_FILE"`}}}
	workflow := parallelWorkflowFixture(workspace, second)
	workflow.Nodes[1].Config = map[string]any{"command": []string{"sh", "-c", `touch "$AGENTFLOW_WORKSPACE/a.once"; printf '{"value":"a"}' > "$AGENTFLOW_RESULT_FILE"`}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	executors := map[string]driver.Executor{"process": {Kind: "command", Workspace: workspace}}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "failed" || run.State.Nodes["b"].Status != "failed" {
		t.Fatalf("expected one failed branch: %+v", run.State)
	}
	if err := store.EnqueueControl(run.RunDir, model.Control{Kind: ControlRetry}); err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "succeeded" || run.State.Nodes["a"].Attempts != 1 || run.State.Nodes["b"].Attempts != 1 {
		t.Fatalf("retry did not preserve completed branch: %+v", run.State)
	}
	if _, err := os.Stat(filepath.Join(workspace, "a.once")); err != nil {
		t.Fatal(err)
	}
}

func TestParallelJoinCanRunAgainAfterLoop(t *testing.T) {
	workspace := t.TempDir()
	workflow := model.Workflow{Version: "3", Name: "repeated-fork", Entry: "start", Nodes: []model.Node{
		{ID: "start", Executor: "process", Dispatch: "all", Config: map[string]any{"command": []string{"sh", "-c", `file="$AGENTFLOW_WORKSPACE/count"; n=0; if [ -f "$file" ]; then n=$(cat "$file"); fi; n=$((n+1)); printf '%s' "$n" > "$file"; printf '{"n":%s}' "$n" > "$AGENTFLOW_RESULT_FILE"`}}},
		{ID: "a", Executor: "process", Config: map[string]any{"command": []string{"sh", "-c", `cat "$AGENTFLOW_INPUT_FILE" > "$AGENTFLOW_RESULT_FILE"`}}},
		{ID: "b", Executor: "process", Config: map[string]any{"command": []string{"sh", "-c", `cat "$AGENTFLOW_INPUT_FILE" > "$AGENTFLOW_RESULT_FILE"`}}},
		{ID: "join", Executor: "process", Join: &model.JoinSpec{From: []string{"a", "b"}}, Config: map[string]any{"command": []string{"sh", "-c", `cat "$AGENTFLOW_INPUT_FILE" > "$AGENTFLOW_RESULT_FILE"`}}},
	}, Edges: []model.Transition{
		{From: "start", To: "a"}, {From: "start", To: "b"},
		{From: "a", To: "join"}, {From: "b", To: "join"},
		{From: "join", To: "start", When: "nodes.start.result.n < 2", MaxIterations: 1},
		{From: "join", To: "$end"},
	}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, map[string]driver.Executor{"process": {Kind: "command", Workspace: workspace}}); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "succeeded" || run.State.Visits["start"] != 2 || run.State.Visits["join"] != 2 || len(run.State.JoinInputs) != 0 {
		t.Fatalf("repeated join did not complete: %+v", run.State)
	}
}

func TestParallelExternalBranchesAcceptIndependentSubmissions(t *testing.T) {
	workflow := model.Workflow{Version: "3", Name: "two-reviews", Entry: "start", Nodes: []model.Node{
		{ID: "start", Executor: "process", Dispatch: "all", Config: map[string]any{"command": []string{"sh", "-c", `printf '{}' > "$AGENTFLOW_RESULT_FILE"`}}},
		{ID: "a", Executor: "external"}, {ID: "b", Executor: "external"},
		{ID: "join", Executor: "process", Join: &model.JoinSpec{From: []string{"a", "b"}}, Config: map[string]any{"command": []string{"sh", "-c", `cat "$AGENTFLOW_INPUT_FILE" > "$AGENTFLOW_RESULT_FILE"`}}},
	}, Edges: []model.Transition{{From: "start", To: "a"}, {From: "start", To: "b"}, {From: "a", To: "join"}, {From: "b", To: "join"}, {From: "join", To: "$end"}}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, nil); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "waiting_input" || len(run.State.ActiveNodes) != 2 || run.State.ActiveNode != "" {
		t.Fatalf("both branches should wait independently: %+v", run.State)
	}
	if err := store.EnqueueControl(run.RunDir, model.Control{Kind: ControlSubmit, NodeID: "b", Output: map[string]any{"answer": "right"}}); err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, nil); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "waiting_input" || run.State.ActiveNode != "a" || run.State.JoinInputs["join"]["b"] == nil {
		t.Fatalf("first submission lost the other branch: %+v", run.State)
	}
	if err := store.EnqueueControl(run.RunDir, model.Control{Kind: ControlSubmit, NodeID: "a", Output: map[string]any{"answer": "left"}}); err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, nil); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "succeeded" || run.State.Nodes["join"].Result.(map[string]any)["a"].(map[string]any)["answer"] != "left" {
		t.Fatalf("independent submissions did not join: %+v", run.State)
	}
}

func TestParallelJoinFailsWhenRequiredBranchWasNotSelected(t *testing.T) {
	workflow := model.Workflow{Version: "3", Name: "incomplete", Entry: "start", Nodes: []model.Node{
		{ID: "start", Executor: "process", Dispatch: "all", Config: map[string]any{"command": []string{"sh", "-c", `printf '{}' > "$AGENTFLOW_RESULT_FILE"`}}},
		{ID: "a", Executor: "process", Config: map[string]any{"command": []string{"sh", "-c", `printf '{}' > "$AGENTFLOW_RESULT_FILE"`}}},
		{ID: "b", Executor: "process", Config: map[string]any{"command": []string{"sh", "-c", `printf '{}' > "$AGENTFLOW_RESULT_FILE"`}}},
		{ID: "join", Executor: "process", Join: &model.JoinSpec{From: []string{"a", "b"}}, Config: map[string]any{"command": []string{"sh", "-c", `printf '{}' > "$AGENTFLOW_RESULT_FILE"`}}},
	}, Edges: []model.Transition{{From: "start", To: "a"}, {From: "start", To: "b", When: "false"}, {From: "a", To: "join"}, {From: "b", To: "join"}, {From: "join", To: "$end"}}}
	run, err := store.CreateRun(t.TempDir(), workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, nil); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "failed" || run.State.Outcome != "join_incomplete" || run.State.Nodes["join"].Status != "failed" {
		t.Fatalf("missing join branch should fail clearly: %+v", run.State)
	}
}
