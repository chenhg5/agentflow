// Package queue owns admission for every durable run entry point. Graph and
// executor contracts remain independent of the service and its concurrency.
package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/engine"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

var ErrWorkerBusy = store.ErrWorkspaceBusy
var errManagedAdmission = errors.New("workspace switched to managed admission")

type Worker struct {
	Root        string
	Executors   map[string]driver.Executor
	Concurrency int
	Owner       string
	target      string
	done        chan struct{}
	mu          sync.Mutex
	err         error
}

// Start reserves the single admission owner before recovery or execution.
// managed persists service admission after this process exits.
func Start(ctx context.Context, root, cwd string, concurrency int, managed bool) (*Worker, error) {
	return start(ctx, root, cwd, concurrency, managed, "")
}

func start(ctx context.Context, root, cwd string, concurrency int, managed bool, target string) (*Worker, error) {
	if concurrency < 1 {
		return nil, fmt.Errorf("concurrency must be positive")
	}
	executors, err := driver.LoadExecutors(cwd)
	if err != nil {
		return nil, err
	}
	release, err := store.AcquireWorkspaceLock(ctx, root, "worker.lock", false)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			release()
		}
	}()
	if target != "" {
		managed, err := store.QueueManaged(root)
		if err != nil {
			return nil, err
		}
		if managed {
			return nil, errManagedAdmission
		}
	}
	if managed {
		qrelease, err := store.AcquireWorkspaceLock(ctx, root, "queue.lock", true)
		if err != nil {
			return nil, err
		}
		err = store.ConfigureQueue(root, concurrency)
		qrelease()
		if err != nil {
			return nil, err
		}
	}
	if err := store.RecoverRunCreations(ctx, root); err != nil {
		return nil, err
	}
	owner := fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	if err := store.SaveWorkerInfo(root, store.WorkerInfo{Owner: owner, Concurrency: concurrency, Heartbeat: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		return nil, err
	}
	w := &Worker{Root: root, Executors: executors, Concurrency: concurrency, Owner: owner, target: target, done: make(chan struct{})}
	ok = true
	go func() { defer close(w.done); defer release(); w.loop(ctx) }()
	return w, nil
}

func (w *Worker) Wait() error { <-w.done; w.mu.Lock(); defer w.mu.Unlock(); return w.err }
func (w *Worker) fail(err error) {
	w.mu.Lock()
	if w.err == nil {
		w.err = err
	}
	w.mu.Unlock()
}

func (w *Worker) loop(ctx context.Context) {
	owner := w.Owner
	active := map[string]bool{}
	finished := make(chan string, w.Concurrency)
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	stopping := false
	lastHeartbeat := time.Time{}
	for {
		w.mu.Lock()
		failed := w.err != nil
		w.mu.Unlock()
		if failed {
			stopping = true
		}
		if !stopping {
			if time.Since(lastHeartbeat) >= time.Second {
				if err := store.SaveWorkerInfo(w.Root, store.WorkerInfo{Owner: owner, Concurrency: w.Concurrency, Heartbeat: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
					w.fail(err)
					stopping = true
				}
				lastHeartbeat = time.Now()
			}
			for !stopping && len(active) < w.Concurrency {
				claim, err := store.ClaimDispatch(ctx, w.Root, owner, active, w.target)
				if err != nil {
					if ctx.Err() == nil {
						w.fail(err)
					}
					stopping = true
					break
				}
				if claim == nil {
					break
				}
				active[claim.RunID] = true
				go func(job store.DispatchJob) { w.execute(job); finished <- job.RunID }(*claim)
			}
		}
		if stopping && len(active) == 0 {
			return
		}
		if stopping {
			id := <-finished
			delete(active, id)
			continue
		}
		select {
		case <-ctx.Done():
			stopping = true
		case id := <-finished:
			delete(active, id)
		case <-ticker.C:
		}
	}
}

func (w *Worker) execute(claim store.DispatchJob) {
	run, err := store.LoadRun(w.Root, claim.RunID)
	if err == nil {
		err = engine.Execute(run, w.Executors)
	}
	status, message := "failed", ""
	if err != nil {
		message = err.Error()
	} else {
		status = run.State.Status
	}
	// A live standalone executor may still own a run when adopting a workspace.
	// Never mark that execution failed or acknowledge its queued work.
	if errors.Is(err, store.ErrRunBusy) {
		if _, wakeErr := store.WakeRun(context.Background(), w.Root, claim.RunID, "run_busy"); wakeErr != nil {
			w.fail(wakeErr)
		}
	}
	if err := store.FinishDispatch(context.Background(), w.Root, claim, status, message); err != nil {
		w.fail(err)
	}
}

// Drive preserves the synchronous CLI outside managed workspaces. Concurrent
// callers elect one owner and can take over after it exits; all execute through
// the same queue. Managed workspaces always return durable queued acceptance.
func Drive(ctx context.Context, root, cwd, runID string) (*model.Run, error) {
	managed, err := store.QueueManaged(root)
	if err != nil {
		return nil, err
	}
	if managed {
		return store.LoadRun(root, runID)
	}
	ctx, cancel := context.WithCancel(ctx)
	var worker *Worker
	defer func() {
		cancel()
		if worker != nil {
			_ = worker.Wait()
		}
	}()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := store.LoadDispatch(root, runID)
		if err != nil {
			return nil, err
		}
		if job.Generation == job.Completed && job.Owner == "" {
			if job.Error != "" {
				return nil, fmt.Errorf("worker: %s", job.Error)
			}
			return store.LoadRun(root, runID)
		}
		if worker == nil {
			managed, err := store.QueueManaged(root)
			if err != nil {
				return nil, err
			}
			if managed {
				return store.LoadRun(root, runID)
			}
			worker, err = start(ctx, root, cwd, 1, false, runID)
			if err != nil {
				if errors.Is(err, errManagedAdmission) {
					return store.LoadRun(root, runID)
				}
				if !errors.Is(err, ErrWorkerBusy) {
					return nil, err
				}
			}
		}
		if worker != nil {
			select {
			case <-worker.done:
				if err := worker.Wait(); err != nil {
					return nil, err
				}
				worker = nil
			default:
			}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
