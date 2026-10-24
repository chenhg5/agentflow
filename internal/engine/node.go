package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/metrics"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/schema"
	"github.com/chenhg5/agentflow/internal/store"
	"github.com/chenhg5/agentflow/internal/template"
)

// nodeTask invokes one executor using the same file contract for every node.
type nodeTask struct {
	runDir              string
	node                model.Node
	executor            driver.Executor
	state               model.NodeState
	command             []string
	options             driver.CommandOptions
	resultFile          string
	sessionFile         string
	sessionRelativePath string
	resultRelativePath  string
	visit               int
}

func prepareNode(run *model.Run, node model.Node, executors map[string]driver.Executor) (*nodeTask, model.NodeState, error) {
	state := cloneState(run.State.Nodes[node.ID])
	state.Status = "running"
	state.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	state.LeaseToken = store.NewToken()
	if err := store.AppendEvent(run.RunDir, map[string]any{"type": "node.started", "nodeId": node.ID, "executor": node.Executor}); err != nil {
		return nil, state, err
	}
	if state.Input == nil {
		state.Input = map[string]any{}
	}
	inputArtifact := visitArtifact(run, node.ID, "input.json")
	if err := writeJSONArtifact(run.RunDir, inputArtifact, state.Input); err != nil {
		return nil, state, err
	}
	state.Artifacts["input"] = "artifacts/" + filepath.ToSlash(inputArtifact)
	inputFile, err := store.ArtifactPath(run.RunDir, inputArtifact)
	if err != nil {
		return nil, state, err
	}
	if problems := schema.ValidateValue("input", state.Input, node.InputSchema); len(problems) > 0 {
		return nil, fail(state, schema.FormatContractErrors("input schema validation failed", problems)), nil
	}
	inputJSON, _ := json.Marshal(state.Input)
	state.InputBytes = int64(len(inputJSON))
	executor, ok := executors[node.Executor]
	if !ok {
		return nil, fail(state, fmt.Sprintf("executor %s is not configured; add .agentflow/executors.json", node.Executor)), nil
	}
	resultRelativePath := visitArtifact(run, node.ID, "result.json")
	resultFile, err := store.ArtifactPath(run.RunDir, resultRelativePath)
	if err != nil {
		return nil, state, err
	}
	sessionRelativePath := visitArtifact(run, node.ID, "session.txt")
	sessionFile, err := store.ArtifactPath(run.RunDir, sessionRelativePath)
	if err != nil {
		return nil, state, err
	}
	input, ok := state.Input.(map[string]any)
	if !ok {
		return nil, fail(state, "node input must be a JSON object"), nil
	}
	runContext := context(run.State)
	runContext["input"] = state.Input
	config, _ := template.RenderValue(node.Config, runContext).(map[string]any)
	configArtifact := visitArtifact(run, node.ID, "config.json")
	if err := writeJSONArtifact(run.RunDir, configArtifact, config); err != nil {
		return nil, state, err
	}
	state.Artifacts["config"] = "artifacts/" + filepath.ToSlash(configArtifact)
	configFile, err := store.ArtifactPath(run.RunDir, configArtifact)
	if err != nil {
		return nil, state, err
	}
	sessionMode, sessionID, err := sessionForNode(run, node, state, runContext)
	if err != nil {
		return nil, fail(state, err.Error()), nil
	}
	prepared, err := driver.Prepare(node.Executor, executor, driver.Invocation{Input: input, Config: config, InputSchema: node.InputSchema, OutputSchema: node.OutputSchema, SessionMode: sessionMode, SessionID: sessionID, ResultFile: resultFile, RunID: run.RunID, NodeID: node.ID})
	if err != nil {
		return nil, fail(state, err.Error()), nil
	}
	if prepared.Waiting {
		state.Status = "waiting_input"
		state.SessionID = sessionID
		_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.waiting_input", "nodeId": node.ID})
		return nil, state, nil
	}
	state.Workspace, state.SessionID = prepared.Workspace, prepared.SessionID
	env := map[string]string{
		"AGENTFLOW_RUN_ID":       run.RunID,
		"AGENTFLOW_NODE_ID":      node.ID,
		"AGENTFLOW_NODE_TOKEN":   state.LeaseToken,
		"AGENTFLOW_WORKSPACE":    prepared.Workspace,
		"AGENTFLOW_SESSION_ID":   prepared.SessionID,
		"AGENTFLOW_SESSION_MODE": prepared.SessionMode,
		"AGENTFLOW_RESULT_FILE":  resultFile,
		"AGENTFLOW_INPUT_FILE":   inputFile,
		"AGENTFLOW_CONFIG_FILE":  configFile,
		"AGENTFLOW_SESSION_FILE": sessionFile,
	}
	for key, value := range prepared.Env {
		if _, reserved := env[key]; !reserved {
			env[key] = value
		}
	}
	return &nodeTask{runDir: run.RunDir, node: node, executor: executor, state: cloneState(&state), command: prepared.Command, options: driver.CommandOptions{Dir: prepared.Workspace, Env: env, Stdin: prepared.Stdin, TimeoutMs: node.TimeoutMs}, resultFile: resultFile, resultRelativePath: resultRelativePath, sessionFile: sessionFile, sessionRelativePath: sessionRelativePath, visit: run.State.Visits[node.ID]}, state, nil
}

// run executes the command with retries and turns its result file into the
// node's final state. It only touches the task's own state and files.
func (t *nodeTask) run() (model.NodeState, error) {
	node, state := t.node, t.state
	maxAttempts := node.Retry.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var result driver.CommandResult
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		state.Attempts++
		_ = os.Remove(t.resultFile)
		_ = os.Remove(t.sessionFile)
		result = driver.RunCommand(t.command, t.options)
		if result.Code == 0 {
			break
		}
		if attempt < maxAttempts {
			_ = store.AppendEvent(t.runDir, map[string]any{"type": "node.retrying", "nodeId": node.ID, "attempt": attempt, "code": result.Code})
		}
	}
	stdoutArtifact := filepath.Join("logs", visitArtifactName(t.node.ID, t.visit, "stdout.log"))
	stderrArtifact := filepath.Join("logs", visitArtifactName(t.node.ID, t.visit, "stderr.log"))
	if err := store.WriteArtifact(t.runDir, stdoutArtifact, []byte(result.Stdout)); err != nil {
		return state, err
	}
	if err := store.WriteArtifact(t.runDir, stderrArtifact, []byte(result.Stderr)); err != nil {
		return state, err
	}
	state.StdoutBytes = int64(len([]byte(result.Stdout)))
	state.StderrBytes = int64(len([]byte(result.Stderr)))
	state.Artifacts["stdoutLog"] = "artifacts/" + filepath.ToSlash(stdoutArtifact)
	state.Artifacts["stderrLog"] = "artifacts/" + filepath.ToSlash(stderrArtifact)
	if result.Code != 0 {
		message := fmt.Sprintf("command exited with code %d", result.Code)
		if result.TimedOut {
			message = "command timed out after " + strconv.Itoa(node.TimeoutMs) + "ms"
		}
		return t.failed(state, message), nil
	}
	structured, resultBytes, err := loadResult(t.resultFile)
	if err != nil {
		state.OutputBytes = resultBytes
		return t.failed(state, err.Error()), nil
	}
	state.OutputBytes = resultBytes
	state.Result = structured
	state.Artifacts["result"] = "artifacts/" + filepath.ToSlash(t.resultRelativePath)
	if problems := schema.ValidateValue("output", structured, schema.EffectiveOutputSchema(node)); len(problems) > 0 {
		return t.failed(state, schema.FormatContractErrors("output schema validation failed", problems)), nil
	}
	if sessionID := sessionFromFile(t.sessionFile); sessionID != "" {
		state.SessionID = sessionID
		state.Artifacts["session"] = "artifacts/" + filepath.ToSlash(t.sessionRelativePath)
	} else if sessionID := driver.SessionFromLogs(t.executor, result.Stdout); sessionID != "" {
		state.SessionID = sessionID
	}
	if t.node.Execution.Session.Mode == "fork" && (state.SessionID == "" || state.SessionID == t.options.Env["AGENTFLOW_SESSION_ID"]) {
		return t.failed(state, "fork executor must report a new session ID in AGENTFLOW_SESSION_FILE or native JSON logs"), nil
	}
	if t.node.Execution.Session.Mode == "fixed" && state.SessionID == "" {
		return t.failed(state, "fixed session executor must report a session ID in AGENTFLOW_SESSION_FILE or native JSON logs"), nil
	}
	state.Status = "succeeded"
	state.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	metrics.RefreshNode(&state, time.Now().UTC())
	_ = store.AppendEvent(t.runDir, map[string]any{"type": "node.succeeded", "nodeId": node.ID, "resultArtifact": state.Artifacts["result"]})
	return state, nil
}

func sessionFromFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(bytes.TrimSpace(data))
}

func (t *nodeTask) failed(state model.NodeState, message string) model.NodeState {
	state = fail(state, message)
	_ = store.AppendEvent(t.runDir, map[string]any{"type": "node.failed", "nodeId": t.node.ID, "error": message})
	return state
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
	clone.Artifacts = make(map[string]string, len(state.Artifacts))
	for key, value := range state.Artifacts {
		clone.Artifacts[key] = value
	}
	return clone
}

// cloneValue keeps edge payloads isolated from the previous node's result.
// Results are required to be JSON objects, so a JSON round trip gives the
// engine a simple deep copy without coupling it to executor-specific types.
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

func sessionForNode(run *model.Run, node model.Node, state model.NodeState, runContext map[string]any) (string, string, error) {
	spec := node.Execution.Session
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
				return "", "", fmt.Errorf("node %s fixed session id resolved to an empty value", node.ID)
			}
			return mode, id, nil
		}
		for index := len(run.State.History) - 1; index >= 0; index-- {
			visit := run.State.History[index]
			if visit.NodeID == node.ID && visit.State.SessionID != "" {
				return mode, visit.State.SessionID, nil
			}
		}
		return mode, "", nil
	case "inherit", "fork":
		source := spec.From
		if source == "" {
			source = state.IncomingNode
		}
		id := ""
		if source == state.IncomingNode {
			id = state.IncomingSessionID
		}
		if source != "" {
			if sourceState := run.State.Nodes[source]; sourceState != nil {
				id = sourceState.SessionID
			}
			if id == "" {
				for index := len(run.State.History) - 1; index >= 0; index-- {
					visit := run.State.History[index]
					if visit.NodeID == source && visit.State.SessionID != "" {
						id = visit.State.SessionID
						break
					}
				}
			}
		}
		if id == "" {
			return "", "", fmt.Errorf("node %s session mode %s needs a session from %s", node.ID, mode, sourceLabel(source))
		}
		return mode, id, nil
	default:
		return "", "", fmt.Errorf("node %s has unsupported session mode %q", node.ID, mode)
	}
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

func writeJSONArtifact(runDir, relativePath string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteArtifact(runDir, relativePath, append(data, '\n'))
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

func context(state model.RunState) map[string]any {
	nodes := map[string]any{}
	for id, node := range state.Nodes {
		nodes[id] = map[string]any{"status": node.Status, "input": node.Input, "result": node.Result, "sessionId": node.SessionID, "artifacts": node.Artifacts}
	}
	return map[string]any{"inputs": state.Inputs, "nodes": nodes}
}
