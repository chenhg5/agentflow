package domain

import (
	"errors"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	good := Run{Key: RunKey{Machine: "m", RunID: "r"}, Workflow: "wf", Status: StatusRunning, Seq: 1}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Run){
		"machine":  func(r *Run) { r.Key.Machine = " " },
		"run id":   func(r *Run) { r.Key.RunID = "" },
		"workflow": func(r *Run) { r.Workflow = "" },
		"status":   func(r *Run) { r.Status = "done" },
		"seq":      func(r *Run) { r.Seq = 0 },
	} {
		run := good
		mutate(&run)
		if err := run.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestWhenFallsBack(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := (Run{ReceivedAt: at}).When(); !got.Equal(at) {
		t.Fatal("When should fall back to ReceivedAt")
	}
	if got := (Run{CreatedAt: at, ReceivedAt: at.Add(time.Hour)}).When(); !got.Equal(at) {
		t.Fatal("When should prefer CreatedAt over ReceivedAt")
	}
}

func TestMachineSeenKeepsLatest(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	machine := Machine{ID: "m"}.Seen("h1", "v1", []string{"submit"}, at.Add(time.Hour))
	machine = machine.Seen("h0", "v0", nil, at) // an older report arriving late
	if !machine.Accepts("submit") || machine.Version != "v1" || machine.Hostname != "h1" || !machine.FirstSeen.Equal(at) || !machine.LastSeen.Equal(at.Add(time.Hour)) {
		t.Fatalf("machine = %+v", machine)
	}
}

func TestNewSubmission(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	run := Run{Key: RunKey{Machine: "m", RunID: "r"}, Status: StatusWaitingInput, Steps: []Step{
		{ID: "build", Status: StatusSucceeded}, {ID: "approve", Status: StatusWaitingInput, StartedAt: at},
	}}
	accepting := Machine{ID: "m", Capabilities: []string{CapabilitySubmit}}
	command, err := NewSubmission("c1", run, accepting, "approve", nil, "glenn", at)
	if err != nil || command.Status != CommandPending || !command.StepStartedAt.Equal(at) || command.Output == nil || command.By != "glenn" {
		t.Fatalf("command = %+v, %v", command, err)
	}
	if _, err := NewSubmission("c2", run, Machine{ID: "m"}, "approve", nil, "", at); !errors.Is(err, ErrNoCapability) {
		t.Fatalf("a machine that did not opt in: %v", err)
	}
	if _, err := NewSubmission("c3", run, accepting, "build", nil, "", at); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("a step that is not waiting: %v", err)
	}
	finished := run
	finished.Status = StatusCancelled
	if _, err := NewSubmission("c4", finished, accepting, "approve", nil, "", at); !errors.Is(err, ErrNotWaiting) {
		t.Fatalf("a finished run: %v", err)
	}
}

func TestKeepTails(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	old := Run{Steps: []Step{{ID: "a", StartedAt: at, Attempts: 1, StderrTail: "boom", Result: "{}"}, {ID: "b", StartedAt: at, Attempts: 1, StdoutTail: "old visit"}}}
	newer := Run{Steps: []Step{{ID: "a", StartedAt: at, Attempts: 1}, {ID: "b", StartedAt: at.Add(time.Minute), Attempts: 1}}}
	newer.KeepTails(old)
	if newer.Steps[0].StderrTail != "boom" || newer.Steps[0].Result != "{}" {
		t.Fatal("same visit keeps its tail")
	}
	if newer.Steps[1].StdoutTail != "" {
		t.Fatal("a new visit of a step must not show the previous visit's logs")
	}
}
