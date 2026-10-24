package engine

import (
	"errors"
	"fmt"
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
	resolved := driver.BuiltInExecutors()
	for name, executor := range executors {
		resolved[name] = executor
	}
	executors = resolved
	for first := true; ; first = false {
		release, _, err := store.AcquireRunLock(run.RunDir)
		if err != nil {
			if !first && errors.Is(err, store.ErrRunBusy) {
				return store.ReloadState(run)
			}
			return err
		}
		err = executeGraph(run, executors)
		release()
		if err != nil || !store.HasControls(run.RunDir) {
			return err
		}
	}
}

func nodeByID(workflow model.Workflow, id string) model.Node {
	for _, node := range workflow.Nodes {
		if node.ID == id {
			return node
		}
	}
	return model.Node{}
}

func recoverOrphans(run *model.Run) {
	for id, node := range run.State.Nodes {
		if node.Status == "running" {
			*node = model.NodeState{Status: "pending", Input: node.Input, IncomingNode: node.IncomingNode, IncomingSessionID: node.IncomingSessionID, Artifacts: map[string]string{}}
			_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.orphaned", "nodeId": id})
		}
	}
}

// RetryPlan permits retrying only the active failed node.
func RetryPlan(run *model.Run, from []string) ([]string, error) {
	id := run.State.ActiveNode
	if id == "" || run.State.Nodes[id] == nil || run.State.Nodes[id].Status != "failed" {
		return nil, fmt.Errorf("active node is not failed")
	}
	if len(from) > 0 && (len(from) != 1 || from[0] != id) {
		return nil, fmt.Errorf("only active failed node %s can be retried", id)
	}
	return []string{id}, nil
}

// Exactly one active node follows the first matching outgoing edge.
func executeGraph(run *model.Run, executors map[string]driver.Executor) error {
	if err := store.ReloadState(run); err != nil {
		return err
	}
	if run.State.Traversals == nil {
		run.State.Traversals = map[string]int{}
	}
	recoverOrphans(run)
	if _, err := applyControls(run); err != nil {
		return err
	}
	if run.State.Status == "cancelled" {
		return store.SaveState(run)
	}
	if run.State.ActiveNode == "" {
		return store.SaveState(run)
	}
	metrics.MarkRunStarted(&run.State, time.Now().UTC())
	run.State.Status = "running"
	if err := store.SaveState(run); err != nil {
		return err
	}
	for run.State.ActiveNode != "" {
		if _, err := applyControls(run); err != nil {
			return err
		}
		if run.State.Status == "cancelled" {
			return store.SaveState(run)
		}
		id := run.State.ActiveNode
		state := run.State.Nodes[id]
		if state == nil {
			return fmt.Errorf("active node %s is missing", id)
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
			task, prepared, err := prepareNode(run, definition, executors)
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
		*run.State.Nodes[selected.To] = model.NodeState{Status: "pending", Input: nextInput, IncomingNode: id, IncomingSessionID: state.SessionID, Artifacts: map[string]string{}}
		if err := store.SaveState(run); err != nil {
			return err
		}
	}
	return nil
}
