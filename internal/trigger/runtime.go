package trigger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/engine"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/schema"
	"github.com/chenhg5/agentflow/internal/store"
)

type Runtime struct {
	Root      string
	CWD       string
	Executors map[string]driver.Executor
	queue     chan string
	workers   int
	wg        sync.WaitGroup
	mu        sync.Mutex
}

type dispatchJob struct {
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func NewRuntime(root, cwd string, workers int) (*Runtime, error) {
	if workers < 1 {
		workers = 1
	}
	executors, err := driver.LoadExecutors(cwd)
	if err != nil {
		return nil, err
	}
	return &Runtime{Root: root, CWD: cwd, Executors: executors, queue: make(chan string, workers*4), workers: workers}, nil
}

func (r *Runtime) StartWorkers(ctx context.Context) {
	for index := 0; index < r.workers; index++ {
		r.wg.Add(1)
		go r.worker(ctx)
	}
	// A process may have been interrupted after durable enqueue and before
	// execution. Recover queued jobs when the trigger service starts again.
	for _, runID := range r.queuedJobs() {
		r.enqueue(runID)
	}
}

func (r *Runtime) Wait() { r.wg.Wait() }

func (r *Runtime) Start(_ context.Context, command StartRunCommand) (RunRef, error) {
	workflowPath := command.WorkflowPath
	if !filepath.IsAbs(workflowPath) {
		workflowPath = filepath.Join(r.CWD, workflowPath)
	}
	data, err := os.ReadFile(workflowPath)
	if err != nil {
		return RunRef{}, err
	}
	var workflow model.Workflow
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&workflow); err != nil {
		return RunRef{}, fmt.Errorf("invalid workflow JSON: %w", err)
	}
	if problems := schema.Validate(workflow); len(problems) > 0 {
		return RunRef{}, fmt.Errorf("invalid workflow: %v", problems)
	}
	for name, definition := range workflow.Inputs {
		if definition.Required && command.Inputs[name] == "" {
			return RunRef{}, fmt.Errorf("missing required workflow input: %s", name)
		}
	}
	if existing, err := store.FindRunByIdempotency(r.Root, workflow.Name, command.IdempotencyKey); err != nil {
		return RunRef{}, err
	} else if existing != nil {
		return RunRef{RunID: existing.RunID, Status: existing.State.Status}, nil
	}
	run, err := store.CreateRun(r.Root, workflow, command.Inputs, command.IdempotencyKey)
	if err != nil {
		return RunRef{}, err
	}
	run.State.Status = "queued"
	run.State.Trigger = &model.TriggerInfo{ID: command.TriggerID, Kind: command.TriggerKind, DeliveryID: command.DeliveryID, ReceivedAt: command.ReceivedAt.UTC().Format(time.RFC3339Nano)}
	if command.ScheduledAt != nil {
		run.State.Trigger.ScheduledAt = command.ScheduledAt.UTC().Format(time.RFC3339Nano)
	}
	if err := store.SaveState(run); err != nil {
		return RunRef{}, err
	}
	if err := store.AppendEvent(run.RunDir, map[string]any{"type": "run.queued", "runId": run.RunID, "triggerId": command.TriggerID, "deliveryId": command.DeliveryID}); err != nil {
		return RunRef{}, err
	}
	if err := r.saveJob(dispatchJob{RunID: run.RunID, Status: "queued", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		return RunRef{}, err
	}
	r.enqueue(run.RunID)
	return RunRef{RunID: run.RunID, Status: run.State.Status}, nil
}

func (r *Runtime) worker(ctx context.Context) {
	defer r.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case runID := <-r.queue:
			r.execute(runID)
		}
	}
}

func (r *Runtime) execute(runID string) {
	r.updateJob(runID, "running", "")
	run, err := store.LoadRun(r.Root, runID)
	if err == nil && run.State.Status != "cancelled" {
		err = engine.Execute(run, r.Executors)
		if err == nil && run.State.Status == "queued" {
			run.State.Status = "failed"
			err = fmt.Errorf("run worker did not advance queued run")
		}
		if err != nil {
			run.State.Status = "failed"
			run.State.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
			_ = store.SaveState(run)
		}
	}
	if err != nil {
		r.updateJob(runID, "failed", err.Error())
		return
	}
	status := "succeeded"
	if run.State.Status == "failed" {
		status = "failed"
	} else if run.State.Status == "waiting_input" {
		status = "waiting_input"
	}
	r.updateJob(runID, status, "")
}

func (r *Runtime) enqueue(runID string) {
	select {
	case r.queue <- runID:
	default:
		go func() { r.queue <- runID }()
	}
}

func (r *Runtime) jobPath(runID string) string {
	return filepath.Join(r.Root, "dispatch", runID+".json")
}

func (r *Runtime) saveJob(job dispatchJob) error {
	if err := store.EnsureDir(filepath.Dir(r.jobPath(job.RunID))); err != nil {
		return err
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	path := r.jobPath(job.RunID)
	if err := os.WriteFile(path+".tmp", append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func (r *Runtime) updateJob(runID, status, message string) {
	data, err := os.ReadFile(r.jobPath(runID))
	if err != nil {
		return
	}
	var job dispatchJob
	if json.Unmarshal(data, &job) != nil {
		return
	}
	job.Status = status
	job.Error = message
	job.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_ = r.saveJob(job)
}

func (r *Runtime) queuedJobs() []string {
	entries, err := os.ReadDir(filepath.Join(r.Root, "dispatch"))
	if err != nil {
		return nil
	}
	result := []string{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.Root, "dispatch", entry.Name()))
		if err != nil {
			continue
		}
		var job dispatchJob
		if json.Unmarshal(data, &job) == nil && job.Status == "queued" {
			result = append(result, job.RunID)
		}
	}
	return result
}

func (r *Runtime) WaitForRun(ctx context.Context, runID string) (*model.Run, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		run, err := store.LoadRun(r.Root, runID)
		if err != nil {
			return nil, err
		}
		switch run.State.Status {
		case "queued", "pending", "running":
		default:
			return run, nil
		}
		select {
		case <-ctx.Done():
			return run, ctx.Err()
		case <-ticker.C:
		}
	}
}
