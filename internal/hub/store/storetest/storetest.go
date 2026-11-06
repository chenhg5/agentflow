// Package storetest is the contract every Hub store must meet. Each store's
// tests run it, so a new database backend proves itself by passing it.
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/app"
	"github.com/chenhg5/agentflow/internal/hub/domain"
)

var base = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

// NewRun builds a valid run for tests.
func NewRun(machine, id, workflow string, status domain.Status, started time.Time, seq int64) domain.Run {
	return domain.Run{
		Key: domain.RunKey{Machine: machine, RunID: id}, Workflow: workflow, Status: status, Seq: seq,
		Orchestration: "json", Project: "demo", StartedAt: started, CreatedAt: started, UpdatedAt: started.Add(time.Minute),
		DurationMs: 60000, Inputs: map[string]string{"issue": "ABC-1"},
		Steps: []domain.Step{{ID: "build", Type: "shell", Status: status, Attempts: 1, StartedAt: started, FinishedAt: started.Add(time.Minute), DurationMs: 60000, Summary: "built"}},
	}
}

// Run checks a store against the contract. open must return an empty store.
func Run(t *testing.T, open func(t *testing.T) app.Store) {
	ctx := context.Background()

	t.Run("save keeps the newest report", func(t *testing.T) {
		store := open(t)
		first := NewRun("m1", "r1", "wf", domain.StatusRunning, base, 10)
		if saved, err := store.Save(ctx, first); err != nil || !saved {
			t.Fatalf("first save = %v, %v", saved, err)
		}
		newer := first
		newer.Status, newer.Seq = domain.StatusSucceeded, 20
		if saved, err := store.Save(ctx, newer); err != nil || !saved {
			t.Fatalf("newer save = %v, %v", saved, err)
		}
		older := first
		older.Seq = 15
		if saved, err := store.Save(ctx, older); err != nil || saved {
			t.Fatalf("stale save = %v, %v; want ignored", saved, err)
		}
		if saved, _ := store.Save(ctx, newer); saved {
			t.Fatal("re-sending the same seq must be ignored")
		}
		got, err := store.Get(ctx, first.Key)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != domain.StatusSucceeded || got.Seq != 20 {
			t.Fatalf("got %s seq %d", got.Status, got.Seq)
		}
	})

	t.Run("get round-trips every field", func(t *testing.T) {
		store := open(t)
		run := NewRun("m1", "r1", "wf", domain.StatusFailed, base, 5)
		run.Error, run.TriggerKind, run.TriggerID, run.WaitingMs = "build: boom", "cron", "nightly", 1500
		run.FinishedAt, run.ReceivedAt = base.Add(time.Minute), base.Add(2*time.Minute)
		run.Steps[0].Error, run.Steps[0].Reason = "boom", "because"
		if _, err := store.Save(ctx, run); err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(ctx, run.Key)
		if err != nil {
			t.Fatal(err)
		}
		if got.Error != run.Error || got.TriggerKind != "cron" || got.TriggerID != "nightly" || got.WaitingMs != 1500 || got.Project != "demo" {
			t.Fatalf("fields lost: %+v", got)
		}
		if !got.StartedAt.Equal(base) || !got.FinishedAt.Equal(run.FinishedAt) || !got.ReceivedAt.Equal(run.ReceivedAt) {
			t.Fatalf("times lost: %v %v %v", got.StartedAt, got.FinishedAt, got.ReceivedAt)
		}
		if got.Inputs["issue"] != "ABC-1" || len(got.Steps) != 1 || got.Steps[0].Reason != "because" || got.Steps[0].Summary != "built" || !got.Steps[0].FinishedAt.Equal(base.Add(time.Minute)) {
			t.Fatalf("inputs or steps lost: %+v", got)
		}
	})

	t.Run("get reports a missing run", func(t *testing.T) {
		store := open(t)
		if _, err := store.Get(ctx, domain.RunKey{Machine: "m", RunID: "nope"}); !errors.Is(err, app.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("runs are per machine", func(t *testing.T) {
		store := open(t)
		store.Save(ctx, NewRun("m1", "same", "wf", domain.StatusSucceeded, base, 1))
		store.Save(ctx, NewRun("m2", "same", "wf", domain.StatusFailed, base, 1))
		_, total, err := store.List(ctx, app.RunFilter{})
		if err != nil || total != 2 {
			t.Fatalf("total = %d, %v", total, err)
		}
	})

	t.Run("list filters, orders and pages", func(t *testing.T) {
		store := open(t)
		for index, item := range []struct {
			machine, workflow string
			status            domain.Status
		}{{"m1", "a", domain.StatusSucceeded}, {"m1", "a", domain.StatusFailed}, {"m2", "b", domain.StatusSucceeded}, {"m2", "a", domain.StatusWaiting}, {"m1", "b", domain.StatusFailed}} {
			run := NewRun(item.machine, "r"+string(rune('0'+index)), item.workflow, item.status, base.Add(time.Duration(index)*time.Hour), 1)
			if _, err := store.Save(ctx, run); err != nil {
				t.Fatal(err)
			}
		}
		runs, total, err := store.List(ctx, app.RunFilter{})
		if err != nil || total != 5 || len(runs) != 5 {
			t.Fatalf("all: %d/%d %v", len(runs), total, err)
		}
		if runs[0].Key.RunID != "r4" || runs[4].Key.RunID != "r0" {
			t.Fatalf("want newest first, got %s … %s", runs[0].Key.RunID, runs[4].Key.RunID)
		}
		if len(runs[0].Steps) != 0 {
			t.Fatal("listings leave steps out unless asked")
		}
		withSteps, _, _ := store.List(ctx, app.RunFilter{Limit: 1, WithSteps: true})
		if len(withSteps) != 1 || len(withSteps[0].Steps) != 1 {
			t.Fatal("WithSteps must load steps")
		}
		check := func(name string, filter app.RunFilter, want ...string) {
			t.Helper()
			runs, total, err := store.List(ctx, filter)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, run := range runs {
				got = append(got, run.Key.RunID)
			}
			if len(got) != len(want) {
				t.Fatalf("%s: got %v (total %d), want %v", name, got, total, want)
			}
			for index := range want {
				if got[index] != want[index] {
					t.Fatalf("%s: got %v, want %v", name, got, want)
				}
			}
		}
		check("workflow", app.RunFilter{Workflow: "a"}, "r3", "r1", "r0")
		check("machine", app.RunFilter{Machine: "m2"}, "r3", "r2")
		check("statuses", app.RunFilter{Statuses: []domain.Status{domain.StatusFailed, domain.StatusWaiting}}, "r4", "r3", "r1")
		check("since/until", app.RunFilter{Since: base.Add(time.Hour), Until: base.Add(3 * time.Hour)}, "r2", "r1")
		check("page", app.RunFilter{Limit: 2, Offset: 1}, "r3", "r2")
		_, total, _ = store.List(ctx, app.RunFilter{Limit: 2, Offset: 1})
		if total != 5 {
			t.Fatalf("total ignores paging: got %d", total)
		}
		check("past the end", app.RunFilter{Offset: 10})
	})

	t.Run("graphs", func(t *testing.T) {
		store := open(t)
		if _, found, err := store.Graph(ctx, "wf@x"); err != nil || found {
			t.Fatalf("empty store found a graph: %v %v", found, err)
		}
		graph := domain.Graph{Entry: "a", Nodes: []domain.GraphNode{{ID: "a", Executors: []string{"codex", "process"}, Parallel: true, Join: []string{"z"},
			OutputSchema: map[string]any{"type": "object"}}},
			Edges: []domain.GraphEdge{{Source: "a", Target: "$end", Condition: "ok", MaxIterations: 2, Parallel: true}}}
		if err := store.SaveGraph(ctx, "wf@x", graph); err != nil {
			t.Fatal(err)
		}
		graph.Entry = "b"
		store.SaveGraph(ctx, "wf@x", graph)
		got, found, err := store.Graph(ctx, "wf@x")
		if err != nil || !found || got.Entry != "b" || len(got.Nodes) != 1 || got.Nodes[0].Executors[1] != "process" || !got.Nodes[0].Parallel ||
			got.Nodes[0].Join[0] != "z" || got.Nodes[0].OutputSchema["type"] != "object" || got.Edges[0].Condition != "ok" || got.Edges[0].MaxIterations != 2 || !got.Edges[0].Parallel {
			t.Fatalf("graph = %+v %v %v", got, found, err)
		}
		if run, _ := store.Get(ctx, domain.RunKey{Machine: "m", RunID: "nope"}); run.Graph != nil {
			t.Fatal("runs do not carry graphs")
		}
	})

	t.Run("commands", func(t *testing.T) {
		store := open(t)
		run := domain.RunKey{Machine: "m1", RunID: "r1"}
		older := domain.Command{ID: "c1", Kind: domain.CommandSubmit, Run: run, NodeID: "approve", Output: map[string]any{"decision": "approve", "n": 2.0},
			StepStartedAt: base, By: "glenn", Status: domain.CommandPending, CreatedAt: base, UpdatedAt: base}
		newer := older
		newer.ID, newer.CreatedAt = "c2", base.Add(time.Minute)
		elsewhere := older
		elsewhere.ID, elsewhere.Run = "c3", domain.RunKey{Machine: "m2", RunID: "r9"}
		for _, command := range []domain.Command{newer, older, elsewhere} {
			if err := store.SaveCommand(ctx, command); err != nil {
				t.Fatal(err)
			}
		}
		pending, err := store.PendingCommands(ctx, "m1")
		if err != nil || len(pending) != 2 || pending[0].ID != "c1" || pending[0].Output["decision"] != "approve" || !pending[0].StepStartedAt.Equal(base) {
			t.Fatalf("pending = %+v %v", pending, err)
		}
		older.Status, older.Error, older.UpdatedAt = domain.CommandApplied, "", base.Add(time.Hour)
		store.SaveCommand(ctx, older)
		got, found, _ := store.Command(ctx, "c1")
		if !found || got.Status != domain.CommandApplied || got.By != "glenn" {
			t.Fatalf("command = %+v", got)
		}
		history, _ := store.RunCommands(ctx, run)
		if len(history) != 2 || history[0].ID != "c2" {
			t.Fatalf("history newest first = %+v", history)
		}
		if _, found, _ := store.Command(ctx, "nope"); found {
			t.Fatal("unknown command found")
		}
	})

	t.Run("machines", func(t *testing.T) {
		store := open(t)
		if _, found, err := store.GetMachine(ctx, "m1"); err != nil || found {
			t.Fatalf("empty store found machine: %v %v", found, err)
		}
		machine := domain.Machine{ID: "m1", Hostname: "h", Version: "v1", FirstSeen: base, LastSeen: base.Add(time.Hour), Capabilities: []string{"submit"}}
		if err := store.SaveMachine(ctx, machine); err != nil {
			t.Fatal(err)
		}
		machine.Version = "v2"
		store.SaveMachine(ctx, machine)
		store.SaveMachine(ctx, domain.Machine{ID: "m0", FirstSeen: base, LastSeen: base})
		got, found, err := store.GetMachine(ctx, "m1")
		if err != nil || !found || got.Version != "v2" || !got.Accepts("submit") || !got.LastSeen.Equal(base.Add(time.Hour)) {
			t.Fatalf("got %+v %v %v", got, found, err)
		}
		all, err := store.Machines(ctx)
		if err != nil || len(all) != 2 || all[0].ID != "m0" {
			t.Fatalf("machines = %+v, %v", all, err)
		}
	})
}
