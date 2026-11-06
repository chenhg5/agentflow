package engine

import (
	"encoding/json"
	"path/filepath"
	"strings"
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

func runStatusTerminal(status string) bool {
	return status == "cancelled" || status == "succeeded" || status == "failed"
}

// applyControls applies queued requests in arrival order. Requests that no
// longer fit the run (a decision for a node that is not waiting) are recorded
// as ignored rather than failing the run.
func applyControls(run *model.Run) (bool, error) {
	controls, err := store.PeekControls(run.RunDir)
	if err != nil || len(controls) == 0 {
		return false, err
	}
	for _, queued := range controls {
		control := queued.Control
		var reason string
		switch control.Kind {
		case ControlSubmit:
			reason, err = applySubmission(run, control)
		case ControlCancel:
			reason, err = applyCancel(run, control)
		case ControlRetry:
			reason, err = applyRetry(run, control)
		default:
			reason = "unknown control kind"
		}
		if err != nil {
			// The request stays queued because its durable checkpoint did not
			// land; the next executor pass can deliver it again.
			return true, err
		}
		if reason != "" {
			_ = store.AppendEvent(run.RunDir, map[string]any{"type": "control.ignored", "kind": control.Kind, "nodeId": control.NodeID, "reason": reason})
		}
		// Each applied control saves its completed state before emitting its
		// event. Only then may the queued request be acknowledged, so a failed
		// save cannot lose an accepted submission or leave contradictory
		// events behind.
		if err := store.AcknowledgeControl(run.RunDir, queued.Name); err != nil {
			return true, err
		}
	}
	return true, nil
}

func applySubmission(run *model.Run, control model.Control) (string, error) {
	if runStatusTerminal(run.State.Status) {
		return "run is terminal", nil
	}
	node, ok := run.State.Nodes[control.NodeID]
	if !ok || node.Status != "waiting_input" {
		return "node is not waiting for input", nil
	}
	if control.Token != "" && control.Token != node.LeaseToken {
		return "submission token does not match the current invocation", nil
	}
	if problems := schema.ValidateValue("output", control.Output, nodeByID(run.Workflow, control.NodeID).OutputSchema); len(problems) > 0 {
		return schema.FormatContractErrors("invalid submitted output", problems), nil
	}
	node.Result = control.Output
	data, err := json.Marshal(node.Result)
	if err != nil {
		return "", err
	}
	node.OutputBytes += int64(len(data))
	artifact := ""
	if len(node.AttemptSequence) > 0 {
		last := &node.AttemptSequence[len(node.AttemptSequence)-1]
		artifact = strings.TrimPrefix(last.Artifacts["result"], "artifacts/")
	}
	if artifact == "" {
		artifact = visitArtifact(run, control.NodeID, "result.json")
	}
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
	// Report the completed attempt itself, not the per-visit attempt count:
	// after a manual retry, loop visit or interrupted-attempt recovery the
	// durable sequence can be higher than node.Attempts.
	completed := model.AttemptEvidence{Sequence: node.Attempts}
	if len(node.AttemptSequence) > 0 {
		last := &node.AttemptSequence[len(node.AttemptSequence)-1]
		last.Status = "succeeded"
		last.OutputBytes = int64(len(data))
		last.FinishedAt = node.FinishedAt
		last.DurationMs = metrics.DurationMillis(last.StartedAt, last.FinishedAt, time.Now().UTC())
		last.Artifacts["result"] = node.Artifacts["result"]
		completed = *last
	}
	if err := store.SaveState(run); err != nil {
		return "", err
	}
	_ = store.AppendEvent(run.RunDir, map[string]any{
		"type": "node.submitted", "nodeId": control.NodeID, "executor": node.SelectedExecutor,
		"candidate": completed.Candidate, "candidateAttempt": completed.CandidateAttempt, "attempt": completed.Sequence,
		"resultArtifact": node.Artifacts["result"], "output": control.Output,
	})
	return "", nil
}

func applyCancel(run *model.Run, control model.Control) (string, error) {
	if run.State.Status == "cancelled" {
		return "run is already cancelled", nil
	}
	now := time.Now().UTC()
	boundary := now
	if run.State.Status == "succeeded" || run.State.Status == "failed" {
		requestedAt, requestErr := time.Parse(time.RFC3339Nano, control.At)
		finishedAt, finishErr := time.Parse(time.RFC3339Nano, run.State.FinishedAt)
		if requestErr != nil || finishErr != nil || requestedAt.After(finishedAt) {
			return "run is terminal", nil
		}
		// The request was durable before the terminal checkpoint. Cancellation
		// wins, but retains that checkpoint as the run boundary so status,
		// outcome and timing describe one coherent terminal transition.
		boundary = finishedAt
	}
	run.State.Status = "cancelled"
	run.State.Outcome = "cancelled"
	metrics.MarkRunFinished(&run.State, boundary)
	// Freeze unfinished nodes and attempts at the durable run boundary. Without
	// this, a cancelled external wait keeps accruing waiting time whenever the
	// metrics are read again.
	for _, node := range run.State.Nodes {
		if node == nil {
			continue
		}
		// Any started, unfinished node is frozen at the run boundary,
		// including a pending node whose durable continuation had not been
		// restarted yet.
		if node.StartedAt != "" && node.FinishedAt == "" {
			node.FinishedAt = run.State.FinishedAt
			metrics.RefreshNode(node, boundary)
		}
		for index := range node.AttemptSequence {
			attempt := &node.AttemptSequence[index]
			if attempt.StartedAt != "" && attempt.FinishedAt == "" {
				attempt.FinishedAt = run.State.FinishedAt
				attempt.DurationMs = metrics.DurationMillis(attempt.StartedAt, attempt.FinishedAt, boundary)
			}
		}
	}
	if err := store.SaveState(run); err != nil {
		return "", err
	}
	_ = store.AppendEvent(run.RunDir, map[string]any{"type": "run.cancelled", "runId": run.RunID, "requestedAt": control.At})
	return "", nil
}

func applyRetry(run *model.Run, control model.Control) (string, error) {
	ids, err := RetryPlan(run, control.Nodes)
	if err != nil {
		return err.Error(), nil
	}
	for _, id := range ids {
		previous := run.State.Nodes[id]
		archiveAndResetNode(run, id, cloneState(previous))
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
	if err := store.SaveState(run); err != nil {
		return "", err
	}
	_ = store.AppendEvent(run.RunDir, map[string]any{"type": "run.retrying", "runId": run.RunID, "nodes": ids})
	return "", nil
}
