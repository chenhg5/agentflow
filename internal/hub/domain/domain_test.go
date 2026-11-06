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
	machine := Machine{ID: "m"}.Seen("h1", "v1", at.Add(time.Hour))
	machine = machine.Seen("h0", "v0", at) // an older report arriving late
	if machine.Version != "v1" || machine.Hostname != "h1" || !machine.FirstSeen.Equal(at) || !machine.LastSeen.Equal(at.Add(time.Hour)) {
		t.Fatalf("machine = %+v", machine)
	}
}
