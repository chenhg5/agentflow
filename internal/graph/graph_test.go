package graph

import (
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
)

func TestDescribeAndValidateConditionalCycle(t *testing.T) {
	workflow := model.Workflow{Version: "3", Name: "graph", Entry: "draft", Nodes: []model.Node{
		{ID: "draft", Execution: oneCandidate("codex")}, {ID: "review", Execution: oneCandidate("external")},
	}, Edges: []model.Transition{
		{From: "draft", To: "review", Map: &map[string]any{"summary": "{{ output.summary }}"}},
		{From: "review", To: "draft", When: "nodes.review.result.decision == 'again'", MaxIterations: 2},
		{From: "review", To: End},
	}}
	if problems := Validate(workflow); len(problems) != 0 {
		t.Fatalf("valid graph: %v", problems)
	}
	document := Describe(workflow)
	if len(document.Edges) != 3 || document.Edges[0].Map == nil || (*document.Edges[0].Map)["summary"] != "{{ output.summary }}" || document.Edges[1].Condition != workflow.Edges[1].When || document.Edges[1].MaxIterations != 2 {
		t.Fatalf("unexpected graph view: %+v", document)
	}
	workflow.Edges[1].MaxIterations = 0
	if problems := Validate(workflow); len(problems) == 0 {
		t.Fatal("unbounded cycle should fail")
	}
	workflow.AllowUnboundedCycles = true
	if problems := Validate(workflow); len(problems) != 0 {
		t.Fatalf("explicitly unbounded cycle should pass: %v", problems)
	}
}

func TestParallelDispatchAndJoinValidation(t *testing.T) {
	workflow := model.Workflow{Version: "3", Name: "parallel", Entry: "start", Nodes: []model.Node{
		{ID: "start", Execution: oneCandidate("process"), Dispatch: "all"},
		{ID: "left", Execution: oneCandidate("process")},
		{ID: "right", Execution: oneCandidate("process")},
		{ID: "join", Execution: oneCandidate("process"), Join: &model.JoinSpec{From: []string{"left", "right"}}},
	}, Edges: []model.Transition{
		{From: "start", To: "left"}, {From: "start", To: "right"},
		{From: "left", To: "join"}, {From: "right", To: "join"}, {From: "join", To: End},
	}}
	if problems := Validate(workflow); len(problems) != 0 {
		t.Fatalf("valid parallel graph: %v", problems)
	}
	if channels := Describe(workflow).Edges[0].Channels; len(channels) != 2 || channels[1] != ChannelParallel {
		t.Fatalf("fan-out edge missing parallel channel: %v", channels)
	}
	workflow.Nodes[0].Dispatch = "first"
	if problems := Validate(workflow); len(problems) == 0 {
		t.Fatal("serial dispatch must reject multiple defaults")
	}
	workflow.Nodes[0].Dispatch = "all"
	workflow.Nodes[3].Execution.Candidates[0].Session.Mode = "inherit"
	workflow.Nodes[3].Execution.Candidates[0].Session.From = "start"
	if problems := Validate(workflow); len(problems) == 0 {
		t.Fatal("join session source must be a declared predecessor")
	}
	workflow.Nodes[3].Execution.Candidates[0].Session = model.SessionSpec{}
	workflow.Nodes[3].Join.From = []string{"left", "unknown"}
	if problems := Validate(workflow); len(problems) == 0 {
		t.Fatal("join must reject non-incoming predecessor")
	}
}

func oneCandidate(executor string) model.Execution {
	return model.Execution{Candidates: []model.ExecutorCandidate{{Executor: executor}}}
}
