package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/domain"
)

// Commands lets people act on runs from the Hub. Runners pull commands, so the
// Hub never connects to them; a command is pending until its runner answers.
type Commands struct {
	Store Store
	Now   func() time.Time
}

// ErrCommandDone is returned when a runner answers a command twice.
var ErrCommandDone = errors.New("command already answered")

// Submit records input for a step waiting on run.
func (c Commands) Submit(ctx context.Context, key domain.RunKey, nodeID string, output map[string]any, by string) (domain.Command, error) {
	run, err := c.Store.Get(ctx, key)
	if err != nil {
		return domain.Command{}, err
	}
	machine, _, err := c.Store.GetMachine(ctx, key.Machine)
	if err != nil {
		return domain.Command{}, err
	}
	command, err := domain.NewSubmission(newID(), run, machine, nodeID, output, by, c.now())
	if err != nil {
		return domain.Command{}, err
	}
	return command, c.Store.SaveCommand(ctx, command)
}

// Pending is what a runner pulls.
func (c Commands) Pending(ctx context.Context, machine string) ([]domain.Command, error) {
	return c.Store.PendingCommands(ctx, machine)
}

// Answer records the runner's outcome. Only the machine a command was for
// may answer it.
func (c Commands) Answer(ctx context.Context, id, machine string, status domain.CommandStatus, message string) error {
	command, found, err := c.Store.Command(ctx, id)
	if err != nil {
		return err
	}
	if !found || command.Run.Machine != machine {
		return ErrNotFound
	}
	if command.Status != domain.CommandPending {
		return ErrCommandDone
	}
	if status != domain.CommandApplied && status != domain.CommandRejected {
		return errors.New("status must be applied or rejected")
	}
	command.Status, command.Error, command.UpdatedAt = status, message, c.now()
	return c.Store.SaveCommand(ctx, command)
}

// ForRun lists a run's commands for its page.
func (c Commands) ForRun(ctx context.Context, key domain.RunKey) ([]domain.Command, error) {
	return c.Store.RunCommands(ctx, key)
}

func (c Commands) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func newID() string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return "cmd_" + hex.EncodeToString(buf[:])
}
