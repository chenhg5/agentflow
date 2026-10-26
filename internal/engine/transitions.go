package engine

import (
	stdctx "context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/chenhg5/agentflow/internal/condition"
	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/graph"
	"github.com/chenhg5/agentflow/internal/metrics"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
	"github.com/chenhg5/agentflow/internal/template"
)

// Execute is the sole writer of run state. Controls are queued if another
// process owns the run lock.
func Execute(run *model.Run, executors map[string]driver.Executor) error {
	if run.Workflow.Version != "3" {
		return fmt.Errorf("only version 3 edge workflows can execute")
	}
	if err := store.ValidateResources(filepath.Dir(filepath.Dir(run.RunDir)), run.Workflow); err != nil {
		return err
	}
	executors = resolveExecutors(executors)
	if err := validateExecutorRegistrations(run.Workflow, executors); err != nil {
		return err
	}
	for first := true; ; first = false {
		release, _, err := store.AcquireRunLock(run.RunDir)
		if err != nil {
			if !first && errors.Is(err, store.ErrRunBusy) {
				return store.ReloadState(run)
			}
			return err
		}
		if parallelWorkflow(run.Workflow) {
			err = executeParallelGraph(run, executors)
		} else {
			err = executeGraph(run, executors)
		}
		release()
		if err != nil || !store.HasControls(run.RunDir) {
			return err
		}
	}
}

// ValidateExecutors rejects workflow references that are absent from the
// effective registry. Availability failures remain candidate-level runtime
// evidence, but a misspelled or removed registration is a workflow setup
// error and can never be selected by fallback policy.
func ValidateExecutors(workflow model.Workflow, executors map[string]driver.Executor) error {
	return validateExecutorRegistrations(workflow, resolveExecutors(executors))
}

func resolveExecutors(executors map[string]driver.Executor) map[string]driver.Executor {
	resolved := driver.BuiltInExecutors()
	for name, executor := range executors {
		resolved[name] = executor
	}
	return resolved
}

func validateExecutorRegistrations(workflow model.Workflow, executors map[string]driver.Executor) error {
	for _, node := range workflow.Nodes {
		for index, candidate := range node.Execution.Candidates {
			if _, ok := executors[candidate.Executor]; !ok {
				return fmt.Errorf("node %s candidate %d references unregistered executor %q", node.ID, index+1, candidate.Executor)
			}
		}
	}
	return nil
}

func nodeByID(workflow model.Workflow, id string) model.Node {
	for _, node := range workflow.Nodes {
		if node.ID == id {
			return node
		}
	}
	return model.Node{}
}

// recoverOrphans reconciles attempts left "running" by a previous executor
// process. It durably checkpoints every recovery mutation before emitting the
// corresponding event, so a failed state write cannot leave events claiming an
// attempt was archived or completed while durable state still says running.
func recoverOrphans(run *model.Run) error {
	events := []map[string]any{}
	emit := func(event map[string]any) { events = append(events, event) }
	for id, node := range run.State.Nodes {
		if node.Status != "running" {
			continue
		}
		now := time.Now().UTC()
		if len(node.AttemptSequence) > 0 {
			attempt := &node.AttemptSequence[len(node.AttemptSequence)-1]
			if attempt.Status == "failed" {
				continuation := node.Continuation
				if continuation != nil && continuation.Kind != continuationTerminal {
					// The attempt already completed and its next step is
					// durable. Resume that step without appending a replay.
					node.Status = "pending"
					emit(map[string]any{
						"type": "node.continuation_resumed", "nodeId": id, "attempt": attempt.Sequence,
						"continuation": continuation.Kind, "candidate": continuation.Candidate, "candidateAttempt": continuation.CandidateAttempt,
					})
					continue
				}
				failure := copyFailure(attempt.Failure)
				if continuation != nil {
					failure = copyFailure(continuation.Failure)
				}
				if failure == nil {
					failure = copyFailure(node.Failure)
				}
				message := "executor attempt failed before the node status was finalized"
				if failure != nil && failure.Message != "" {
					message = failure.Message
				}
				node.Status = "failed"
				node.Error = message
				node.Failure = failure
				node.FinishedAt = attempt.FinishedAt
				if node.FinishedAt == "" {
					node.FinishedAt = now.Format(time.RFC3339Nano)
				}
				metrics.RefreshNode(node, now)
				emit(map[string]any{"type": "node.recovered_failed_attempt", "nodeId": id, "attempt": attempt.Sequence, "failure": failure})
				continue
			}
			if attempt.Status != "running" {
				node.Status = attempt.Status
				node.FinishedAt = attempt.FinishedAt
				metrics.RefreshNode(node, now)
				emit(map[string]any{"type": "node.recovered_completed_attempt", "nodeId": id, "attempt": attempt.Sequence, "status": attempt.Status})
				continue
			}
		}
		archived := cloneState(node)
		failure := &model.ExecutionFailure{Kind: "interrupted", Started: archived.Workspace != "", Message: "executor process ended before the attempt completed", Executor: archived.SelectedExecutor}
		resumeCandidate, resumeAttempt := 0, 1
		if len(archived.AttemptSequence) > 0 {
			attempt := &archived.AttemptSequence[len(archived.AttemptSequence)-1]
			failure.Executor = attempt.Executor
			failure.Attempt = attempt.Sequence
			failure.Started = attempt.Workspace != ""
			resumeCandidate, resumeAttempt = attempt.Candidate, attempt.CandidateAttempt
			attempt.Status = "failed"
			attempt.Failure = failure
			attempt.FinishedAt = now.Format(time.RFC3339Nano)
			attempt.DurationMs = metrics.DurationMillis(attempt.StartedAt, attempt.FinishedAt, now)
		}
		archived.Status = "failed"
		archived.Error = failure.Message
		archived.Failure = failure
		archived.FinishedAt = now.Format(time.RFC3339Nano)
		metrics.RefreshNode(&archived, now)
		oldVisit, newVisit := archiveAndResetNode(run, id, archived)
		// The fresh visit gets a new artifact namespace, but only the attempt
		// that was actually running may replay. Preserve its candidate/retry
		// position instead of restarting the candidate list from scratch.
		node.Continuation = &model.NodeContinuation{
			Kind: continuationResume, Candidate: resumeCandidate, CandidateAttempt: resumeAttempt,
			Failure: failure, At: now.Format(time.RFC3339Nano),
		}
		emit(map[string]any{"type": "node.orphaned", "nodeId": id, "visit": oldVisit, "nextVisit": newVisit, "resumeCandidate": resumeCandidate, "resumeAttempt": resumeAttempt, "failure": failure})
	}
	if len(events) == 0 {
		return nil
	}
	if err := store.SaveState(run); err != nil {
		return err
	}
	for _, event := range events {
		_ = store.AppendEvent(run.RunDir, event)
	}
	return nil
}

func archiveAndResetNode(run *model.Run, id string, archived model.NodeState) (int, int) {
	previous := run.State.Nodes[id]
	oldVisit := run.State.Visits[id]
	run.State.History = append(run.State.History, model.NodeVisit{NodeID: id, Visit: oldVisit, State: archived})
	run.State.Visits[id] = oldVisit + 1
	*previous = model.NodeState{
		Status:                  "pending",
		Input:                   previous.Input,
		IncomingNode:            previous.IncomingNode,
		IncomingSessionID:       previous.IncomingSessionID,
		IncomingExecutor:        previous.IncomingExecutor,
		IncomingSessionProtocol: previous.IncomingSessionProtocol,
		Artifacts:               map[string]string{},
	}
	return oldVisit, run.State.Visits[id]
}

// RetryPlan permits retrying failed active nodes without resetting successful siblings.
func RetryPlan(run *model.Run, from []string) ([]string, error) {
	if parallelWorkflow(run.Workflow) {
		failed := []string{}
		for _, id := range run.State.ActiveNodes {
			if node := run.State.Nodes[id]; node != nil && node.Status == "failed" {
				failed = append(failed, id)
			}
		}
		if len(failed) == 0 {
			return nil, fmt.Errorf("no active failed node to retry")
		}
		if len(from) == 0 {
			return failed, nil
		}
		for _, id := range from {
			found := false
			for _, candidate := range failed {
				if id == candidate {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("node %s is not active and failed", id)
			}
		}
		return from, nil
	}
	id := run.State.ActiveNode
	if id == "" || run.State.Nodes[id] == nil || run.State.Nodes[id].Status != "failed" {
		return nil, fmt.Errorf("active node is not failed")
	}
	if len(from) > 0 && (len(from) != 1 || from[0] != id) {
		return nil, fmt.Errorf("only active failed node %s can be retried", id)
	}
	return []string{id}, nil
}

// Serial workflows follow the first matching outgoing edge.
func executeGraph(run *model.Run, executors map[string]driver.Executor) error {
	if err := store.ReloadState(run); err != nil {
		return err
	}
	if run.State.Traversals == nil {
		run.State.Traversals = map[string]int{}
	}
	if err := recoverOrphans(run); err != nil {
		return err
	}
	if _, err := applyControls(run); err != nil {
		return err
	}
	if runStatusTerminal(run.State.Status) {
		return nil
	}
	if run.State.ActiveNode == "" {
		return store.SaveState(run)
	}
	metrics.MarkRunStarted(&run.State, time.Now().UTC())
	run.State.Status = "running"
	if err := store.SaveState(run); err != nil {
		return err
	}
	stateMu := &sync.Mutex{}
	for run.State.ActiveNode != "" {
		if _, err := applyControls(run); err != nil {
			return err
		}
		if runStatusTerminal(run.State.Status) {
			return nil
		}
		id := run.State.ActiveNode
		state := run.State.Nodes[id]
		if state == nil {
			return fmt.Errorf("active node %s is missing", id)
		}
		if state.Status == "waiting_resource" {
			ready, err := store.ResourcesReady(stdctx.Background(), filepath.Dir(filepath.Dir(run.RunDir)), state.ResourceWait)
			if err != nil {
				return err
			}
			if !ready {
				run.State.Status = "waiting_resource"
				return store.SaveState(run)
			}
			state.Status = "pending"
		}
		if state.Status == "waiting_input" {
			run.State.Status = "waiting_input"
			return store.SaveState(run)
		}
		if state.Status == "failed" {
			run.State.Status = "failed"
			run.State.Outcome = "error"
			metrics.MarkRunFinished(&run.State, time.Now().UTC())
			return store.SaveState(run)
		}
		if state.Status == "pending" {
			definition := nodeByID(run.Workflow, id)
			task, prepared, err := prepareNode(run, definition, executors, stateMu)
			*state = prepared
			if err != nil {
				return err
			}
			if err := store.SaveState(run); err != nil {
				return err
			}
			if task != nil {
				result, err := task.run()
				*state = result
				if err != nil {
					return err
				}
				if err := store.SaveState(run); err != nil {
					return err
				}
			}
			continue
		}
		if state.Status != "succeeded" {
			return fmt.Errorf("active node %s has unexpected status %s", id, state.Status)
		}
		var selected *model.Transition
		edgeContext := context(run.State)
		edgeContext["input"] = state.Input
		edgeContext["output"] = state.Result
		for i := range run.Workflow.Edges {
			edge := &run.Workflow.Edges[i]
			if edge.From != id {
				continue
			}
			matched, err := condition.Evaluate(edge.When, edgeContext)
			if err != nil {
				state.Status = "failed"
				state.Error = err.Error()
				break
			}
			if matched {
				selected = edge
				break
			}
		}
		if state.Status == "failed" {
			continue
		}
		if selected == nil {
			state.Status = "failed"
			state.Error = fmt.Sprintf("no matching edge from %s", id)
			continue
		}
		run.State.History = append(run.State.History, model.NodeVisit{NodeID: id, Visit: run.State.Visits[id], State: cloneState(state)})
		key := graph.TransitionKey(*selected)
		run.State.Traversals[key]++
		if selected.MaxIterations > 0 && run.State.Traversals[key] > selected.MaxIterations {
			run.State.ActiveNode = ""
			run.State.Status = "failed"
			run.State.Outcome = "max_iterations"
			metrics.MarkRunFinished(&run.State, time.Now().UTC())
			_ = store.AppendEvent(run.RunDir, map[string]any{"type": "run.max_iterations", "from": id, "to": selected.To, "limit": selected.MaxIterations})
			return store.SaveState(run)
		}
		_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.transition", "from": id, "to": selected.To, "when": selected.When, "visit": run.State.Visits[id]})
		if selected.To == graph.End {
			run.State.ActiveNode = ""
			run.State.Status = "succeeded"
			run.State.Outcome = "completed"
			for _, other := range run.State.Nodes {
				if other.Status == "pending" {
					other.Status = "skipped"
					other.Reason = "not_selected"
				}
			}
			metrics.MarkRunFinished(&run.State, time.Now().UTC())
			if err := store.SaveState(run); err != nil {
				return err
			}
			return store.AppendEvent(run.RunDir, map[string]any{"type": "run.succeeded", "runId": run.RunID, "outcome": run.State.Outcome})
		}
		nextInput := cloneValue(state.Result)
		if selected.Map != nil {
			nextInput = template.RenderMappingValue(*selected.Map, edgeContext)
		}
		run.State.ActiveNode = selected.To
		run.State.Visits[selected.To]++
		*run.State.Nodes[selected.To] = model.NodeState{Status: "pending", Input: nextInput, IncomingNode: id, IncomingSessionID: state.SessionID, IncomingExecutor: state.SelectedExecutor, IncomingSessionProtocol: state.SessionProtocol, Artifacts: map[string]string{}}
		if err := store.SaveState(run); err != nil {
			return err
		}
	}
	return nil
}
