package trigger

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/engine"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/queue"
	"github.com/chenhg5/agentflow/internal/schema"
	"github.com/chenhg5/agentflow/internal/store"
)

type Runtime struct {
	Root      string
	CWD       string
	Executors map[string]driver.Executor
	workers   int
	worker    *queue.Worker
}

type dispatchJob = store.DispatchJob

func NewRuntime(root, cwd string, workers int) (*Runtime, error) {
	if workers < 1 {
		workers = 1
	}
	executors, err := driver.LoadExecutors(cwd)
	if err != nil {
		return nil, err
	}
	return &Runtime{Root: root, CWD: cwd, Executors: executors, workers: workers}, nil
}

func (r *Runtime) StartWorkers(ctx context.Context) error {
	worker, err := queue.Start(ctx, r.Root, r.CWD, r.workers, false)
	if err != nil {
		return err
	}
	r.worker = worker
	return nil
}
func (r *Runtime) Wait() {
	if r.worker != nil {
		_ = r.worker.Wait()
	}
}

func (r *Runtime) creationRequest(command StartRunCommand) (store.CreateRequest, error) {
	workflowPath := command.WorkflowPath
	if !filepath.IsAbs(workflowPath) {
		workflowPath = filepath.Join(r.CWD, workflowPath)
	}
	var data []byte
	var err error
	if command.Workflow != nil {
		data, err = json.Marshal(command.Workflow)
	} else {
		data, err = os.ReadFile(workflowPath)
	}
	if err != nil {
		return store.CreateRequest{}, err
	}
	var workflow model.Workflow
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&workflow); err != nil {
		return store.CreateRequest{}, fmt.Errorf("invalid workflow JSON: %w", err)
	}
	if problems := schema.ValidateWorkflowJSON(data); len(problems) > 0 {
		return store.CreateRequest{}, fmt.Errorf("invalid workflow: %v", problems)
	}
	if problems := schema.Validate(workflow); len(problems) > 0 {
		return store.CreateRequest{}, fmt.Errorf("invalid workflow: %v", problems)
	}
	if err := engine.ValidateExecutors(workflow, r.Executors); err != nil {
		return store.CreateRequest{}, err
	}
	if err := store.ValidateResources(r.Root, workflow); err != nil {
		return store.CreateRequest{}, err
	}
	for name, definition := range workflow.Inputs {
		if definition.Required && command.Inputs[name] == "" {
			return store.CreateRequest{}, fmt.Errorf("missing required workflow input: %s", name)
		}
	}
	info := &model.TriggerInfo{ID: command.TriggerID, Kind: command.TriggerKind, DeliveryID: command.DeliveryID, ReceivedAt: command.ReceivedAt.UTC().Format(time.RFC3339Nano), Timezone: command.Timezone, CatchUp: command.CatchUp}
	if command.ScheduledAt != nil {
		info.ScheduledAt = command.ScheduledAt.UTC().Format(time.RFC3339Nano)
	}
	return store.CreateRequest{Workflow: workflow, Inputs: command.Inputs, IdempotencyKey: command.IdempotencyKey, Trigger: info, Queued: true}, nil
}

// Prepare freezes the graph before dispatch records an intent. Recovery cannot
// accidentally create another workflow name under the same occurrence key.
func (r *Runtime) Prepare(_ context.Context, command StartRunCommand) (StartRunCommand, error) {
	request, err := r.creationRequest(command)
	if err != nil {
		return command, err
	}
	command.Workflow = &request.Workflow
	return command, nil
}

func (r *Runtime) ValidateReplay(_ context.Context, command StartRunCommand, previous RunRef) error {
	request, err := r.creationRequest(command)
	if err != nil {
		return err
	}
	return store.ValidateRunRequest(r.Root, previous.RunID, request)
}

func (r *Runtime) Start(ctx context.Context, command StartRunCommand) (RunRef, error) {
	request, err := r.creationRequest(command)
	if err != nil {
		return RunRef{}, err
	}
	created, err := store.CreateOrGetRun(ctx, r.Root, request)
	if err != nil {
		return RunRef{}, err
	}
	run := created.Run
	return RunRef{RunID: run.RunID, Status: run.State.Status}, nil
}

func (r *Runtime) saveJob(job dispatchJob) error { return store.SaveDispatchJob(r.Root, job) }

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
