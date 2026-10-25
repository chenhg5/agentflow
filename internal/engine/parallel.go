package engine

import (
	"fmt"
	"sort"
	"strings"
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

func parallelWorkflow(workflow model.Workflow) bool {
	for _, node := range workflow.Nodes {
		if node.Dispatch == "all" || node.Join != nil {
			return true
		}
	}
	return false
}

func syncActiveNode(state *model.RunState) {
	state.ActiveNode = ""
	if len(state.ActiveNodes) == 1 {
		state.ActiveNode = state.ActiveNodes[0]
	}
}

func removeActive(state *model.RunState, id string) {
	for index, active := range state.ActiveNodes {
		if active == id {
			state.ActiveNodes = append(state.ActiveNodes[:index], state.ActiveNodes[index+1:]...)
			break
		}
	}
	syncActiveNode(state)
}

func executeParallelGraph(run *model.Run, executors map[string]driver.Executor) error {
	if err := store.ReloadState(run); err != nil {
		return err
	}
	if run.State.ActiveNodes == nil && run.State.ActiveNode != "" {
		run.State.ActiveNodes = []string{run.State.ActiveNode}
	}
	if run.State.JoinInputs == nil {
		run.State.JoinInputs = map[string]map[string]any{}
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
	metrics.MarkRunStarted(&run.State, time.Now().UTC())
	run.State.Status = "running"
	if err := store.SaveState(run); err != nil {
		return err
	}
	stateMu := &sync.Mutex{}
	for {
		if _, err := applyControls(run); err != nil {
			return err
		}
		if runStatusTerminal(run.State.Status) {
			return nil
		}
		if len(run.State.ActiveNodes) == 0 {
			if len(run.State.JoinInputs) != 0 {
				for id, inputs := range run.State.JoinInputs {
					state := run.State.Nodes[id]
					if state != nil {
						state.Status = "failed"
						state.Error = fmt.Sprintf("join %s is incomplete: received %d of %d inputs", id, len(inputs), len(nodeByID(run.Workflow, id).Join.From))
					}
				}
				run.State.Status = "failed"
				run.State.Outcome = "join_incomplete"
				metrics.MarkRunFinished(&run.State, time.Now().UTC())
				_ = store.AppendEvent(run.RunDir, map[string]any{"type": "run.join_incomplete", "joins": run.State.JoinInputs, "reason": parallelJoinError(run.State)})
				return store.SaveState(run)
			}
			run.State.Status = "succeeded"
			run.State.Outcome = "completed"
			for _, node := range run.State.Nodes {
				if node.Status == "pending" {
					node.Status = "skipped"
					node.Reason = "not_selected"
				}
			}
			metrics.MarkRunFinished(&run.State, time.Now().UTC())
			if err := store.SaveState(run); err != nil {
				return err
			}
			return store.AppendEvent(run.RunDir, map[string]any{"type": "run.succeeded", "runId": run.RunID, "outcome": run.State.Outcome})
		}
		for _, id := range run.State.ActiveNodes {
			if node := run.State.Nodes[id]; node == nil {
				return fmt.Errorf("active node %s is missing", id)
			} else if node.Status == "failed" {
				run.State.Status = "failed"
				run.State.Outcome = "error"
				metrics.MarkRunFinished(&run.State, time.Now().UTC())
				return store.SaveState(run)
			}
		}
		var tasks []*nodeTask
		preparedPending := false
		for _, id := range run.State.ActiveNodes {
			state := run.State.Nodes[id]
			if state.Status != "pending" {
				continue
			}
			preparedPending = true
			task, prepared, err := prepareNode(run, nodeByID(run.Workflow, id), executors, stateMu)
			*state = prepared
			if err != nil {
				return err
			}
			if task != nil {
				tasks = append(tasks, task)
			}
		}
		if preparedPending {
			if err := store.SaveState(run); err != nil {
				return err
			}
		}
		if len(tasks) != 0 {
			type taskResult struct {
				state model.NodeState
				err   error
			}
			results := make([]taskResult, len(tasks))
			var group sync.WaitGroup
			for index, task := range tasks {
				group.Add(1)
				go func(index int, task *nodeTask) {
					defer group.Done()
					results[index].state, results[index].err = task.run()
				}(index, task)
			}
			group.Wait()
			for index, task := range tasks {
				*run.State.Nodes[task.node.ID] = results[index].state
				if results[index].err != nil {
					return results[index].err
				}
			}
			if err := store.SaveState(run); err != nil {
				return err
			}
			continue
		}
		if preparedPending {
			// Preparing may transition an external node to waiting_input or a
			// deterministic session/configuration failure. Re-evaluate the whole
			// active wave before advancing any previously successful sibling.
			continue
		}
		advanced := false
		for _, id := range append([]string(nil), run.State.ActiveNodes...) {
			if run.State.Nodes[id].Status != "succeeded" {
				continue
			}
			if err := advanceParallelNode(run, id); err != nil {
				return err
			}
			advanced = true
			if run.State.Status == "failed" {
				break
			}
		}
		if run.State.Status == "failed" {
			return store.SaveState(run)
		}
		if advanced {
			if err := store.SaveState(run); err != nil {
				return err
			}
			continue
		}
		waiting := false
		for _, id := range run.State.ActiveNodes {
			if run.State.Nodes[id].Status == "waiting_input" {
				waiting = true
			} else if run.State.Nodes[id].Status != "failed" {
				return fmt.Errorf("active node %s has unexpected status %s", id, run.State.Nodes[id].Status)
			}
		}
		if waiting {
			run.State.Status = "waiting_input"
			return store.SaveState(run)
		}
	}
}

func advanceParallelNode(run *model.Run, id string) error {
	state := run.State.Nodes[id]
	definition := nodeByID(run.Workflow, id)
	context := context(run.State)
	context["input"] = state.Input
	context["output"] = state.Result
	selected := []model.Transition{}
	for _, edge := range run.Workflow.Edges {
		if edge.From != id {
			continue
		}
		matched, err := condition.Evaluate(edge.When, context)
		if err != nil {
			state.Status = "failed"
			state.Error = err.Error()
			return nil
		}
		if matched {
			selected = append(selected, edge)
			if definition.Dispatch != "all" {
				break
			}
		}
	}
	if len(selected) == 0 {
		state.Status = "failed"
		state.Error = fmt.Sprintf("no matching edge from %s", id)
		return nil
	}
	run.State.History = append(run.State.History, model.NodeVisit{NodeID: id, Visit: run.State.Visits[id], State: cloneState(state)})
	removeActive(&run.State, id)
	for _, edge := range selected {
		key := graph.TransitionKey(edge)
		run.State.Traversals[key]++
		if edge.MaxIterations > 0 && run.State.Traversals[key] > edge.MaxIterations {
			run.State.ActiveNodes = nil
			run.State.ActiveNode = ""
			run.State.Status = "failed"
			run.State.Outcome = "max_iterations"
			metrics.MarkRunFinished(&run.State, time.Now().UTC())
			_ = store.AppendEvent(run.RunDir, map[string]any{"type": "run.max_iterations", "from": id, "to": edge.To, "limit": edge.MaxIterations})
			return nil
		}
		_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.transition", "from": id, "to": edge.To, "when": edge.When, "visit": run.State.Visits[id]})
		if edge.To == graph.End {
			continue
		}
		payload := cloneValue(state.Result)
		if edge.Map != nil {
			payload = template.RenderMappingValue(*edge.Map, context)
		}
		target := nodeByID(run.Workflow, edge.To)
		if target.Join != nil {
			if run.State.JoinInputs[edge.To] == nil {
				run.State.JoinInputs[edge.To] = map[string]any{}
			}
			if _, duplicate := run.State.JoinInputs[edge.To][id]; duplicate {
				state.Status = "failed"
				state.Error = fmt.Sprintf("join %s received a second payload from %s", edge.To, id)
				run.State.ActiveNodes = append(run.State.ActiveNodes, id)
				syncActiveNode(&run.State)
				return nil
			}
			run.State.JoinInputs[edge.To][id] = payload
			if len(run.State.JoinInputs[edge.To]) < len(target.Join.From) {
				continue
			}
			payload = cloneValue(run.State.JoinInputs[edge.To])
			delete(run.State.JoinInputs, edge.To)
		}
		for _, active := range run.State.ActiveNodes {
			if active == edge.To {
				state.Status = "failed"
				state.Error = fmt.Sprintf("node %s received concurrent inputs; declare a join", edge.To)
				run.State.ActiveNodes = append(run.State.ActiveNodes, id)
				syncActiveNode(&run.State)
				return nil
			}
		}
		run.State.Visits[edge.To]++
		*run.State.Nodes[edge.To] = model.NodeState{Status: "pending", Input: payload, IncomingNode: id, IncomingSessionID: state.SessionID, IncomingExecutor: state.SelectedExecutor, IncomingSessionProtocol: state.SessionProtocol, Artifacts: map[string]string{}}
		run.State.ActiveNodes = append(run.State.ActiveNodes, edge.To)
		syncActiveNode(&run.State)
	}
	return nil
}

func parallelJoinError(state model.RunState) string {
	parts := make([]string, 0, len(state.JoinInputs))
	for id, inputs := range state.JoinInputs {
		parts = append(parts, fmt.Sprintf("%s (%d arrivals)", id, len(inputs)))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}
