package engine

import (
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
	runDir             string
	node               model.Node
	executor           driver.Executor
	state              model.NodeState
	command            []string
	options            driver.CommandOptions
	resultFile         string
	resultRelativePath string
	visit              int
}

func prepareNode(run *model.Run, node model.Node, executors map[string]driver.Executor) (*nodeTask, model.NodeState, error) {
	state := cloneState(run.State.Nodes[node.ID])
	state.Status = "running"
	state.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	state.LeaseToken = store.NewToken()
	if err := store.AppendEvent(run.RunDir, map[string]any{"type": "node.started", "nodeId": node.ID, "executor": node.Executor}); err != nil {
		return nil, state, err
	}
	state.Input = resolveInput(run, node, context(run.State))
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
	input, _ := state.Input.(map[string]any)
	prepared, err := driver.Prepare(node.Executor, executor, driver.Invocation{Input: input, InputSchema: node.InputSchema, OutputSchema: node.OutputSchema, ResultFile: resultFile, RunID: run.RunID, NodeID: node.ID})
	if err != nil {
		return nil, fail(state, err.Error()), nil
	}
	if prepared.Waiting {
		state.Status = "waiting_input"
		_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.waiting_input", "nodeId": node.ID})
		return nil, state, nil
	}
	state.Workspace, state.SessionID = prepared.Workspace, prepared.SessionID
	env := map[string]string{
		"AGENTFLOW_RUN_ID":      run.RunID,
		"AGENTFLOW_NODE_ID":     node.ID,
		"AGENTFLOW_NODE_TOKEN":  state.LeaseToken,
		"AGENTFLOW_WORKSPACE":   prepared.Workspace,
		"AGENTFLOW_SESSION_ID":  prepared.SessionID,
		"AGENTFLOW_RESULT_FILE": resultFile,
		"AGENTFLOW_INPUT_FILE":  inputFile,
	}
	for key, value := range prepared.Env {
		if _, reserved := env[key]; !reserved {
			env[key] = value
		}
	}
	return &nodeTask{runDir: run.RunDir, node: node, executor: executor, state: cloneState(&state), command: prepared.Command, options: driver.CommandOptions{Dir: prepared.Workspace, Env: env, Stdin: prepared.Stdin, TimeoutMs: node.TimeoutMs}, resultFile: resultFile, resultRelativePath: resultRelativePath, visit: run.State.Visits[node.ID]}, state, nil
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
	if sessionID := driver.SessionFromOutput(t.executor, structured); sessionID != "" {
		state.SessionID = sessionID
	}
	state.Status = "succeeded"
	state.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	metrics.RefreshNode(&state, time.Now().UTC())
	_ = store.AppendEvent(t.runDir, map[string]any{"type": "node.succeeded", "nodeId": node.ID, "resultArtifact": state.Artifacts["result"]})
	return state, nil
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

func visitArtifact(run *model.Run, nodeID, suffix string) string {
	return visitArtifactName(nodeID, run.State.Visits[nodeID], suffix)
}

func visitArtifactName(nodeID string, visit int, suffix string) string {
	if visit < 1 {
		return nodeID + "." + suffix
	}
	return fmt.Sprintf("%s.%d.%s", nodeID, visit, suffix)
}

func resolveInput(_ *model.Run, node model.Node, runContext map[string]any) any {
	if node.Input == nil {
		return map[string]any{}
	}
	return template.RenderValue(node.Input, runContext)
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
		nodes[id] = map[string]any{"status": node.Status, "input": node.Input, "result": node.Result, "artifacts": node.Artifacts}
	}
	return map[string]any{"inputs": state.Inputs, "nodes": nodes}
}
