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
	aggregate := Aggregate([]*model.Run{run}, Filter{Workflow: "demo"}, now)
	if aggregate.Runs.Succeeded != 1 || len(aggregate.NodeGroups) != 1 || aggregate.NodeGroups[0].Duration.Avg != 1500 {
		t.Fatalf("unexpected aggregate: %#v", aggregate)
	}
}

func TestAggregateSplitsWaitingAndExcludesOpenRuns(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	at := func(offset time.Duration) string { return now.Add(offset).Format(time.RFC3339Nano) }
	workflow := model.Workflow{Name: "gate", Nodes: []model.Node{{ID: "build", Type: "shell"}, {ID: "approve", Type: "human", Needs: []string{"build"}}}}
	done := &model.Run{RunID: "done", Workflow: workflow, State: model.RunState{
		WorkflowName: "gate", Status: "succeeded", CreatedAt: at(-10 * time.Second), StartedAt: at(-10 * time.Second), FinishedAt: at(-4 * time.Second),
		Nodes: map[string]*model.NodeState{
			"build":   {Status: "succeeded", StartedAt: at(-10 * time.Second), FinishedAt: at(-9 * time.Second)},
			"approve": {Status: "succeeded", StartedAt: at(-9 * time.Second), FinishedAt: at(-4 * time.Second), WaitDurationMs: 5000},
		},
	}}
	open := &model.Run{RunID: "open", Workflow: workflow, State: model.RunState{
		WorkflowName: "gate", Status: "waiting_human", CreatedAt: at(-time.Hour), StartedAt: at(-time.Hour),
		Nodes: map[string]*model.NodeState{
			"build":   {Status: "succeeded", StartedAt: at(-time.Hour), FinishedAt: at(-time.Hour + time.Second)},
			"approve": {Status: "waiting_human", StartedAt: at(-time.Hour + time.Second)},
		},
	}}
	snapshot := Snapshot(done, now)
	if snapshot.DurationMs != 6000 || snapshot.WaitingMs != 5000 || snapshot.ActiveMs != 1000 {
		t.Fatalf("expected 6000 total = 1000 active + 5000 waiting, got %#v", snapshot)
	}
	aggregate := Aggregate([]*model.Run{done, open}, Filter{Workflow: "gate"}, now)
	if aggregate.Runs.Total != 2 || aggregate.Runs.WaitingHuman != 1 {
		t.Fatalf("unexpected counts: %#v", aggregate.Runs)
	}
	if aggregate.Duration.Count != 1 || aggregate.Duration.Max != 6000 || aggregate.Active.Avg != 1000 || aggregate.Waiting.Avg != 5000 {
		t.Fatalf("open run leaked into run distributions: %#v", aggregate)
	}
}

func TestAggregateByDigestSeparatesWorkflowVersions(t *testing.T) {
	now := time.Now().UTC()
	v1 := model.Workflow{Name: "demo", Nodes: []model.Node{{ID: "a", Type: "shell"}}}
	v2 := model.Workflow{Name: "demo", Nodes: []model.Node{{ID: "a", Type: "shell", TimeoutMs: 1000}}}
	run := func(id, created string, workflow model.Workflow) *model.Run {
		return &model.Run{RunID: id, Workflow: workflow, State: model.RunState{WorkflowName: "demo", Status: "succeeded", CreatedAt: created, Nodes: map[string]*model.NodeState{"a": {Status: "succeeded"}}}}
	}
	runs := []*model.Run{run("r3", "2026-10-03", v2), run("r1", "2026-10-01", v1), run("r2", "2026-10-02", v1)}
	runs[0].State.WorkflowDigest = model.Digest(v2)
	groups := AggregateByDigest(runs, "demo", now)
	if len(groups) != 2 {
		t.Fatalf("expected two versions, got %#v", groups)
	}
	if groups[0].WorkflowDigest != model.Digest(v1) || groups[0].Runs.Total != 2 || groups[0].FirstRunAt != "2026-10-01" {
		t.Fatalf("first group should be v1 with two runs: %#v", groups[0])
	}
	if groups[1].WorkflowDigest != model.Digest(v2) || groups[1].Runs.Total != 1 {
		t.Fatalf("second group should be v2 with one run: %#v", groups[1])
	}
	if model.Digest(v1) == model.Digest(v2) {
		t.Fatal("changing a node must change the digest")
	}
}
