// Package domain holds the Hub's model of what happened: runs reported by
// runners, the steps inside them, and the machines they ran on. It has no
// knowledge of HTTP, storage or the reporting wire format.
package domain

import (
	"errors"
	"strings"
	"time"
)

// Status of a run or a step.
type Status string

const (
	StatusPending Status = "pending"
	StatusQueued  Status = "queued"
	StatusRunning Status = "running"
	StatusWaiting Status = "waiting_human"
	// StatusWaitingInput is the v0.4 engine's wait for an external submission
	// (a person through a card or form, or a program).
	StatusWaitingInput Status = "waiting_input"
	// StatusWaitingResource waits for a shared resource; nothing is needed from a person.
	StatusWaitingResource Status = "waiting_resource"
	StatusSucceeded       Status = "succeeded"
	StatusFailed          Status = "failed"
	StatusCancelled       Status = "cancelled"
	StatusSkipped         Status = "skipped"
)

var knownStatuses = map[Status]bool{StatusPending: true, StatusQueued: true, StatusRunning: true, StatusWaiting: true,
	StatusWaitingInput: true, StatusWaitingResource: true, StatusSucceeded: true, StatusFailed: true, StatusCancelled: true, StatusSkipped: true}

// Known reports whether s is a status the Hub understands.
func (s Status) Known() bool { return knownStatuses[s] }

// Finished reports whether a run in this status will not change by itself.
func (s Status) Finished() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCancelled
}

// Active reports whether a run in this status is still going without anyone's help.
func (s Status) Active() bool {
	return s == StatusPending || s == StatusQueued || s == StatusRunning || s == StatusWaitingResource
}

// NeedsInput reports whether a run in this status waits for someone to act.
func (s Status) NeedsInput() bool { return s == StatusWaiting || s == StatusWaitingInput }

// WaitingStatuses are the statuses that wait for someone to act.
var WaitingStatuses = []Status{StatusWaiting, StatusWaitingInput}

// RunKey identifies a run across machines: run ids are only unique per machine.
type RunKey struct {
	Machine string
	RunID   string
}

func (k RunKey) String() string { return k.Machine + "/" + k.RunID }

// Run is the latest known state of one workflow run.
type Run struct {
	Key           RunKey
	Hostname      string
	Project       string
	Path          string
	Version       string
	Orchestration string
	Workflow      string
	Digest        string
	Status        Status
	Seq           int64
	CreatedAt     time.Time
	StartedAt     time.Time
	FinishedAt    time.Time
	UpdatedAt     time.Time
	ReceivedAt    time.Time
	DurationMs    int64
	WaitingMs     int64
	TriggerKind   string
	TriggerID     string
	Inputs        map[string]string
	Error         string
	Steps         []Step
	// Graph is stored once per workflow version (see GraphKey), not per run.
	Graph *Graph
}

// Step is one node of a JSON workflow or one step of a code workflow.
type Step struct {
	ID          string
	Type        string
	Provider    string
	Status      Status
	SkipReason  string
	Attempts    int
	StartedAt   time.Time
	FinishedAt  time.Time
	DurationMs  int64
	WaitingMs   int64
	Error       string
	Summary     string
	Reason      string
	OutputBytes int64
	LogBytes    int64
}

// ErrInvalid marks a report the Hub refuses to store.
var ErrInvalid = errors.New("invalid run")

// Validate checks the invariants every stored run must hold.
func (r Run) Validate() error {
	switch {
	case strings.TrimSpace(r.Key.Machine) == "":
		return errors.Join(ErrInvalid, errors.New("machine is required"))
	case strings.TrimSpace(r.Key.RunID) == "":
		return errors.Join(ErrInvalid, errors.New("run id is required"))
	case strings.TrimSpace(r.Workflow) == "":
		return errors.Join(ErrInvalid, errors.New("workflow is required"))
	case !r.Status.Known():
		return errors.Join(ErrInvalid, errors.New("unknown status "+string(r.Status)))
	case r.Seq <= 0:
		return errors.Join(ErrInvalid, errors.New("seq must be positive"))
	}
	return nil
}

// Supersedes reports whether r is a newer report of the same run than old.
func (r Run) Supersedes(old Run) bool { return r.Seq > old.Seq }

// When is the time used to place a run on a timeline.
func (r Run) When() time.Time {
	if !r.StartedAt.IsZero() {
		return r.StartedAt
	}
	if !r.CreatedAt.IsZero() {
		return r.CreatedAt
	}
	return r.ReceivedAt
}

// FailedStep is the first failed step, if any.
func (r Run) FailedStep() (Step, bool) {
	for _, step := range r.Steps {
		if step.Status == StatusFailed {
			return step, true
		}
	}
	return Step{}, false
}

// WaitingStep is the step waiting for a person, if any.
func (r Run) WaitingStep() (Step, bool) {
	for _, step := range r.Steps {
		if step.Status.NeedsInput() {
			return step, true
		}
	}
	return Step{}, false
}
