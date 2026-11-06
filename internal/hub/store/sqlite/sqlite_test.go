package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/app"
	"github.com/chenhg5/agentflow/internal/hub/domain"
	"github.com/chenhg5/agentflow/internal/hub/store/sqlite"
	"github.com/chenhg5/agentflow/internal/hub/store/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(t *testing.T) app.Store {
		store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "hub.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		return store
	})
}

func TestReopenKeepsData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "hub.db")
	store, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	run := storetest.NewRun("m1", "r1", "wf", domain.StatusSucceeded, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), 1)
	if _, err := store.Save(ctx, run); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = sqlite.Open(ctx, path) // migrations must be idempotent
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Get(ctx, run.Key); err != nil {
		t.Fatalf("run lost after reopen: %v", err)
	}
}
