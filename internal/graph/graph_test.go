package graph

import (
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
)

func TestDescribeMergesControlDataAndConditionChannels(t *testing.T) {
	workflow := model.Workflow{Version: "1", Name: "graph", Nodes: []model.Node{
		{ID: "inspect", Type: "shell", Command: []string{"true"}},
		{ID: "review", Type: "human", Prompt: "approve", Needs: []string{"inspect"}, When: "nodes.inspect.result.ready == true", InputMapping: map[string]string{"summary": "$node.inspect.output.summary"}},
	}}
	document := Describe(workflow)
	if len(document.Edges) != 1 {
		t.Fatalf("edges = %#v", document.Edges)
	}
	edge := document.Edges[0]
	if edge.Source != "inspect" || edge.Target != "review" || len(edge.Channels) != 3 {
		t.Fatalf("unexpected edge: %#v", edge)
	}
	if edge.Mapping["summary"] != "$node.inspect.output.summary" {
		t.Fatalf("mapping = %#v", edge.Mapping)
	}
}

func TestValidateRejectsUndeclaredMappingDependency(t *testing.T) {
	workflow := model.Workflow{Version: "1", Name: "graph", Nodes: []model.Node{
		{ID: "source", Type: "shell", Command: []string{"true"}},
		{ID: "consumer", Type: "shell", InputMapping: map[string]string{"value": "$node.source.output.value"}, Command: []string{"true"}},
	}}
	errors := Validate(workflow)
	if len(errors) != 1 || errors[0] != "node consumer inputMapping.value references source without declaring it in needs" {
		t.Fatalf("errors = %v", errors)
	}
}

func TestParseNodeOutput(t *testing.T) {
	node, field, ok := ParseNodeOutput("$node.inspect.output.summary.text")
	if !ok || node != "inspect" || field != "summary.text" {
		t.Fatalf("parsed = %q %q %v", node, field, ok)
	}
	if _, _, ok := ParseNodeOutput("$output.summary"); ok {
		t.Fatal("$output reference should not identify a source node")
	}
}
