package engine

import (
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/chenhg5/agentflow/internal/metrics"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/schema"
	"github.com/chenhg5/agentflow/internal/store"
)

// Control kinds accepted by the executor.
const (
	ControlSubmit = "submit"
	ControlCancel = "cancel"
	ControlRetry  = "retry"
)

// applyControls applies queued requests in arrival order. Requests that no
// longer fit the run (a decision for a node that is not waiting) are recorded
// as ignored rather than failing the run.
func applyControls(run *model.Run) (bool, error) {
	controls, err := store.TakeControls(run.RunDir)
	if err != nil || len(controls) == 0 {
		return false, err
	}
	for _, control := range controls {
		var reason string
		switch control.Kind {
		case ControlSubmit:
			reason, err = applySubmission(run, control)
		case ControlCancel:
			reason = applyCancel(run)
		case ControlRetry:
			reason = applyRetry(run, control)
		default:
			reason = "unknown control kind"
		}
		if err != nil {
			return true, err
		}
		if reason != "" {
			_ = store.AppendEvent(run.RunDir, map[string]any{"type": "control.ignored", "kind": control.Kind, "nodeId": control.NodeID, "reason": reason})
		}
	}
	return true, nil
}

func applySubmission(run *model.Run, control model.Control) (string, error) {
	node, ok := run.State.Nodes[control.NodeID]
	if !ok || node.Status != "waiting_input" {
		return "node is not waiting for input", nil
	}
	if problems := schema.ValidateValue("output", control.Output, nodeByID(run.Workflow, control.NodeID).OutputSchema); len(problems) > 0 {
		return schema.FormatContractErrors("invalid submitted output", problems), nil
	}
	node.Result = control.Output
	node.Attempts = 1
	data, err := json.Marshal(node.Result)
	if err != nil {
		return "", err
	}
	node.OutputBytes = int64(len(data))
	artifact := visitArtifact(run, control.NodeID, "result.json")
	if err := store.WriteArtifact(run.RunDir, artifact, append(data, '\n')); err != nil {
		return "", err
	}
	if node.Artifacts == nil {
		node.Artifacts = map[string]string{}
	}
	node.Artifacts["result"] = "artifacts/" + filepath.ToSlash(artifact)
	node.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	metrics.RefreshNode(node, time.Now().UTC())
	node.Status = "succeeded"
	return "", store.AppendEvent(run.RunDir, map[string]any{"type": "node.submitted", "nodeId": control.NodeID, "output": control.Output})
}

func applyCancel(run *model.Run) string {
	if run.State.Status == "cancelled" {
		return "run is already cancelled"
	}
	run.State.Status = "cancelled"
	metrics.MarkRunFinished(&run.State, time.Now().UTC())
	_ = store.AppendEvent(run.RunDir, map[string]any{"type": "run.cancelled", "runId": run.RunID})
	return ""
}

func applyRetry(run *model.Run, control model.Control) string {
	ids, err := RetryPlan(run, control.Nodes)
	if err != nil {
		return err.Error()
	}
	for _, id := range ids {
		previous := run.State.Nodes[id]
		*previous = model.NodeState{Status: "pending", Input: previous.Input, IncomingNode: previous.IncomingNode, IncomingSessionID: previous.IncomingSessionID, Artifacts: map[string]string{}}
		for target, arrivals := range run.State.JoinInputs {
			delete(arrivals, id)
			if len(arrivals) == 0 {
				delete(run.State.JoinInputs, target)
			}
		}
	}
	run.State.Status = "running"
	run.State.Outcome = ""
	run.State.FinishedAt = ""
	run.State.DurationMs = 0
	_ = store.AppendEvent(run.RunDir, map[string]any{"type": "run.retrying", "runId": run.RunID, "nodes": ids})
	return ""
}
