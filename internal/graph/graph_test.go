package graph

import (
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
)

func TestDescribeAndValidateConditionalCycle(t *testing.T) {
	workflow := model.Workflow{Version: "2", Name: "graph", Entry: "draft", Nodes: []model.Node{
		{ID: "draft", Executor: "codex"}, {ID: "review", Executor: "external"},
	}, Edges: []model.Transition{
		{From: "draft", To: "review"},
		{From: "review", To: "draft", When: "nodes.review.result.decision == 'again'", MaxIterations: 2},
		{From: "review", To: End},
	}}
	if problems := Validate(workflow); len(problems) != 0 {
		t.Fatalf("valid graph: %v", problems)
	}
	document := Describe(workflow)
	if len(document.Edges) != 3 || document.Edges[1].Condition != workflow.Edges[1].When || document.Edges[1].MaxIterations != 2 {
		t.Fatalf("unexpected graph view: %+v", document)
	}
	workflow.Edges[1].MaxIterations = 0
	if problems := Validate(workflow); len(problems) == 0 {
		t.Fatal("unbounded cycle should fail")
	}
}
