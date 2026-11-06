package app

import (
	"context"
	"fmt"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/domain"
)

// IngestResult says what happened to a batch of runs.
type IngestResult struct {
	Accepted int
	Stale    int
	Rejected []string
}

// Ingest stores reported runs. A run older than the stored one is counted as
// stale and ignored; an invalid run is rejected without stopping the batch.
// It knows nothing of the wire protocol: the API translates reports first.
type Ingest struct {
	Store Store
	Now   func() time.Time
}

// Accept stores a batch of runs, stamping each with the time it was received.
func (s Ingest) Accept(ctx context.Context, runs []domain.Run) (IngestResult, error) {
	var result IngestResult
	now := s.now()
	for _, run := range runs {
		run.ReceivedAt = now
		if err := run.Validate(); err != nil {
			result.Rejected = append(result.Rejected, fmt.Sprintf("%s: %v", run.Key.RunID, err))
			continue
		}
		saved, err := s.Store.Save(ctx, run)
		if err != nil {
			return result, err
		}
		// The graph is saved even from a stale snapshot, so backfilling runs
		// the Hub already has (from an older runner) adds their canvas.
		if run.Graph != nil {
			if err := s.Store.SaveGraph(ctx, domain.GraphKey(run), *run.Graph); err != nil {
				return result, err
			}
		}
		if !saved {
			result.Stale++
			continue
		}
		result.Accepted++
		if err := s.seeMachine(ctx, run, now); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (s Ingest) seeMachine(ctx context.Context, run domain.Run, at time.Time) error {
	machine, _, err := s.Store.GetMachine(ctx, run.Key.Machine)
	if err != nil {
		return err
	}
	machine.ID = run.Key.Machine
	return s.Store.SaveMachine(ctx, machine.Seen(run.Hostname, run.Version, at))
}

func (s Ingest) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
