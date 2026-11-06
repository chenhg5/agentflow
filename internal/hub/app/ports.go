// Package app holds the Hub's use cases: ingesting reports and answering the
// questions the pages and the API ask. Storage is reached through the ports in
// this file, so any database can back the Hub.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/domain"
)

// ErrNotFound is returned by repositories for a missing run.
var ErrNotFound = errors.New("not found")

// RunFilter narrows a run listing. Zero values mean "any".
type RunFilter struct {
	Workflow string
	Machine  string
	Project  string
	Statuses []domain.Status
	Since    time.Time
	Until    time.Time
	Limit    int
	Offset   int
	// WithSteps loads step details; listings leave them out.
	WithSteps bool
}

// RunRepository stores runs.
type RunRepository interface {
	// Save stores run when it is newer than the stored one and reports whether it did.
	Save(ctx context.Context, run domain.Run) (bool, error)
	Get(ctx context.Context, key domain.RunKey) (domain.Run, error)
	// List returns matching runs, newest first, and the total number of matches.
	List(ctx context.Context, filter RunFilter) ([]domain.Run, int, error)
}

// MachineRepository stores machines.
type MachineRepository interface {
	GetMachine(ctx context.Context, id string) (domain.Machine, bool, error)
	SaveMachine(ctx context.Context, machine domain.Machine) error
	Machines(ctx context.Context) ([]domain.Machine, error)
}

// GraphRepository stores workflow graphs by GraphKey.
type GraphRepository interface {
	SaveGraph(ctx context.Context, key string, graph domain.Graph) error
	Graph(ctx context.Context, key string) (domain.Graph, bool, error)
}

// Store is everything the Hub persists.
type Store interface {
	RunRepository
	MachineRepository
	GraphRepository
	Close() error
}
