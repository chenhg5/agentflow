package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/app"
	"github.com/chenhg5/agentflow/internal/hub/domain"
	"github.com/chenhg5/agentflow/internal/hub/store/memory"
)

var now = time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)

func report(machine, id, workflow, status string, started time.Time, seq int64) domain.Run {
	return domain.Run{
		Key: domain.RunKey{Machine: machine, RunID: id}, Hostname: machine + ".local", Version: "v1", Orchestration: "json",
		Workflow: workflow, Status: domain.Status(status), Seq: seq, StartedAt: started.UTC(),
		DurationMs: 10000, Steps: []domain.Step{{ID: "a", Status: domain.Status(status), Summary: "did a"}},
	}
}

func TestIngestCountsAcceptedStaleAndRejected(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	ingest := app.Ingest{Store: store, Now: func() time.Time { return now }}
	bad := report("m1", "r3", "wf", "exploded", now, 1)
	noMachine := report("", "r4", "wf", "succeeded", now, 1)
	noSeq := report("m1", "r5", "wf", "succeeded", now, 0)
	result, err := ingest.Accept(ctx, []domain.Run{
		report("m1", "r1", "wf", "running", now, 1),
		report("m1", "r1", "wf", "succeeded", now, 2),
		report("m1", "r1", "wf", "running", now, 1), // arrives late
		report("m2", "r2", "wf", "failed", now, 1),
		bad, noMachine, noSeq,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Accepted != 3 || result.Stale != 1 || len(result.Rejected) != 3 {
		t.Fatalf("result = %+v", result)
	}
	run, err := store.Get(ctx, domain.RunKey{Machine: "m1", RunID: "r1"})
	if err != nil || run.Status != domain.StatusSucceeded || !run.ReceivedAt.Equal(now.UTC()) || run.Steps[0].Summary != "did a" {
		t.Fatalf("run = %+v, %v", run, err)
	}
	machines, _ := store.Machines(ctx)
	if len(machines) != 2 || machines[0].Hostname != "m1.local" || !machines[0].LastSeen.Equal(now.UTC()) {
		t.Fatalf("machines = %+v", machines)
	}
}

func TestOverview(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	ingest := app.Ingest{Store: store, Now: func() time.Time { return now }}
	day := 24 * time.Hour
	ingest.Accept(ctx, []domain.Run{
		report("m1", "r1", "build", "succeeded", now.Add(-2*day), 1),
		report("m1", "r2", "build", "failed", now.Add(-2*day+time.Hour), 1),
		report("m1", "r3", "build", "succeeded", now.Add(-time.Hour), 1),
		report("m2", "r4", "deploy", "waiting_human", now.Add(-time.Hour), 1),
		report("m2", "r5", "deploy", "running", now.Add(-time.Minute), 1),
		report("m2", "old", "deploy", "failed", now.Add(-30*day), 1),
	})
	overview, err := app.Query{Store: store, Now: func() time.Time { return now }}.Overview(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	counts := overview.Counts
	if counts.Total != 5 || counts.Succeeded != 2 || counts.Failed != 1 || counts.Waiting != 1 || counts.Active != 1 {
		t.Fatalf("counts = %+v", counts)
	}
	if rate := counts.SuccessRate(); rate < 0.66 || rate > 0.67 {
		t.Fatalf("success rate = %v", rate)
	}
	if len(overview.Daily) != 7 || overview.Daily[4].Counts.Total != 2 || overview.Daily[6].Counts.Total != 3 {
		t.Fatalf("daily = %+v", overview.Daily)
	}
	if len(overview.Failures) != 1 || len(overview.Waiting) != 1 || len(overview.Running) != 1 || overview.Machines != 2 {
		t.Fatalf("lists: failures %d waiting %d running %d machines %d", len(overview.Failures), len(overview.Waiting), len(overview.Running), overview.Machines)
	}
	if len(overview.Workflows) != 2 {
		t.Fatalf("workflows = %+v", overview.Workflows)
	}
	build := overview.Workflows[1]
	if overview.Workflows[0].Name != "deploy" || build.Name != "build" || build.Counts.Total != 3 || build.LastStatus != domain.StatusSucceeded {
		t.Fatalf("workflow stats = %+v", overview.Workflows)
	}
	want := []domain.Status{domain.StatusSucceeded, domain.StatusFailed, domain.StatusSucceeded}
	for index, status := range want {
		if build.Recent[index] != status {
			t.Fatalf("recent = %v, want oldest first %v", build.Recent, want)
		}
	}
	if build.P50Ms != 10000 || build.P95Ms != 10000 {
		t.Fatalf("percentiles = %d/%d", build.P50Ms, build.P95Ms)
	}
}

func TestRunsClampsLimit(t *testing.T) {
	store := memory.New()
	for index := 0; index < 60; index++ {
		store.Save(context.Background(), domain.Run{Key: domain.RunKey{Machine: "m", RunID: string(rune('A' + index))}, Workflow: "wf", Status: domain.StatusSucceeded, Seq: 1})
	}
	runs, total, _ := app.Query{Store: store}.Runs(context.Background(), app.RunFilter{Limit: 1000})
	if len(runs) != 50 || total != 60 {
		t.Fatalf("got %d of %d", len(runs), total)
	}
}

func TestGraphsAreStoredPerWorkflowVersion(t *testing.T) {
	ctx := context.Background()
	store := memory.New()
	first := report("m1", "r1", "wf", "succeeded", now, 1)
	first.Digest = "v1"
	first.Graph = &domain.Graph{Entry: "a", Nodes: []domain.GraphNode{{ID: "a"}}}
	second := report("m1", "r2", "wf", "running", now, 1)
	second.Digest = "v1" // same version, no graph sent again
	other := report("m1", "r3", "wf", "running", now, 1)
	other.Digest = "v2"
	withoutGraph := first
	withoutGraph.Graph = nil
	app.Ingest{Store: store}.Accept(ctx, []domain.Run{withoutGraph})         // an older runner reported r1 first
	app.Ingest{Store: store}.Accept(ctx, []domain.Run{first, second, other}) // backfill: r1 is stale, its graph still lands
	query := app.Query{Store: store}
	for id, want := range map[string]bool{"r1": true, "r2": true, "r3": false} {
		run, err := query.Run(ctx, domain.RunKey{Machine: "m1", RunID: id})
		if err != nil || (run.Graph != nil) != want {
			t.Fatalf("%s: graph %v, want %v (%v)", id, run.Graph != nil, want, err)
		}
	}
}
