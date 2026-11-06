package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/engine"
	"github.com/chenhg5/agentflow/internal/hubapi"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestHubApplierSubmitsToAWaitingNode(t *testing.T) {
	root := t.TempDir()
	workflow := model.Workflow{Version: "3", Name: "review", Entry: "decide",
		Nodes: []model.Node{{ID: "decide", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "external"}}},
			OutputSchema: map[string]any{"type": "object", "required": []any{"decision"}, "properties": map[string]any{"decision": map[string]any{"type": "string", "enum": []any{"approve", "hold"}}}}}},
		Edges: []model.Transition{{From: "decide", To: "$end"}}}
	run, err := store.CreateRun(root, workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Execute(run, nil); err != nil {
		t.Fatal(err)
	}
	if run.State.Nodes["decide"].Status != "waiting_input" {
		t.Fatalf("setup: node is %s", run.State.Nodes["decide"].Status)
	}
	apply := hubApplier(root)
	started := run.State.Nodes["decide"].StartedAt
	ctx := context.Background()
	for name, command := range map[string]hubapi.Command{
		"unknown run":   {Kind: "submit", RunID: "run_nope", NodeID: "decide", Output: map[string]any{"decision": "approve"}},
		"unknown kind":  {Kind: "delete", RunID: run.RunID, NodeID: "decide"},
		"bad output":    {Kind: "submit", RunID: run.RunID, NodeID: "decide", Output: map[string]any{"decision": "yolo"}, StepStartedAt: started},
		"another visit": {Kind: "submit", RunID: run.RunID, NodeID: "decide", Output: map[string]any{"decision": "approve"}, StepStartedAt: "2001-01-01T00:00:00Z"},
	} {
		if err := apply(ctx, command); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if store.HasControls(run.RunDir) {
		t.Fatal("refused commands must not queue anything")
	}
	// The Hub keeps times in milliseconds: its copy of the start time differs
	// from the run state's nanoseconds and must still match.
	parsed, _ := time.Parse(time.RFC3339Nano, started)
	hubCopy := time.UnixMilli(parsed.UnixMilli()).UTC().Format(time.RFC3339Nano)
	if err := apply(ctx, hubapi.Command{Kind: "submit", RunID: run.RunID, NodeID: "decide", Output: map[string]any{"decision": "approve"}, StepStartedAt: hubCopy}); err != nil {
		t.Fatal(err)
	}
	controls, err := store.PeekControls(run.RunDir)
	if err != nil || len(controls) != 1 || controls[0].Control.Kind != engine.ControlSubmit || controls[0].Control.Output["decision"] != "approve" || !strings.HasPrefix(controls[0].Control.NodeID, "decide") {
		t.Fatalf("queued controls = %+v, %v", controls, err)
	}
}

func TestParseRetention(t *testing.T) {
	for value, want := range map[string]time.Duration{"": 90 * 24 * time.Hour, "0": 0, "30d": 30 * 24 * time.Hour, "720h": 720 * time.Hour, " 7d ": 7 * 24 * time.Hour} {
		if got, err := parseRetention(value); err != nil || got != want {
			t.Errorf("parseRetention(%q) = %v, %v; want %v", value, got, err, want)
		}
	}
	for _, bad := range []string{"forever", "-1d", "1.5d", "-5h"} {
		if _, err := parseRetention(bad); err == nil {
			t.Errorf("parseRetention(%q) accepted", bad)
		}
	}
}
