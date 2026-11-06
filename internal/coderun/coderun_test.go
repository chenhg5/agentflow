package coderun

import (
	"errors"
	"sort"
	"sync"
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func edges(run *model.Run) []string {
	var out []string
	for _, edge := range run.Workflow.Edges {
		out = append(out, edge.From+">"+edge.To)
	}
	sort.Strings(out)
	return out
}

func step(t *testing.T, root, runID, id string, status string, result map[string]any) {
	t.Helper()
	if _, err := Update(root, runID, func(run *model.Run) error { return Begin(run, id, Executor) }); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(root, runID, func(run *model.Run) error { return Finish(run, id, status, result, "", nil) }); err != nil {
		t.Fatal(err)
	}
}

func TestStepsBuildTheRunAndItsGraph(t *testing.T) {
	root := t.TempDir()
	run, err := Start(root, model.Workflow{Name: "nightly"}, map[string]string{"repo": "x"})
	if err != nil {
		t.Fatal(err)
	}
	step(t, root, run.RunID, "fetch", "succeeded", map[string]any{"count": 2.0})
	step(t, root, run.RunID, "triage", "succeeded", nil)
	step(t, root, run.RunID, "triage", "succeeded", nil) // a second visit
	// two steps started together, then one that follows both
	for _, id := range []string{"lint", "test"} {
		if _, err := Update(root, run.RunID, func(r *model.Run) error { return Begin(r, id, "sh") }); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"test", "lint"} {
		if _, err := Update(root, run.RunID, func(r *model.Run) error { return Finish(r, id, "succeeded", nil, "", nil) }); err != nil {
			t.Fatal(err)
		}
	}
	step(t, root, run.RunID, "report", "succeeded", nil)
	ended, err := Update(root, run.RunID, func(r *model.Run) error { return End(r, "", "") })
	if err != nil {
		t.Fatal(err)
	}
	loaded, _ := store.LoadRun(root, run.RunID)
	want := []string{"fetch>triage", "lint>report", "report>$end", "test>report", "triage>lint", "triage>test"}
	if got := edges(loaded); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[4] != want[4] || got[5] != want[5] {
		t.Fatalf("edges = %v, want %v", got, want)
	}
	if ended.State.Status != "succeeded" || loaded.Workflow.Entry != "fetch" || loaded.Workflow.Version != Version {
		t.Fatalf("run = %s entry %s version %s", ended.State.Status, loaded.Workflow.Entry, loaded.Workflow.Version)
	}
	triage := loaded.State.Nodes["triage"]
	if triage.Attempts != 2 || loaded.State.Visits["triage"] != 2 || len(loaded.State.History) != 6 {
		t.Fatalf("visits: attempts %d visits %d history %d", triage.Attempts, loaded.State.Visits["triage"], len(loaded.State.History))
	}
	if loaded.State.Nodes["fetch"].Result.(map[string]any)["count"] != 2.0 || loaded.State.DurationMs < 0 {
		t.Fatal("results and timing are kept")
	}
	if _, err := Update(root, run.RunID, func(r *model.Run) error { return Begin(r, "late", Executor) }); !errors.Is(err, ErrFinished) {
		t.Fatalf("a step after the end: %v", err)
	}
}

func TestEndDefaultsAndRunningSteps(t *testing.T) {
	root := t.TempDir()
	run, _ := Start(root, model.Workflow{Name: "wf"}, nil)
	step(t, root, run.RunID, "a", "failed", nil)
	if _, err := Update(root, run.RunID, func(r *model.Run) error { return Begin(r, "b", Executor) }); err != nil {
		t.Fatal(err)
	}
	ended, err := Update(root, run.RunID, func(r *model.Run) error { return End(r, "", "") })
	if err != nil || ended.State.Status != "failed" || ended.State.Nodes["b"].Status != "failed" {
		t.Fatalf("a failed step fails the run and a running one is closed: %s %v", ended.State.Status, err)
	}
	for _, bad := range []string{"", "$end"} {
		other, _ := Start(root, model.Workflow{Name: "wf"}, nil)
		if _, err := Update(root, other.RunID, func(r *model.Run) error { return Begin(r, bad, Executor) }); err == nil {
			t.Errorf("step id %q accepted", bad)
		}
	}
	other, _ := Start(root, model.Workflow{Name: "wf"}, nil)
	if _, err := Update(root, other.RunID, func(r *model.Run) error { return Finish(r, "never", "succeeded", nil, "", nil) }); err == nil {
		t.Error("finishing a step that never began")
	}
	if _, err := Update(root, other.RunID, func(r *model.Run) error { return End(r, "exploded", "") }); err == nil {
		t.Error("unknown end status")
	}
}

func TestParallelStepsDoNotLoseUpdates(t *testing.T) {
	root := t.TempDir()
	run, _ := Start(root, model.Workflow{Name: "wf"}, nil)
	var wg sync.WaitGroup
	for index := 0; index < 12; index++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			step(t, root, run.RunID, id, "succeeded", nil)
		}(string(rune('a' + index)))
	}
	wg.Wait()
	loaded, _ := store.LoadRun(root, run.RunID)
	if len(loaded.State.Nodes) != 12 || len(loaded.Workflow.Nodes) != 12 {
		t.Fatalf("lost updates: %d states, %d nodes", len(loaded.State.Nodes), len(loaded.Workflow.Nodes))
	}
}

func TestWorkflowRunsAreNotCodeRuns(t *testing.T) {
	root := t.TempDir()
	workflow := model.Workflow{Version: "3", Name: "wf", Entry: "a", Nodes: []model.Node{{ID: "a", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "process"}}}}},
		Edges: []model.Transition{{From: "a", To: "$end"}}}
	run, err := store.CreateRun(root, workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Update(root, run.RunID, func(r *model.Run) error { return Begin(r, "x", Executor) }); !errors.Is(err, ErrNotCode) {
		t.Fatalf("code steps on a workflow run: %v", err)
	}
}
