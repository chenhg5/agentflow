package metrics

import (
	"math"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
)

func TestSnapshotAndAggregate(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	start := now.Add(-2 * time.Second).Format(time.RFC3339Nano)
	finish := now.Add(-500 * time.Millisecond).Format(time.RFC3339Nano)
	run := &model.Run{RunID: "run-1", Workflow: model.Workflow{Name: "demo", Nodes: []model.Node{{ID: "agent", Execution: metricExecution("codex")}}}, State: model.RunState{
		RunID: "run-1", WorkflowName: "demo", Status: "succeeded", StartedAt: start, FinishedAt: finish,
		Nodes: map[string]*model.NodeState{"agent": {Status: "succeeded", SelectedExecutor: "codex", StartedAt: start, FinishedAt: finish, Attempts: 2, AttemptSequence: []model.AttemptEvidence{{Sequence: 1, Executor: "primary", Status: "failed"}, {Sequence: 2, Executor: "codex", Status: "succeeded"}}, InputBytes: 100, OutputBytes: 80, StdoutBytes: 12, StderrBytes: 3}},
	}}
	snapshot := Snapshot(run, now)
	if snapshot.DurationMs != 1500 || snapshot.Totals.Attempts != 2 || snapshot.Totals.OutputBytes != 80 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if snapshot.Nodes[0].Executor != "codex" || len(snapshot.Nodes[0].AttemptSequence) != 2 {
		t.Fatalf("candidate evidence missing from snapshot: %#v", snapshot.Nodes[0])
	}
	aggregate := Aggregate([]*model.Run{run}, Filter{Workflow: "demo"}, now)
	if aggregate.Runs.Succeeded != 1 || len(aggregate.NodeGroups) != 1 || aggregate.NodeGroups[0].Duration.Avg != 1500 {
		t.Fatalf("unexpected aggregate: %#v", aggregate)
	}
}

func TestSnapshotIncludesArchivedVisitsOnce(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	at := func(offset time.Duration) string { return now.Add(offset).Format(time.RFC3339Nano) }
	workflow := model.Workflow{Name: "retry", Nodes: []model.Node{{ID: "work", Execution: metricExecution("worker")}}}
	failed := model.NodeState{
		Status: "failed", SelectedExecutor: "worker", StartedAt: at(-500 * time.Millisecond), FinishedAt: at(-400 * time.Millisecond),
		Attempts: 1, AttemptSequence: []model.AttemptEvidence{{Sequence: 1, Executor: "worker", Status: "failed"}},
		InputBytes: 10, OutputBytes: 20, StdoutBytes: 30, StderrBytes: 40, WaitDurationMs: 25,
	}
	succeeded := model.NodeState{
		Status: "succeeded", SelectedExecutor: "worker", StartedAt: at(-300 * time.Millisecond), FinishedAt: at(-100 * time.Millisecond),
		Attempts: 1, AttemptSequence: []model.AttemptEvidence{{Sequence: 2, Executor: "worker", Status: "succeeded"}},
		InputBytes: 1, OutputBytes: 2, StdoutBytes: 3, StderrBytes: 4, WaitDurationMs: 15,
	}
	run := &model.Run{RunID: "retry-1", Workflow: workflow, State: model.RunState{
		WorkflowName: "retry", Status: "succeeded", StartedAt: at(-600 * time.Millisecond), FinishedAt: now.Format(time.RFC3339Nano),
		Visits: map[string]int{"work": 2}, Nodes: map[string]*model.NodeState{"work": &succeeded},
		History: []model.NodeVisit{{NodeID: "work", Visit: 1, State: failed}, {NodeID: "work", Visit: 2, State: succeeded}},
	}}
	snapshot := Snapshot(run, now)
	if len(snapshot.Nodes) != 2 || snapshot.Nodes[0].Visit != 1 || snapshot.Nodes[1].Visit != 2 {
		t.Fatalf("visits were omitted or duplicated: %+v", snapshot.Nodes)
	}
	if snapshot.Totals.Attempts != 2 || snapshot.Totals.InputBytes != 11 || snapshot.Totals.OutputBytes != 22 || snapshot.Totals.StdoutBytes != 33 || snapshot.Totals.StderrBytes != 44 {
		t.Fatalf("archived bytes or attempts missing: %+v", snapshot.Totals)
	}
	if snapshot.WaitingMs != 40 || snapshot.ActiveMs != 560 {
		t.Fatalf("archived waiting time missing: %+v", snapshot)
	}
	aggregate := Aggregate([]*model.Run{run}, Filter{Workflow: "retry"}, now)
	if len(aggregate.NodeGroups) != 1 || aggregate.NodeGroups[0].Executions != 2 || aggregate.NodeGroups[0].Totals.Attempts != 2 || aggregate.NodeGroups[0].Totals.OutputBytes != 22 {
		t.Fatalf("aggregate omitted archived visits: %+v", aggregate.NodeGroups)
	}
}

func TestSnapshotUnionsOverlappingParallelWaits(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	at := func(offset time.Duration) string { return now.Add(offset).Format(time.RFC3339Nano) }
	workflow := model.Workflow{Name: "parallel", Nodes: []model.Node{
		{ID: "left", Execution: metricExecution("external")},
		{ID: "right", Execution: metricExecution("external")},
	}}
	run := &model.Run{RunID: "parallel-1", Workflow: workflow, State: model.RunState{
		WorkflowName: "parallel", Status: "succeeded", StartedAt: at(-time.Second), FinishedAt: at(-200 * time.Millisecond),
		Nodes: map[string]*model.NodeState{
			"left":  {Status: "succeeded", StartedAt: at(-900 * time.Millisecond), WaitingStartedAt: at(-700 * time.Millisecond), FinishedAt: at(-300 * time.Millisecond)},
			"right": {Status: "succeeded", StartedAt: at(-900 * time.Millisecond), WaitingStartedAt: at(-700 * time.Millisecond), FinishedAt: at(-300 * time.Millisecond)},
		},
	}}
	snapshot := Snapshot(run, now)
	if snapshot.DurationMs != 800 || snapshot.WaitingMs != 400 || snapshot.ActiveMs != 400 {
		t.Fatalf("overlapping waits were summed instead of unioned: %+v", snapshot)
	}
}

func TestSnapshotFreezesFinishedRunWaits(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	at := func(offset time.Duration) string { return now.Add(offset).Format(time.RFC3339Nano) }
	workflow := model.Workflow{Name: "cancel", Nodes: []model.Node{
		{ID: "gate", Execution: metricExecution("external")},
		{ID: "resume", Execution: metricExecution("worker")},
	}}
	run := &model.Run{RunID: "cancel-1", Workflow: workflow, State: model.RunState{
		WorkflowName: "cancel", Status: "cancelled", StartedAt: at(-time.Second), FinishedAt: at(-400 * time.Millisecond), DurationMs: 600,
		Nodes: map[string]*model.NodeState{
			"gate": {Status: "waiting_input", StartedAt: at(-900 * time.Millisecond), WaitingStartedAt: at(-500 * time.Millisecond), FinishedAt: at(-400 * time.Millisecond)},
			// A continuation-pending node recovered but not restarted before
			// cancellation keeps its start and no finish timestamp.
			"resume": {Status: "pending", StartedAt: at(-900 * time.Millisecond)},
		},
	}}
	first := Snapshot(run, now)
	second := Snapshot(run, now.Add(time.Second))
	if first.WaitingMs != 100 || first.ActiveMs != 500 {
		t.Fatalf("cancelled wait was not bounded by the run interval: %+v", first)
	}
	if first.WaitingMs != second.WaitingMs || first.ActiveMs != second.ActiveMs || first.DurationMs != second.DurationMs {
		t.Fatalf("finished metrics changed with asOf: before=%+v after=%+v", first, second)
	}
	for index := range first.Nodes {
		if first.Nodes[index].DurationMs != second.Nodes[index].DurationMs {
			t.Fatalf("node duration changed after run completion: %s before=%d after=%d", first.Nodes[index].ID, first.Nodes[index].DurationMs, second.Nodes[index].DurationMs)
		}
		if first.Nodes[index].ID == "resume" && first.Nodes[index].DurationMs != 500 {
			t.Fatalf("pending node duration was not capped at the run boundary: %+v", first.Nodes[index])
		}
	}
	aggregateBefore := Aggregate([]*model.Run{run}, Filter{Workflow: "cancel"}, now)
	aggregateAfter := Aggregate([]*model.Run{run}, Filter{Workflow: "cancel"}, now.Add(time.Second))
	for index := range aggregateBefore.NodeGroups {
		if aggregateBefore.NodeGroups[index].Duration != aggregateAfter.NodeGroups[index].Duration {
			t.Fatalf("aggregate node duration changed after completion: %+v vs %+v", aggregateBefore.NodeGroups[index], aggregateAfter.NodeGroups[index])
		}
	}
}

func TestAggregateSplitsWaitingAndExcludesOpenRuns(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	at := func(offset time.Duration) string { return now.Add(offset).Format(time.RFC3339Nano) }
	workflow := model.Workflow{Name: "gate", Nodes: []model.Node{{ID: "build", Execution: metricExecution("process")}, {ID: "approve", Execution: metricExecution("external")}}}
	done := &model.Run{RunID: "done", Workflow: workflow, State: model.RunState{
		WorkflowName: "gate", Status: "succeeded", CreatedAt: at(-10 * time.Second), StartedAt: at(-10 * time.Second), FinishedAt: at(-4 * time.Second),
		Nodes: map[string]*model.NodeState{
			"build":   {Status: "succeeded", StartedAt: at(-10 * time.Second), FinishedAt: at(-9 * time.Second)},
			"approve": {Status: "succeeded", StartedAt: at(-9 * time.Second), FinishedAt: at(-4 * time.Second), WaitDurationMs: 5000},
		},
	}}
	open := &model.Run{RunID: "open", Workflow: workflow, State: model.RunState{
		WorkflowName: "gate", Status: "waiting_input", CreatedAt: at(-time.Hour), StartedAt: at(-time.Hour),
		Nodes: map[string]*model.NodeState{
			"build":   {Status: "succeeded", StartedAt: at(-time.Hour), FinishedAt: at(-time.Hour + time.Second)},
			"approve": {Status: "waiting_input", StartedAt: at(-time.Hour + time.Second)},
		},
	}}
	snapshot := Snapshot(done, now)
	if snapshot.DurationMs != 6000 || snapshot.WaitingMs != 5000 || snapshot.ActiveMs != 1000 {
		t.Fatalf("expected 6000 total = 1000 active + 5000 waiting, got %#v", snapshot)
	}
	aggregate := Aggregate([]*model.Run{done, open}, Filter{Workflow: "gate"}, now)
	if aggregate.Runs.Total != 2 || aggregate.Runs.WaitingInput != 1 {
		t.Fatalf("unexpected counts: %#v", aggregate.Runs)
	}
	if aggregate.Duration.Count != 1 || aggregate.Duration.Max != 6000 || aggregate.Active.Avg != 1000 || aggregate.Waiting.Avg != 5000 {
		t.Fatalf("open run leaked into run distributions: %#v", aggregate)
	}
}

func TestAggregateByDigestSeparatesWorkflowVersions(t *testing.T) {
	now := time.Now().UTC()
	v1 := model.Workflow{Name: "demo", Nodes: []model.Node{{ID: "a", Execution: metricExecution("process")}}}
	v2 := model.Workflow{Name: "demo", Nodes: []model.Node{{ID: "a", TimeoutMs: 1000, Execution: metricExecution("process")}}}
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

func TestCompareReportsRuntimeAndEvaluationChanges(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	at := func(offset time.Duration) string { return now.Add(offset).Format(time.RFC3339Nano) }
	workflow := model.Workflow{Name: "demo", Version: "3", Nodes: []model.Node{{ID: "agent", Execution: metricExecution("process")}}}
	makeRun := func(id string, duration time.Duration, score float64, created time.Duration) *model.Run {
		start := at(created)
		finish := at(created + duration)
		return &model.Run{
			RunID: id, Workflow: workflow,
			State: model.RunState{
				RunID: id, WorkflowName: "demo", Status: "succeeded", CreatedAt: start, StartedAt: start, FinishedAt: finish,
				Nodes:      map[string]*model.NodeState{"agent": {Status: "succeeded", StartedAt: start, FinishedAt: finish, Attempts: 2}},
				Evaluation: &model.Evaluation{EvaluatedAt: finish, Measurements: map[string]model.Measurement{"quality": {Value: score, Direction: "higher", Unit: "ratio"}}},
			},
		}
	}
	baseline := makeRun("baseline", 2*time.Second, 0.75, -10*time.Second)
	current := makeRun("current", time.Second, 0.90, -5*time.Second)
	comparison := Compare(current, baseline, now)
	if !comparison.SameWorkflow || !comparison.SameVersion {
		t.Fatalf("unexpected version flags: %#v", comparison)
	}
	if improved := comparison.Runtime["active_ms"].Improved; improved == nil || !*improved {
		t.Fatalf("active runtime should improve: %#v", comparison.Runtime["active_ms"])
	}
	if improved := comparison.Measurements["quality"].Improved; improved == nil || !*improved {
		t.Fatalf("quality should improve: %#v", comparison.Measurements["quality"])
	}
	if change := comparison.Measurements["quality"].ChangePercent; change == nil || math.Abs(*change-20) > 1e-9 {
		t.Fatalf("expected 20%% quality change, got %#v", change)
	}
}

func TestAggregateIncludesCustomMeasurements(t *testing.T) {
	now := time.Now().UTC()
	workflow := model.Workflow{Name: "demo", Version: "3", Nodes: []model.Node{{ID: "agent", Execution: metricExecution("process")}}}
	runs := []*model.Run{
		{RunID: "r1", Workflow: workflow, State: model.RunState{WorkflowName: "demo", Status: "succeeded", CreatedAt: "2026-10-01T00:00:00Z", Evaluation: &model.Evaluation{EvaluatedAt: "2026-10-01T00:01:00Z", Measurements: map[string]model.Measurement{"quality": {Value: 0.8, Direction: "higher", Unit: "ratio"}}}}},
		{RunID: "r2", Workflow: workflow, State: model.RunState{WorkflowName: "demo", Status: "succeeded", CreatedAt: "2026-10-02T00:00:00Z", Evaluation: &model.Evaluation{EvaluatedAt: "2026-10-02T00:01:00Z", Measurements: map[string]model.Measurement{"quality": {Value: 0.9, Direction: "higher", Unit: "ratio"}}}}},
	}
	aggregate := Aggregate(runs, Filter{Workflow: "demo"}, now)
	if len(aggregate.Measurements) != 1 || math.Abs(aggregate.Measurements[0].Values.Avg-0.85) > 1e-9 {
		t.Fatalf("unexpected measurement aggregate: %#v", aggregate.Measurements)
	}
}

func metricExecution(executor string) model.Execution {
	return model.Execution{Candidates: []model.ExecutorCandidate{{Executor: executor}}}
}

func TestCompareSkipsIncompatibleEvaluationUnits(t *testing.T) {
	baseline := &model.Evaluation{Measurements: map[string]model.Measurement{"cost": {Value: 10, Unit: "USD", Direction: "lower"}}}
	current := &model.Evaluation{Measurements: map[string]model.Measurement{"cost": {Value: 9, Unit: "minutes", Direction: "lower"}}}
	if comparisons := compareMeasurements(current, baseline); len(comparisons) != 0 {
		t.Fatalf("different units cannot be compared: %#v", comparisons)
	}
}

func TestAggregateSeparatesUnits(t *testing.T) {
	now := time.Now().UTC()
	runs := []*model.Run{
		{State: model.RunState{Status: "succeeded", Evaluation: &model.Evaluation{Measurements: map[string]model.Measurement{"cost": {Value: 2, Unit: "USD", Direction: "lower"}}}}},
		{State: model.RunState{Status: "succeeded", Evaluation: &model.Evaluation{Measurements: map[string]model.Measurement{"cost": {Value: 3, Unit: "minutes", Direction: "lower"}}}}},
	}
	aggregate := Aggregate(runs, Filter{}, now)
	if len(aggregate.Measurements) != 2 || aggregate.Measurements[0].Values.Count != 1 || aggregate.Measurements[1].Values.Count != 1 {
		t.Fatalf("different units were merged: %#v", aggregate.Measurements)
	}
}

func TestTerminalSnapshotCapsUnfinishedAttemptEvidence(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	at := func(offset time.Duration) string { return now.Add(offset).Format(time.RFC3339Nano) }
	workflow := model.Workflow{Name: "parallel", Nodes: []model.Node{
		{ID: "failed", Execution: metricExecution("worker")},
		{ID: "external", Execution: metricExecution("external")},
	}}
	run := &model.Run{RunID: "parallel-failed", Workflow: workflow, State: model.RunState{
		WorkflowName: "parallel", Status: "failed", StartedAt: at(-time.Second), FinishedAt: at(-200 * time.Millisecond),
		Nodes: map[string]*model.NodeState{
			"failed": {Status: "failed", StartedAt: at(-900 * time.Millisecond), FinishedAt: at(-300 * time.Millisecond)},
			"external": {
				Status: "waiting_input", StartedAt: at(-900 * time.Millisecond), WaitingStartedAt: at(-800 * time.Millisecond),
				AttemptSequence: []model.AttemptEvidence{{Sequence: 1, Executor: "external", Status: "waiting_input", StartedAt: at(-850 * time.Millisecond)}},
			},
		},
	}}
	first := Snapshot(run, now)
	second := Snapshot(run, now.Add(time.Second))
	var attempt *model.AttemptEvidence
	for index := range first.Nodes {
		if first.Nodes[index].ID == "external" && len(first.Nodes[index].AttemptSequence) == 1 {
			attempt = &first.Nodes[index].AttemptSequence[0]
		}
	}
	if attempt == nil || attempt.FinishedAt != run.State.FinishedAt || attempt.DurationMs != 650 {
		t.Fatalf("unfinished attempt was not capped at the run boundary: %+v", attempt)
	}
	for index := range first.Nodes {
		if first.Nodes[index].ID != "external" {
			continue
		}
		if second.Nodes[index].AttemptSequence[0].FinishedAt != attempt.FinishedAt || second.Nodes[index].AttemptSequence[0].DurationMs != attempt.DurationMs {
			t.Fatalf("attempt timing changed with asOf: before=%+v after=%+v", attempt, second.Nodes[index].AttemptSequence[0])
		}
	}
	if run.State.Nodes["external"].AttemptSequence[0].FinishedAt != "" {
		t.Fatal("snapshot mutated the durable attempt evidence")
	}
}
