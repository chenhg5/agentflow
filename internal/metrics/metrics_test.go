package metrics

import (
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
)

func TestSnapshotAndAggregate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	start := now.Add(-2 * time.Second).Format(time.RFC3339Nano)
	finish := now.Add(-500 * time.Millisecond).Format(time.RFC3339Nano)
	run := &model.Run{RunID: "run-1", Workflow: model.Workflow{Name: "demo", Nodes: []model.Node{{ID: "agent", Type: "agent"}}}, State: model.RunState{
		RunID: "run-1", WorkflowName: "demo", Status: "succeeded", StartedAt: start, FinishedAt: finish,
		Nodes: map[string]*model.NodeState{"agent": {Status: "succeeded", StartedAt: start, FinishedAt: finish, Attempts: 2, InputBytes: 100, OutputBytes: 80, StdoutBytes: 12, StderrBytes: 3}},
	}}
	snapshot := Snapshot(run, now)
	if snapshot.DurationMs != 1500 || snapshot.Totals.Attempts != 2 || snapshot.Totals.OutputBytes != 80 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	aggregate := Aggregate([]*model.Run{run}, "demo", now)
	if aggregate.Runs.Succeeded != 1 || len(aggregate.NodeGroups) != 1 || aggregate.NodeGroups[0].Duration.Avg != 1500 {
		t.Fatalf("unexpected aggregate: %#v", aggregate)
	}
}
