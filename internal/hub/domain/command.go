package domain

import (
	"errors"
	"time"
)

// CommandStatus tracks a command from the Hub to a runner.
type CommandStatus string

const (
	CommandPending  CommandStatus = "pending"  // waiting for the runner to pull it
	CommandApplied  CommandStatus = "applied"  // the runner accepted it
	CommandRejected CommandStatus = "rejected" // the runner refused it (Error says why)
)

// Command asks a runner to act on one of its runs. Today the only kind is a
// submission to a node waiting for input.
type Command struct {
	ID            string
	Kind          string
	Run           RunKey
	NodeID        string
	Output        map[string]any
	StepStartedAt time.Time
	By            string
	Status        CommandStatus
	Error         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ErrNotWaiting is returned for a submission to a step that waits for nothing.
var ErrNotWaiting = errors.New("the step is not waiting for input")

// ErrNoCapability is returned when the run's machine does not accept commands.
var ErrNoCapability = errors.New("this machine does not accept input from the Hub")

// NewSubmission validates a submission against what the Hub knows of the run.
// The runner checks again against its own state before acting.
func NewSubmission(id string, run Run, machine Machine, nodeID string, output map[string]any, by string, at time.Time) (Command, error) {
	if !machine.Accepts(CapabilitySubmit) {
		return Command{}, ErrNoCapability
	}
	var step *Step
	for index := range run.Steps {
		if run.Steps[index].ID == nodeID {
			step = &run.Steps[index]
		}
	}
	if step == nil || !step.Status.NeedsInput() || run.Status.Finished() {
		return Command{}, ErrNotWaiting
	}
	if output == nil {
		output = map[string]any{}
	}
	return Command{ID: id, Kind: CommandSubmit, Run: run.Key, NodeID: nodeID, Output: output, StepStartedAt: step.StartedAt,
		By: by, Status: CommandPending, CreatedAt: at, UpdatedAt: at}, nil
}

// CommandSubmit is the only command kind.
const CommandSubmit = "submit"

// CapabilitySubmit marks a machine that pulls and applies submissions.
const CapabilitySubmit = "submit"
