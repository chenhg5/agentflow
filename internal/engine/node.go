package engine

import (
	"bytes"
	stdctx "context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/metrics"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/policy"
	"github.com/chenhg5/agentflow/internal/schema"
	"github.com/chenhg5/agentflow/internal/store"
	"github.com/chenhg5/agentflow/internal/template"
)

// nodeTask owns every candidate attempt for one visit of one logical node.
// Candidate failures never become graph transitions.
type nodeTask struct {
	workflowRun  *model.Run
	node         model.Node
	executors    map[string]driver.Executor
	state        model.NodeState
	input        map[string]any
	visit        int
	stateMu      *sync.Mutex
	runContext   map[string]any
	sourceNodes  map[string]model.NodeState
	history      []model.NodeVisit
	sequenceBase int
}

func prepareNode(run *model.Run, node model.Node, executors map[string]driver.Executor, stateMu *sync.Mutex) (*nodeTask, model.NodeState, error) {
	state := cloneState(run.State.Nodes[node.ID])
	state.Status = "running"
	// A node resumed from a durable continuation keeps its original start so
	// node duration covers the whole visit; a fresh visit starts now.
	if state.StartedAt == "" {
		state.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	state.WaitingStartedAt = ""
	state.FinishedAt = ""
	state.Error = ""
	state.Failure = nil
	state.LeaseToken = store.NewToken()
	if state.Input == nil {
		state.Input = map[string]any{}
	}
	input, ok := state.Input.(map[string]any)
	if !ok {
		return nil, fail(state, "node input must be a JSON object"), nil
	}
	if problems := schema.ValidateValue("input", state.Input, node.InputSchema); len(problems) > 0 {
		return nil, fail(state, schema.FormatContractErrors("input schema validation failed", problems)), nil
	}
	candidates := make([]string, 0, len(node.Execution.Candidates))
	for _, candidate := range node.Execution.Candidates {
		candidates = append(candidates, candidate.Executor)
	}
	if err := store.AppendEvent(run.RunDir, map[string]any{"type": "node.started", "nodeId": node.ID, "candidates": candidates, "visit": run.State.Visits[node.ID]}); err != nil {
		return nil, state, err
	}
	sourceNodes := make(map[string]model.NodeState, len(run.State.Nodes))
	for id, source := range run.State.Nodes {
		sourceNodes[id] = cloneState(source)
	}
	history := make([]model.NodeVisit, len(run.State.History))
	sequenceBase := 0
	for index, visit := range run.State.History {
		history[index] = model.NodeVisit{NodeID: visit.NodeID, Visit: visit.Visit, State: cloneState(&visit.State)}
		if visit.NodeID == node.ID {
			for _, attempt := range visit.State.AttemptSequence {
				if attempt.Sequence > sequenceBase {
					sequenceBase = attempt.Sequence
				}
			}
		}
	}
	task := &nodeTask{
		workflowRun: run, node: node, executors: executors, state: state, input: input,
		visit: run.State.Visits[node.ID], stateMu: stateMu, runContext: context(run.State),
		sourceNodes: sourceNodes, history: history, sequenceBase: sequenceBase,
	}
	// Keep the shared state pending until runAttempt durably installs the
	// selected attempt. That avoids a crash window containing a running node
	// with no attempt evidence at all.
	return task, cloneState(run.State.Nodes[node.ID]), nil
}

// run exhausts each candidate's retries before evaluating fallback and moving
// to the next candidate. It only touches this task's state and attempt files.
//
// Every failed attempt durably records a continuation (retry, fallback or
// terminal) together with the completed attempt before its failure event is
// emitted. A resumed node therefore starts at the recorded position instead of
// replaying an attempt that already finished.
func (t *nodeTask) run() (model.NodeState, error) {
	state := t.state
	maxAttempts := t.maxAttempts()
	candidateIndex, candidateAttempt := 0, 1
	if continuation := state.Continuation; continuation != nil {
		switch continuation.Kind {
		case continuationRetry, continuationResume:
			// A resume cursor replays only the attempt that was still running
			// when the previous process exited; earlier completed candidates
			// and retries stay acknowledged.
			candidateIndex, candidateAttempt = continuation.Candidate, continuation.CandidateAttempt
			if candidateIndex < 0 || candidateIndex >= len(t.node.Execution.Candidates) {
				candidateIndex = 0
			}
			if candidateAttempt < 1 {
				candidateAttempt = 1
			}
			if candidateAttempt > maxAttempts {
				candidateAttempt = maxAttempts
			}
		case continuationFallback:
			candidateIndex, candidateAttempt = continuation.Candidate, 1
		case continuationTerminal:
			return t.failedWithContinuation(state, continuation)
		}
	}
	candidates := t.node.Execution.Candidates
	for candidateIndex < len(candidates) {
		candidate := candidates[candidateIndex]
		advanced := false
		for candidateAttempt <= maxAttempts {
			attempt, failure, err := t.runAttempt(&state, candidateIndex, candidateAttempt, candidate)
			if errors.Is(err, store.ErrResourceUnavailable) {
				return state, nil
			}
			if err != nil {
				return state, err
			}
			if attempt.Status == "succeeded" || attempt.Status == "waiting_input" {
				return state, nil
			}
			continuation := state.Continuation
			if continuation == nil {
				// Defensive: failAttempt installs the checkpoint before returning.
				if failure == nil {
					failure = state.Failure
				}
				return t.failed(state, failureMessage(failure, "executor attempt failed"))
			}
			switch continuation.Kind {
			case continuationRetry:
				_ = store.AppendEvent(t.workflowRun.RunDir, map[string]any{
					"type": "node.retrying", "nodeId": t.node.ID, "executor": candidate.Executor,
					"candidate": candidateIndex, "candidateAttempt": candidateAttempt, "failure": continuation.Failure,
				})
				candidateAttempt = continuation.CandidateAttempt
			case continuationFallback:
				next := candidates[continuation.Candidate]
				_ = store.AppendEvent(t.workflowRun.RunDir, map[string]any{
					"type": "node.fallback_selected", "nodeId": t.node.ID, "fromExecutor": candidate.Executor,
					"toExecutor": next.Executor, "fromCandidate": candidateIndex, "toCandidate": continuation.Candidate,
					"failure": continuation.Failure,
				})
				candidateIndex = continuation.Candidate
				candidateAttempt = 1
				advanced = true
			case continuationTerminal:
				return t.failedWithContinuation(state, continuation)
			default:
				return t.failed(state, failureMessage(continuation.Failure, "executor attempt failed"))
			}
			if advanced {
				break
			}
		}
		if advanced {
			continue
		}
		// The inner loop can only end without a decision on corrupt state; fail
		// explicitly rather than silently skipping a candidate.
		return t.failed(state, failureMessage(state.Failure, "node retry continuation is invalid"))
	}
	return t.failed(state, "node has no execution candidates")
}

func (t *nodeTask) maxAttempts() int {
	maxAttempts := t.node.Retry.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	return maxAttempts
}

func (t *nodeTask) failedWithContinuation(state model.NodeState, continuation *model.NodeContinuation) (model.NodeState, error) {
	state.Failure = copyFailure(continuation.Failure)
	return t.failed(state, failureMessage(continuation.Failure, "node execution failed"))
}

func failureMessage(failure *model.ExecutionFailure, fallback string) string {
	if failure != nil && failure.Message != "" {
		return failure.Message
	}
	return fallback
}

func (t *nodeTask) runAttempt(state *model.NodeState, candidateIndex, candidateAttempt int, candidate model.ExecutorCandidate) (out model.AttemptEvidence, outFailure *model.ExecutionFailure, runErr error) {
	sequence := t.sequenceBase + len(state.AttemptSequence) + 1
	token := store.NewToken()
	var reservation *store.Reservation
	executorDef := t.executors[candidate.Executor]
	if executorDef.Kind != "external" && len(t.node.Resources) > 0 {
		root := filepath.Dir(filepath.Dir(t.workflowRun.RunDir))
		var err error
		reservation, err = store.AcquireResources(stdctx.Background(), root, store.ResourceClaim{RunID: t.workflowRun.RunID, NodeID: t.node.ID, Attempt: sequence, Token: token, Requests: t.node.Resources})
		if errors.Is(err, store.ErrResourceUnavailable) {
			state.Status = "waiting_resource"
			state.Reason = "resource_capacity"
			state.ResourceWait = t.node.Resources
			state.Error = ""
			state.Failure = nil
			state.Continuation = &model.NodeContinuation{Kind: continuationResume, Candidate: candidateIndex, CandidateAttempt: candidateAttempt, At: time.Now().UTC().Format(time.RFC3339Nano)}
			if persistErr := t.persistState(*state); persistErr != nil {
				return out, nil, persistErr
			}
			_ = store.AppendEvent(t.workflowRun.RunDir, map[string]any{"type": "node.waiting_resource", "nodeId": t.node.ID, "requests": t.node.Resources, "candidate": candidateIndex, "candidateAttempt": candidateAttempt})
			return out, nil, err
		}
		if err != nil {
			return out, nil, err
		}
		defer func() {
			if err := reservation.Release(); runErr == nil {
				runErr = err
			}
		}()
	}
	state.ResourceWait = nil
	state.Reason = ""
	now := time.Now().UTC()
	attempt := model.AttemptEvidence{
		Sequence: sequence, Candidate: candidateIndex, CandidateAttempt: candidateAttempt,
		Executor: candidate.Executor, Status: "running", StartedAt: now.Format(time.RFC3339Nano),
		Artifacts: map[string]string{},
	}
	if reservation != nil {
		attempt.ResourceClaimID = reservation.Claim.ID
	}
	selected := candidateIndex
	state.SelectedCandidate = &selected
	state.SelectedExecutor = candidate.Executor
	state.SessionID = ""
	state.SessionProtocol = ""
	state.Workspace = ""
	state.Failure = nil
	// Rotate the lease before this attempt is exposed. A failed candidate that
	// captured AGENTFLOW_NODE_TOKEN must not be able to submit output for a
	// later fallback or retry that reuses the same node; the new token is part
	// of the same durable checkpoint that installs the attempt.
	state.LeaseToken = token
	// Consume the previous continuation in the same durable write that
	// installs this running attempt. Until that write lands, recovery sees the
	// earlier completed attempt and resumes its recorded continuation.
	state.Continuation = nil
	state.Status = "running"
	state.WaitingStartedAt = ""

	inputArtifact := attemptArtifactName(t.node.ID, t.visit, candidateIndex, candidateAttempt, "input.json")
	configArtifact := attemptArtifactName(t.node.ID, t.visit, candidateIndex, candidateAttempt, "config.json")
	resultArtifact := attemptArtifactName(t.node.ID, t.visit, candidateIndex, candidateAttempt, "result.json")
	sessionArtifact := attemptArtifactName(t.node.ID, t.visit, candidateIndex, candidateAttempt, "session.txt")
	stdoutArtifact := filepath.Join("logs", attemptArtifactName(t.node.ID, t.visit, candidateIndex, candidateAttempt, "stdout.log"))
	stderrArtifact := filepath.Join("logs", attemptArtifactName(t.node.ID, t.visit, candidateIndex, candidateAttempt, "stderr.log"))
	attempt.Artifacts["input"] = artifactReference(inputArtifact)
	attempt.Artifacts["config"] = artifactReference(configArtifact)
	attempt.Artifacts["stdoutLog"] = artifactReference(stdoutArtifact)
	attempt.Artifacts["stderrLog"] = artifactReference(stderrArtifact)
	state.AttemptSequence = append(state.AttemptSequence, attempt)
	state.Attempts = len(state.AttemptSequence)
	state.Artifacts = copyArtifacts(attempt.Artifacts)
	if err := t.persistState(*state); err != nil {
		return attempt, nil, err
	}

	inputBytes, err := writeJSONArtifactBytes(t.workflowRun.RunDir, inputArtifact, state.Input)
	if err != nil {
		return attempt, nil, err
	}
	attempt.InputBytes = inputBytes
	state.InputBytes += inputBytes
	inputFile, err := store.ArtifactPath(t.workflowRun.RunDir, inputArtifact)
	if err != nil {
		return attempt, nil, err
	}
	runContext := t.runContext
	runContext["input"] = state.Input
	config, _ := template.RenderValue(candidate.Config, runContext).(map[string]any)
	if config == nil {
		config = map[string]any{}
	}
	if _, err := writeJSONArtifactBytes(t.workflowRun.RunDir, configArtifact, config); err != nil {
		return attempt, nil, err
	}
	configFile, err := store.ArtifactPath(t.workflowRun.RunDir, configArtifact)
	if err != nil {
		return attempt, nil, err
	}
	resultFile, err := store.ArtifactPath(t.workflowRun.RunDir, resultArtifact)
	if err != nil {
		return attempt, nil, err
	}
	sessionFile, err := store.ArtifactPath(t.workflowRun.RunDir, sessionArtifact)
	if err != nil {
		return attempt, nil, err
	}
	if err := store.WriteArtifact(t.workflowRun.RunDir, stdoutArtifact, nil); err != nil {
		return attempt, nil, err
	}
	if err := store.WriteArtifact(t.workflowRun.RunDir, stderrArtifact, nil); err != nil {
		return attempt, nil, err
	}
	_ = os.Remove(resultFile)
	_ = os.Remove(sessionFile)

	executor, ok := t.executors[candidate.Executor]
	if !ok {
		// Execute validates the complete registry before preparing any node. Keep
		// a missing entry non-fallbackable here as a defensive invariant instead
		// of turning workflow setup errors into candidate evidence.
		return attempt, nil, fmt.Errorf("node %s candidate %d references unregistered executor %q", t.node.ID, candidateIndex+1, candidate.Executor)
	}
	attempt.SessionProtocol = driver.SessionProtocol(candidate.Executor, executor)
	state.SessionProtocol = attempt.SessionProtocol
	t.syncAttempt(state, attempt)
	if err := t.persistState(*state); err != nil {
		return attempt, nil, err
	}
	sessionMode, sessionID, err := sessionForCandidate(t.node, candidate, *state, runContext, candidate.Executor, executor, t.sourceNodes, t.history)
	if err != nil {
		failure := executionFailure("session_incompatible", false, nil, false, err.Error(), candidate.Executor, sequence, "")
		if err := t.failAttempt(state, &attempt, failure); err != nil {
			return attempt, nil, err
		}
		return attempt, failure, nil
	}
	// The external adapter registers a waiting node instead of launching a
	// provider process. It cannot create, resume, or fork a session, so a
	// non-new policy would otherwise wait and silently discard the policy (and
	// report an empty session). Reject it deterministically, after session
	// resolution so an absent source still reports session_incompatible.
	if executor.Kind == "external" && sessionMode != "new" {
		message := fmt.Sprintf("external executor %s does not support session mode %s", candidate.Executor, sessionMode)
		failure := executionFailure("configuration_failed", false, nil, false, message, candidate.Executor, sequence, "")
		if err := t.failAttempt(state, &attempt, failure); err != nil {
			return attempt, nil, err
		}
		return attempt, failure, nil
	}
	// Prepare resolves the command required by this invocation's session mode.
	// A missing resume/fork command is a configuration error, not a startup
	// failure, even when an unused base command is unavailable; only the
	// command that is actually launched may produce start_failed.
	prepared, err := driver.Prepare(candidate.Executor, executor, driver.Invocation{
		Input: t.input, Config: config, InputSchema: t.node.InputSchema, OutputSchema: t.node.OutputSchema,
		SessionMode: sessionMode, SessionID: sessionID, ResultFile: resultFile, RunID: t.workflowRun.RunID, NodeID: t.node.ID,
	})
	if err != nil {
		failure := executionFailure("configuration_failed", false, nil, false, err.Error(), candidate.Executor, sequence, "")
		if err := t.failAttempt(state, &attempt, failure); err != nil {
			return attempt, nil, err
		}
		return attempt, failure, nil
	}
	attempt.Workspace, attempt.SessionID = prepared.Workspace, prepared.SessionID
	state.Workspace, state.SessionID, state.SessionProtocol = prepared.Workspace, prepared.SessionID, attempt.SessionProtocol
	state.Artifacts = copyArtifacts(attempt.Artifacts)
	if prepared.Waiting {
		attempt.Artifacts["result"] = artifactReference(resultArtifact)
		state.Artifacts = copyArtifacts(attempt.Artifacts)
		attempt.Status = "waiting_input"
		state.Status = "waiting_input"
		state.WaitingStartedAt = attempt.StartedAt
		t.syncAttempt(state, attempt)
		if err := t.persistState(*state); err != nil {
			return attempt, nil, err
		}
		_ = store.AppendEvent(t.workflowRun.RunDir, map[string]any{
			"type": "node.waiting_input", "nodeId": t.node.ID, "executor": candidate.Executor,
			"candidate": candidateIndex, "attempt": sequence, "waitingStartedAt": state.WaitingStartedAt, "artifacts": attempt.Artifacts,
		})
		return attempt, nil, nil
	}
	t.syncAttempt(state, attempt)
	if err := t.persistState(*state); err != nil {
		return attempt, nil, err
	}
	env := map[string]string{
		"AGENTFLOW_ROOT": filepath.Dir(filepath.Dir(t.workflowRun.RunDir)), "AGENTFLOW_RUN_ID": t.workflowRun.RunID,
		"AGENTFLOW_NODE_ID": t.node.ID, "AGENTFLOW_NODE_TOKEN": state.LeaseToken,
		"AGENTFLOW_WORKSPACE": prepared.Workspace, "AGENTFLOW_SESSION_ID": prepared.SessionID,
		"AGENTFLOW_SESSION_MODE": prepared.SessionMode, "AGENTFLOW_RESULT_FILE": resultFile,
		"AGENTFLOW_INPUT_FILE": inputFile, "AGENTFLOW_CONFIG_FILE": configFile, "AGENTFLOW_SESSION_FILE": sessionFile,
	}
	for key, value := range prepared.Env {
		if _, reserved := env[key]; !reserved {
			env[key] = value
		}
	}
	_ = store.AppendEvent(t.workflowRun.RunDir, map[string]any{
		"type": "node.attempt.started", "nodeId": t.node.ID, "executor": candidate.Executor,
		"candidate": candidateIndex, "candidateAttempt": candidateAttempt, "attempt": sequence,
		"workspace": prepared.Workspace, "sessionId": prepared.SessionID, "sessionProtocol": attempt.SessionProtocol, "artifacts": attempt.Artifacts,
		"environment": driver.EnvironmentDescription(prepared.Environment),
	})
	var onStart func(int) error
	if reservation != nil {
		env["AGENTFLOW_RESOURCE_CLAIM"] = reservation.Claim.ID
		onStart = reservation.Started
	}
	result := driver.RunCommand(prepared.Command, driver.CommandOptions{OnStart: onStart, Dir: prepared.Workspace, Env: env, Environment: prepared.Environment, Stdin: prepared.Stdin, TimeoutMs: t.node.TimeoutMs})
	if err := store.WriteArtifact(t.workflowRun.RunDir, stdoutArtifact, []byte(result.Stdout)); err != nil {
		return attempt, nil, err
	}
	if err := store.WriteArtifact(t.workflowRun.RunDir, stderrArtifact, []byte(result.Stderr)); err != nil {
		return attempt, nil, err
	}
	attempt.StdoutBytes, attempt.StderrBytes = int64(len([]byte(result.Stdout))), int64(len([]byte(result.Stderr)))
	state.StdoutBytes += attempt.StdoutBytes
	state.StderrBytes += attempt.StderrBytes
	if reported := sessionFromFile(sessionFile); reported != "" {
		attempt.SessionID = reported
		attempt.Artifacts["session"] = artifactReference(sessionArtifact)
	} else if reported := driver.SessionFromLogs(executor, result.Stdout); reported != "" {
		attempt.SessionID = reported
	}
	state.SessionID, state.SessionProtocol = attempt.SessionID, attempt.SessionProtocol
	if failure := commandFailure(result, candidate.Executor, sequence, t.node.TimeoutMs); failure != nil {
		if data, err := os.ReadFile(resultFile); err == nil {
			attempt.OutputBytes = int64(len(data))
			state.OutputBytes += attempt.OutputBytes
			attempt.Artifacts["result"] = artifactReference(resultArtifact)
		}
		if err := t.failAttempt(state, &attempt, failure); err != nil {
			return attempt, nil, err
		}
		return attempt, failure, nil
	}

	structured, resultBytes, loadErr := loadResult(resultFile)
	attempt.OutputBytes = resultBytes
	state.OutputBytes += resultBytes
	if _, statErr := os.Stat(resultFile); statErr == nil {
		attempt.Artifacts["result"] = artifactReference(resultArtifact)
	}
	if loadErr != nil {
		kind := "result_invalid"
		if os.IsNotExist(fileError(resultFile)) {
			kind = "result_missing"
		}
		failure := executionFailure(kind, true, intPointer(0), false, loadErr.Error(), candidate.Executor, sequence, result.Signal)
		if err := t.failAttempt(state, &attempt, failure); err != nil {
			return attempt, nil, err
		}
		return attempt, failure, nil
	}
	if problems := schema.ValidateValue("output", structured, schema.EffectiveOutputSchema(t.node)); len(problems) > 0 {
		message := schema.FormatContractErrors("output schema validation failed", problems)
		failure := executionFailure("output_schema", true, intPointer(0), false, message, candidate.Executor, sequence, result.Signal)
		if err := t.failAttempt(state, &attempt, failure); err != nil {
			return attempt, nil, err
		}
		return attempt, failure, nil
	}
	if candidate.Session.Mode == "fork" && (attempt.SessionID == "" || attempt.SessionID == prepared.SessionID) {
		failure := executionFailure("session_invalid", true, intPointer(0), false, "fork executor must report a new session ID in AGENTFLOW_SESSION_FILE or native JSON logs", candidate.Executor, sequence, result.Signal)
		if err := t.failAttempt(state, &attempt, failure); err != nil {
			return attempt, nil, err
		}
		return attempt, failure, nil
	}
	if candidate.Session.Mode == "fixed" && attempt.SessionID == "" {
		failure := executionFailure("session_invalid", true, intPointer(0), false, "fixed session executor must report a session ID in AGENTFLOW_SESSION_FILE or native JSON logs", candidate.Executor, sequence, result.Signal)
		if err := t.failAttempt(state, &attempt, failure); err != nil {
			return attempt, nil, err
		}
		return attempt, failure, nil
	}
	attempt.Status = "succeeded"
	attempt.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	attempt.DurationMs = metrics.DurationMillis(attempt.StartedAt, attempt.FinishedAt, time.Now().UTC())
	state.Status = "succeeded"
	state.Result = structured
	state.SessionID, state.SessionProtocol = attempt.SessionID, attempt.SessionProtocol
	state.Artifacts = copyArtifacts(attempt.Artifacts)
	state.FinishedAt = attempt.FinishedAt
	state.Failure = nil
	metrics.RefreshNode(state, time.Now().UTC())
	t.syncAttempt(state, attempt)
	if err := t.persistState(*state); err != nil {
		return attempt, nil, err
	}
	_ = store.AppendEvent(t.workflowRun.RunDir, map[string]any{
		"type": "node.attempt.succeeded", "nodeId": t.node.ID, "executor": candidate.Executor,
		"candidate": candidateIndex, "candidateAttempt": candidateAttempt, "attempt": sequence,
		"sessionId": attempt.SessionID, "sessionProtocol": attempt.SessionProtocol, "artifacts": attempt.Artifacts,
	})
	_ = store.AppendEvent(t.workflowRun.RunDir, map[string]any{
		"type": "node.succeeded", "nodeId": t.node.ID, "executor": candidate.Executor,
		"candidate": candidateIndex, "attempt": sequence, "resultArtifact": attempt.Artifacts["result"],
	})
	return attempt, nil, nil
}

func (t *nodeTask) finishFailedAttempt(attempt *model.AttemptEvidence, failure *model.ExecutionFailure) {
	attempt.Status = "failed"
	attempt.Failure = failure
	attempt.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	attempt.DurationMs = metrics.DurationMillis(attempt.StartedAt, attempt.FinishedAt, time.Now().UTC())
}

const (
	continuationRetry    = "retry"
	continuationFallback = "fallback"
	continuationTerminal = "terminal"
	// continuationResume replays only an interrupted running attempt at its
	// persisted candidate/retry position after the visit was archived.
	continuationResume = "resume"
)

func (t *nodeTask) failAttempt(state *model.NodeState, attempt *model.AttemptEvidence, failure *model.ExecutionFailure) error {
	t.finishFailedAttempt(attempt, failure)
	continuation, terminalFailure := t.continuationAfterFailure(attempt.Candidate, attempt.CandidateAttempt, failure)
	// Attempt evidence stays tied to the executor result. A fallback-expression
	// evaluation error is a node/continuation decision, so it replaces the
	// node failure and event detail without rewriting attemptSequence[].failure
	// and losing the real start/exit classification.
	state.Failure = terminalFailure
	state.SessionID, state.SessionProtocol = attempt.SessionID, attempt.SessionProtocol
	state.Continuation = continuation
	if continuation.Kind == continuationFallback {
		state.FallbackReason = failure.Message
	}
	t.syncAttempt(state, *attempt)
	// Durable completion checkpoint: the completed attempt and its
	// retry/fallback/terminal decision are persisted in one write before the
	// failure event is emitted. A crash after this point resumes the recorded
	// decision; only an attempt still marked running in durable state (a crash
	// between process launch and this checkpoint) may replay.
	if err := t.persistState(*state); err != nil {
		return err
	}
	t.recordAttemptFailure(*attempt, failure)
	return nil
}

// continuationAfterFailure computes the explicit next step for a completed
// attempt. It returns the failure to persist, which differs only when a
// malformed fallback expression turns into a terminal fallback_expression
// failure.
func (t *nodeTask) continuationAfterFailure(candidateIndex, candidateAttempt int, failure *model.ExecutionFailure) (*model.NodeContinuation, *model.ExecutionFailure) {
	at := time.Now().UTC().Format(time.RFC3339Nano)
	if candidateAttempt < t.maxAttempts() {
		return &model.NodeContinuation{Kind: continuationRetry, Candidate: candidateIndex, CandidateAttempt: candidateAttempt + 1, Failure: copyFailure(failure), At: at}, failure
	}
	candidates := t.node.Execution.Candidates
	if candidateIndex+1 < len(candidates) {
		matched, err := policy.AllowsFallback(t.node.Execution.Fallback, failure)
		if err != nil {
			expressionFailure := &model.ExecutionFailure{
				Kind: "fallback_expression", Started: failure.Started, TimedOut: failure.TimedOut,
				Message: err.Error(), Executor: failure.Executor, Attempt: failure.Attempt,
			}
			return &model.NodeContinuation{Kind: continuationTerminal, Candidate: candidateIndex, CandidateAttempt: candidateAttempt, Failure: copyFailure(expressionFailure), At: at}, expressionFailure
		}
		if matched {
			return &model.NodeContinuation{Kind: continuationFallback, Candidate: candidateIndex + 1, CandidateAttempt: 1, Failure: copyFailure(failure), At: at}, failure
		}
	}
	return &model.NodeContinuation{Kind: continuationTerminal, Candidate: candidateIndex, CandidateAttempt: candidateAttempt, Failure: copyFailure(failure), At: at}, failure
}

func (t *nodeTask) syncAttempt(state *model.NodeState, attempt model.AttemptEvidence) {
	if len(state.AttemptSequence) == 0 {
		state.AttemptSequence = append(state.AttemptSequence, attempt)
	} else {
		state.AttemptSequence[len(state.AttemptSequence)-1] = attempt
	}
	state.Attempts = len(state.AttemptSequence)
	state.Artifacts = copyArtifacts(attempt.Artifacts)
}

func (t *nodeTask) persistState(state model.NodeState) error {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	*t.workflowRun.State.Nodes[t.node.ID] = cloneState(&state)
	return store.SaveState(t.workflowRun)
}

func (t *nodeTask) recordAttemptFailure(attempt model.AttemptEvidence, failure *model.ExecutionFailure) {
	_ = store.AppendEvent(t.workflowRun.RunDir, map[string]any{
		"type": "node.attempt.failed", "nodeId": t.node.ID, "executor": attempt.Executor,
		"candidate": attempt.Candidate, "candidateAttempt": attempt.CandidateAttempt,
		"attempt": attempt.Sequence, "failure": failure, "artifacts": attempt.Artifacts,
	})
}

func commandFailure(result driver.CommandResult, executor string, attempt, timeoutMs int) *model.ExecutionFailure {
	if !result.Started {
		message := result.Error
		if message == "" {
			message = "command failed to start"
		}
		return executionFailure("start_failed", false, nil, false, message, executor, attempt, result.Signal)
	}
	if result.TimedOut {
		message := fmt.Sprintf("command timed out after %dms", timeoutMs)
		return executionFailure("timeout", true, intPointer(result.Code), true, message, executor, attempt, result.Signal)
	}
	if result.Code != 0 {
		message := fmt.Sprintf("command exited with code %d", result.Code)
		if result.Error != "" {
			message += ": " + result.Error
		}
		return executionFailure("exit_code", true, intPointer(result.Code), false, message, executor, attempt, result.Signal)
	}
	return nil
}

func executionFailure(kind string, started bool, code *int, timedOut bool, message, executor string, attempt int, signal string) *model.ExecutionFailure {
	return &model.ExecutionFailure{Kind: kind, Started: started, Code: code, TimedOut: timedOut, Message: message, Executor: executor, Attempt: attempt, Signal: signal}
}

func sessionFromFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(bytes.TrimSpace(data))
}

func (t *nodeTask) failed(state model.NodeState, message string) (model.NodeState, error) {
	state = fail(state, message)
	if err := t.persistState(state); err != nil {
		return state, err
	}
	_ = store.AppendEvent(t.workflowRun.RunDir, map[string]any{
		"type": "node.failed", "nodeId": t.node.ID, "executor": state.SelectedExecutor,
		"candidate": state.SelectedCandidate, "error": message, "failure": state.Failure,
	})
	return state, nil
}

func fail(state model.NodeState, message string) model.NodeState {
	state.Status = "failed"
	state.Error = message
	state.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	metrics.RefreshNode(&state, time.Now().UTC())
	return state
}

func cloneState(state *model.NodeState) model.NodeState {
	clone := *state
	clone.Artifacts = copyArtifacts(state.Artifacts)
	clone.AttemptSequence = make([]model.AttemptEvidence, len(state.AttemptSequence))
	for index, attempt := range state.AttemptSequence {
		clone.AttemptSequence[index] = attempt
		clone.AttemptSequence[index].Artifacts = copyArtifacts(attempt.Artifacts)
		clone.AttemptSequence[index].Failure = copyFailure(attempt.Failure)
	}
	clone.Failure = copyFailure(state.Failure)
	clone.Continuation = copyContinuation(state.Continuation)
	if state.SelectedCandidate != nil {
		value := *state.SelectedCandidate
		clone.SelectedCandidate = &value
	}
	return clone
}

func copyContinuation(continuation *model.NodeContinuation) *model.NodeContinuation {
	if continuation == nil {
		return nil
	}
	clone := *continuation
	clone.Failure = copyFailure(continuation.Failure)
	return &clone
}

func copyFailure(failure *model.ExecutionFailure) *model.ExecutionFailure {
	if failure == nil {
		return nil
	}
	clone := *failure
	if failure.Code != nil {
		value := *failure.Code
		clone.Code = &value
	}
	return &clone
}

func copyArtifacts(artifacts map[string]string) map[string]string {
	clone := make(map[string]string, len(artifacts))
	for key, value := range artifacts {
		clone[key] = value
	}
	return clone
}

func cloneValue(value any) any {
	if value == nil {
		return map[string]any{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var clone any
	if err := json.Unmarshal(data, &clone); err != nil {
		return value
	}
	return clone
}

func sessionForCandidate(node model.Node, candidate model.ExecutorCandidate, state model.NodeState, runContext map[string]any, executorName string, executor driver.Executor, sourceNodes map[string]model.NodeState, history []model.NodeVisit) (string, string, error) {
	spec := candidate.Session
	mode := spec.Mode
	if mode == "" {
		mode = "new"
	}
	switch mode {
	case "new":
		return mode, "", nil
	case "fixed":
		if spec.ID != "" {
			id := template.Render(spec.ID, runContext)
			if id == "" {
				return "", "", fmt.Errorf("node %s candidate %s fixed session id resolved to an empty value", node.ID, executorName)
			}
			return mode, id, nil
		}
		for index := len(state.AttemptSequence) - 1; index >= 0; index-- {
			attempt := state.AttemptSequence[index]
			if attempt.Executor == executorName && attempt.SessionID != "" {
				if !driver.SessionProtocolCompatible(executorName, executor, attempt.SessionProtocol) {
					return "", "", incompatibleSessionError(node.ID, executorName, mode, attempt.Executor, attempt.SessionProtocol)
				}
				return mode, attempt.SessionID, nil
			}
		}
		for index := len(history) - 1; index >= 0; index-- {
			visit := history[index]
			if visit.NodeID != node.ID {
				continue
			}
			for attemptIndex := len(visit.State.AttemptSequence) - 1; attemptIndex >= 0; attemptIndex-- {
				attempt := visit.State.AttemptSequence[attemptIndex]
				if attempt.Executor != executorName || attempt.SessionID == "" {
					continue
				}
				if !driver.SessionProtocolCompatible(executorName, executor, attempt.SessionProtocol) {
					return "", "", incompatibleSessionError(node.ID, executorName, mode, attempt.Executor, attempt.SessionProtocol)
				}
				return mode, attempt.SessionID, nil
			}
			if visit.State.SelectedExecutor == executorName && visit.State.SessionID != "" {
				if !driver.SessionProtocolCompatible(executorName, executor, visit.State.SessionProtocol) {
					return "", "", incompatibleSessionError(node.ID, executorName, mode, visit.State.SelectedExecutor, visit.State.SessionProtocol)
				}
				return mode, visit.State.SessionID, nil
			}
		}
		return mode, "", nil
	case "inherit", "fork":
		source := spec.From
		if source == "" {
			source = state.IncomingNode
		}
		id, sourceExecutor, sourceProtocol := "", "", ""
		if source == state.IncomingNode {
			id, sourceExecutor, sourceProtocol = state.IncomingSessionID, state.IncomingExecutor, state.IncomingSessionProtocol
		}
		if source != "" {
			if sourceState, ok := sourceNodes[source]; ok && sourceState.SessionID != "" {
				id, sourceExecutor, sourceProtocol = sourceState.SessionID, sourceState.SelectedExecutor, sourceState.SessionProtocol
			}
			if id == "" {
				for index := len(history) - 1; index >= 0; index-- {
					visit := history[index]
					if visit.NodeID == source && visit.State.SessionID != "" {
						id, sourceExecutor, sourceProtocol = visit.State.SessionID, visit.State.SelectedExecutor, visit.State.SessionProtocol
						break
					}
				}
			}
		}
		if id == "" {
			return "", "", fmt.Errorf("node %s session mode %s needs a session from %s", node.ID, mode, sourceLabel(source))
		}
		if !driver.SessionProtocolCompatible(executorName, executor, sourceProtocol) {
			return "", "", incompatibleSessionError(node.ID, executorName, mode, sourceExecutor, sourceProtocol)
		}
		return mode, id, nil
	default:
		return "", "", fmt.Errorf("node %s candidate %s has unsupported session mode %q", node.ID, executorName, mode)
	}
}

func incompatibleSessionError(nodeID, executorName, mode, sourceExecutor, sourceProtocol string) error {
	if sourceProtocol == "" {
		return fmt.Errorf("node %s candidate %s cannot %s session from executor %s because its session protocol was not recorded", nodeID, executorName, mode, sourceExecutor)
	}
	return fmt.Errorf("node %s candidate %s cannot %s session from executor %s with protocol %s", nodeID, executorName, mode, sourceExecutor, sourceProtocol)
}

func sourceLabel(source string) string {
	if source == "" {
		return "the incoming node"
	}
	return "node " + source
}

func visitArtifact(run *model.Run, nodeID, suffix string) string {
	return visitArtifactName(nodeID, run.State.Visits[nodeID], suffix)
}

func visitArtifactName(nodeID string, visit int, suffix string) string {
	if visit < 1 {
		return nodeID + "." + suffix
	}
	return fmt.Sprintf("%s.%d.%s", nodeID, visit, suffix)
}

func attemptArtifactName(nodeID string, visit, candidateIndex, candidateAttempt int, suffix string) string {
	base := nodeID
	if visit > 0 {
		base = fmt.Sprintf("%s.%d", nodeID, visit)
	}
	return fmt.Sprintf("%s.candidate-%d.attempt-%d.%s", base, candidateIndex+1, candidateAttempt, suffix)
}

func artifactReference(relativePath string) string {
	return "artifacts/" + filepath.ToSlash(relativePath)
}

func writeJSONArtifact(runDir, relativePath string, value any) error {
	_, err := writeJSONArtifactBytes(runDir, relativePath, value)
	return err
}

func writeJSONArtifactBytes(runDir, relativePath string, value any) (int64, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return 0, err
	}
	data = append(data, '\n')
	return int64(len(data)), store.WriteArtifact(runDir, relativePath, data)
}

func loadResult(file string) (any, int64, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, 0, fmt.Errorf("structured result is missing: write JSON to AGENTFLOW_RESULT_FILE (%s)", file)
	}
	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, int64(len(data)), fmt.Errorf("structured result is invalid JSON: %w", err)
	}
	if _, ok := result.(map[string]any); !ok {
		return nil, int64(len(data)), fmt.Errorf("structured result must be a JSON object")
	}
	return result, int64(len(data)), nil
}

func fileError(path string) error {
	_, err := os.Stat(path)
	return err
}

func intPointer(value int) *int { return &value }

func context(state model.RunState) map[string]any {
	nodes := map[string]any{}
	for id, node := range state.Nodes {
		nodes[id] = map[string]any{
			"status": node.Status, "input": node.Input, "result": node.Result,
			"sessionId": node.SessionID, "sessionProtocol": node.SessionProtocol,
			"selectedExecutor": node.SelectedExecutor, "artifacts": node.Artifacts,
		}
	}
	return map[string]any{"inputs": state.Inputs, "nodes": nodes}
}
