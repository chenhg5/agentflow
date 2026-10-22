package engine

import (
	"errors"
	"time"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/metrics"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
	"github.com/chenhg5/agentflow/internal/template"
)

// DefaultMaxParallel bounds concurrent nodes when a workflow sets no limit.
const DefaultMaxParallel = 4

// controlPollInterval is how often a busy executor picks up approvals and
// cancellations queued by other processes.
const controlPollInterval = 500 * time.Millisecond

type outcome struct {
	nodeID string
	state  model.NodeState
	err    error
}

// Execute drives a run until no node can make progress. It is the only code
// that writes run state: other commands enqueue controls, which Execute
// applies. If another live process is already executing the run, Execute
// returns store.ErrRunBusy and that process applies queued controls.
func Execute(run *model.Run, providers map[string]driver.Provider) error {
	for first := true; ; first = false {
		release, _, err := store.AcquireRunLock(run.RunDir)
		if err != nil {
			if !first && errors.Is(err, store.ErrRunBusy) {
				// The new executor will apply what is queued.
				return store.ReloadState(run)
			}
			return err
		}
		err = execute(run, providers)
		release()
		// A control can arrive between the executor's last check and the
		// lock release; pick it up instead of leaving it queued.
		if err != nil || !store.HasControls(run.RunDir) {
			return err
		}
	}
}

func execute(run *model.Run, providers map[string]driver.Provider) error {
	if err := store.ReloadState(run); err != nil {
		return err
	}
	recoverOrphans(run)
	if _, err := applyControls(run); err != nil {
		return err
	}
	if run.State.Status == "cancelled" {
		return store.SaveState(run)
	}
	metrics.MarkRunStarted(&run.State, time.Now().UTC())
	run.State.Status = "running"
	if err := store.SaveState(run); err != nil {
		return err
	}
	if err := store.AppendEvent(run.RunDir, map[string]any{"type": "run.started", "runId": run.RunID}); err != nil {
		return err
	}
	limit := run.Workflow.MaxParallel
	if limit < 1 {
		limit = DefaultMaxParallel
	}
	outcomes := make(chan outcome)
	ticker := time.NewTicker(controlPollInterval)
	defer ticker.Stop()
	inflight := 0
	var firstErr error
	for {
		if firstErr == nil && run.State.Status != "cancelled" {
			started, err := schedule(run, providers, limit-inflight, outcomes)
			inflight += started
			if err != nil {
				firstErr = err
			}
		}
		if inflight == 0 {
			changed, err := applyControls(run)
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if changed && firstErr == nil && run.State.Status != "cancelled" {
				continue
			}
			break
		}
		changed := false
		select {
		case result := <-outcomes:
			inflight--
			*run.State.Nodes[result.nodeID] = result.state
			changed = true
			if result.err != nil && firstErr == nil {
				firstErr = result.err
			}
		case <-ticker.C:
		}
		applied, err := applyControls(run)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if changed || applied {
			if err := store.SaveState(run); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		_ = store.SaveState(run)
		return firstErr
	}
	return finish(run)
}

// schedule starts ready nodes up to the free slots and records skipped nodes.
// It returns how many workers it started.
func schedule(run *model.Run, providers map[string]driver.Provider, slots int, outcomes chan<- outcome) (int, error) {
	started := 0
	for progressed := true; progressed; {
		progressed = false
		for _, node := range run.Workflow.Nodes {
			current := run.State.Nodes[node.ID]
			if current.Status != "pending" {
				continue
			}
			ready, reason := readiness(run, node)
			if !ready && reason == "" {
				continue
			}
			if ready && !template.EvaluateWhen(node.When, context(run.State)) {
				ready, reason = false, "when_false"
			}
			if !ready {
				current.Status = "skipped"
				current.Reason = reason
				progressed = true
				_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.skipped", "nodeId": node.ID, "reason": reason})
				continue
			}
			if started >= slots {
				continue
			}
			task, state, err := prepareNode(run, node, providers)
			*current = state
			progressed = true
			if err != nil {
				return started, err
			}
			if err := store.SaveState(run); err != nil {
				return started, err
			}
			if task == nil {
				continue
			}
			started++
			go func(task *nodeTask) {
				state, err := task.run()
				outcomes <- outcome{nodeID: task.node.ID, state: state, err: err}
			}(task)
		}
	}
	return started, nil
}

// finish sets the run status once nothing is running. A run with a node
// waiting for a human stays open; otherwise any failed node fails the run.
func finish(run *model.Run) error {
	if run.State.Status == "cancelled" {
		return store.SaveState(run)
	}
	waiting, failed, pending := 0, 0, 0
	for _, node := range run.State.Nodes {
		switch node.Status {
		case "waiting_human":
			waiting++
		case "failed":
			failed++
		case "pending":
			pending++
		}
	}
	if waiting > 0 {
		run.State.Status = "waiting_human"
		return store.SaveState(run)
	}
	run.State.Status = "succeeded"
	if failed > 0 || pending > 0 {
		run.State.Status = "failed"
	}
	metrics.MarkRunFinished(&run.State, time.Now().UTC())
	if err := store.SaveState(run); err != nil {
		return err
	}
	return store.AppendEvent(run.RunDir, map[string]any{"type": "run." + run.State.Status, "runId": run.RunID})
}

// recoverOrphans resets nodes left running by an executor that died. Holding
// the run lock guarantees no live process is executing them.
func recoverOrphans(run *model.Run) {
	for id, node := range run.State.Nodes {
		if node.Status == "running" {
			*node = model.NodeState{Status: "pending", Artifacts: map[string]string{}}
			_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.orphaned", "nodeId": id})
		}
	}
}
