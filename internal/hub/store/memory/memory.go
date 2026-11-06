// Package memory is an in-process Hub store for tests and throwaway hubs.
package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/app"
	"github.com/chenhg5/agentflow/internal/hub/domain"
)

// Store keeps everything in maps guarded by one lock.
type Store struct {
	mu       sync.RWMutex
	runs     map[domain.RunKey]domain.Run
	machines map[string]domain.Machine
	graphs   map[string]domain.Graph
}

var _ app.Store = (*Store)(nil)

// New returns an empty store.
func New() *Store {
	return &Store{runs: map[domain.RunKey]domain.Run{}, machines: map[string]domain.Machine{}, graphs: map[string]domain.Graph{}}
}

func (s *Store) Save(_ context.Context, run domain.Run) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.runs[run.Key]; ok && !run.Supersedes(old) {
		return false, nil
	}
	s.runs[run.Key] = clone(run)
	return true, nil
}

func (s *Store) Get(_ context.Context, key domain.RunKey) (domain.Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.runs[key]
	if !ok {
		return domain.Run{}, app.ErrNotFound
	}
	return clone(run), nil
}

func (s *Store) List(_ context.Context, filter app.RunFilter) ([]domain.Run, int, error) {
	s.mu.RLock()
	matches := make([]domain.Run, 0)
	for _, run := range s.runs {
		if Matches(run, filter) {
			matches = append(matches, run)
		}
	}
	s.mu.RUnlock()
	sort.Slice(matches, func(i, j int) bool { return Newer(matches[i], matches[j]) })
	total := len(matches)
	matches = page(matches, filter.Offset, filter.Limit)
	out := make([]domain.Run, len(matches))
	for index, run := range matches {
		out[index] = clone(run)
		if !filter.WithSteps {
			out[index].Steps = nil
		}
	}
	return out, total, nil
}

func (s *Store) GetMachine(_ context.Context, id string) (domain.Machine, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	machine, ok := s.machines[id]
	return machine, ok, nil
}

func (s *Store) SaveMachine(_ context.Context, machine domain.Machine) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.machines[machine.ID] = machine
	return nil
}

func (s *Store) Machines(_ context.Context) ([]domain.Machine, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Machine, 0, len(s.machines))
	for _, machine := range s.machines {
		out = append(out, machine)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) SaveGraph(_ context.Context, key string, graph domain.Graph) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.graphs[key] = graph
	return nil
}

func (s *Store) Graph(_ context.Context, key string) (domain.Graph, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	graph, ok := s.graphs[key]
	return graph, ok, nil
}

func (s *Store) DeleteFinishedBefore(_ context.Context, cutoff time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for key, run := range s.runs {
		if run.Status.Finished() && run.When().Before(cutoff) {
			delete(s.runs, key)
			removed++
		}
	}
	used := map[string]bool{}
	for _, run := range s.runs {
		used[domain.GraphKey(run)] = true
	}
	for key := range s.graphs {
		if !used[key] {
			delete(s.graphs, key)
		}
	}
	return removed, nil
}

func (s *Store) Close() error { return nil }

// Matches reports whether run passes filter.
func Matches(run domain.Run, filter app.RunFilter) bool {
	if filter.Workflow != "" && run.Workflow != filter.Workflow {
		return false
	}
	if filter.Machine != "" && run.Key.Machine != filter.Machine {
		return false
	}
	if filter.Project != "" && run.Project != filter.Project {
		return false
	}
	if len(filter.Statuses) > 0 {
		found := false
		for _, status := range filter.Statuses {
			found = found || run.Status == status
		}
		if !found {
			return false
		}
	}
	when := run.When()
	if !filter.Since.IsZero() && when.Before(filter.Since) {
		return false
	}
	if !filter.Until.IsZero() && !when.Before(filter.Until) {
		return false
	}
	return true
}

// Newer orders runs newest first, then by key for a stable order.
func Newer(a, b domain.Run) bool {
	if !a.When().Equal(b.When()) {
		return a.When().After(b.When())
	}
	return a.Key.String() > b.Key.String()
}

func page(runs []domain.Run, offset, limit int) []domain.Run {
	if offset > len(runs) {
		return nil
	}
	runs = runs[offset:]
	if limit > 0 && limit < len(runs) {
		runs = runs[:limit]
	}
	return runs
}

func clone(run domain.Run) domain.Run {
	if run.Inputs != nil {
		inputs := make(map[string]string, len(run.Inputs))
		for key, value := range run.Inputs {
			inputs[key] = value
		}
		run.Inputs = inputs
	}
	run.Steps = append([]domain.Step(nil), run.Steps...)
	run.Graph = nil // graphs live in their own map
	return run
}
