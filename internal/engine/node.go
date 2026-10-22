package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/metrics"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/schema"
	"github.com/chenhg5/agentflow/internal/store"
	"github.com/chenhg5/agentflow/internal/template"
)

// nodeTask is everything a worker needs to run one node. It is built on the
// scheduler goroutine from the current run state and shares nothing with it,
// so several tasks can run at once.
type nodeTask struct {
	runDir             string
	node               model.Node
	state              model.NodeState
	command            []string
	options            driver.CommandOptions
	resultFile         string
	resultRelativePath string
}

// prepareNode renders a node's input, prompt, workspace, session, and command.
// When the node already reaches a final or waiting state here (human nodes,
// input contract or provider errors), the task is nil and the returned state
// is the outcome.
func prepareNode(run *model.Run, node model.Node, providers map[string]driver.Provider) (*nodeTask, model.NodeState, error) {
	state := cloneState(run.State.Nodes[node.ID])
	state.Status = "running"
	state.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	state.LeaseToken = store.NewToken()
	if err := store.AppendEvent(run.RunDir, map[string]any{"type": "node.started", "nodeId": node.ID, "nodeType": node.Type}); err != nil {
		return nil, state, err
	}
	runContext := context(run.State)
	if node.Type == "human" {
		state.Status = "waiting_human"
		state.Input = resolveInput(run, node, runContext)
		inputArtifact := node.ID + ".input.json"
		if err := writeJSONArtifact(run.RunDir, inputArtifact, state.Input); err != nil {
			return nil, state, err
		}
		state.Artifacts["input"] = "artifacts/" + filepath.ToSlash(inputArtifact)
		if problems := schema.ValidateValue("input", state.Input, schema.EffectiveInputSchema(node)); len(problems) > 0 {
			return nil, fail(state, schema.FormatContractErrors("input schema validation failed", problems)), nil
		}
		state.Prompt = template.Render(node.Prompt, runContext)
		inputJSON, _ := json.Marshal(state.Input)
		state.InputBytes = int64(len([]byte(state.Prompt)) + len(inputJSON))
		_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.waiting_human", "nodeId": node.ID, "prompt": state.Prompt})
		return nil, state, nil
	}
	workspace := template.Render(node.Workspace, runContext)
	if workspace == "" {
		workspace = "."
	}
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return nil, state, err
	}
	state.Workspace = workspace
	state.Input = resolveInput(run, node, runContext)
	inputArtifact := node.ID + ".input.json"
	if err := writeJSONArtifact(run.RunDir, inputArtifact, state.Input); err != nil {
		return nil, state, err
	}
	state.Artifacts["input"] = "artifacts/" + filepath.ToSlash(inputArtifact)
	if problems := schema.ValidateValue("input", state.Input, schema.EffectiveInputSchema(node)); len(problems) > 0 {
		return nil, fail(state, schema.FormatContractErrors("input schema validation failed", problems)), nil
	}
	sessionID := template.Render(node.Session, runContext)
	state.SessionID = sessionID
	prompt := template.Render(node.Prompt, runContext)
	inputJSON, err := json.Marshal(state.Input)
	if err != nil {
		return nil, state, err
	}
	state.InputBytes = int64(len([]byte(prompt)) + len(inputJSON))
	resultRelativePath := node.ID + ".result.json"
	if node.Result != nil && node.Result.Path != "" {
		resultRelativePath = node.Result.Path
	}
	resultFile, err := store.ArtifactPath(run.RunDir, resultRelativePath)
	if err != nil {
		return nil, state, err
	}
	if node.Type == "agent" {
		prompt += fmt.Sprintf("\n\nExecution contract: when the task is complete, write one JSON object to AGENTFLOW_RESULT_FILE before exiting successfully. The exact result path for this node is %s. The workspace is %s. Structured node input is %s. Expected input fields are %s. Required output fields are %s. You may use the environment variable AGENTFLOW_RESULT_FILE as well. stdout and stderr are logs only.", resultFile, workspace, string(inputJSON), fieldSummary(node.InputFields), fieldSummary(node.OutputFields))
		state.InputBytes = int64(len([]byte(prompt)) + len(inputJSON))
	}
	env := map[string]string{
		"AGENTFLOW_RUN_ID":      run.RunID,
		"AGENTFLOW_NODE_ID":     node.ID,
		"AGENTFLOW_NODE_TOKEN":  state.LeaseToken,
		"AGENTFLOW_WORKSPACE":   workspace,
		"AGENTFLOW_SESSION_ID":  sessionID,
		"AGENTFLOW_PROMPT":      prompt,
		"AGENTFLOW_RESULT_FILE": resultFile,
	}
	command := node.Command
	if node.Type == "agent" {
		provider, ok := providers[node.Provider]
		if !ok {
			return nil, fail(state, fmt.Sprintf("provider %s is not configured; add .agentflow/providers.json", node.Provider)), nil
		}
		command, err = driver.BuildCommand(node.Provider, provider, driver.Request{
			Prompt: prompt, Workspace: workspace, SessionID: sessionID,
			ResultFile: resultFile, RunID: run.RunID, NodeID: node.ID,
		})
		if err != nil {
			return nil, fail(state, err.Error()), nil
		}
	} else {
		command = template.RenderArray(command, runContext)
	}
	stdin := ""
	if node.PromptMode == "stdin" {
		stdin = prompt
	}
	return &nodeTask{
		runDir:             run.RunDir,
		node:               node,
		state:              cloneState(&state),
		command:            command,
		options:            driver.CommandOptions{Dir: workspace, Env: env, Stdin: stdin, TimeoutMs: node.TimeoutMs},
		resultFile:         resultFile,
		resultRelativePath: resultRelativePath,
	}, state, nil
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
	stdoutArtifact := filepath.Join("logs", node.ID+".stdout.log")
	stderrArtifact := filepath.Join("logs", node.ID+".stderr.log")
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
		if node.Type == "agent" || (node.Result != nil && node.Result.Required) {
			return t.failed(state, err.Error()), nil
		}
		structured = map[string]any{"exitCode": result.Code}
		data, marshalErr := json.MarshalIndent(structured, "", "  ")
		if marshalErr != nil {
			return state, marshalErr
		}
		if err := store.WriteArtifact(t.runDir, t.resultRelativePath, append(data, '\n')); err != nil {
			return state, err
		}
		resultBytes = int64(len(data) + 1)
	}
	state.OutputBytes = resultBytes
	state.Result = structured
	state.Artifacts["result"] = "artifacts/" + filepath.ToSlash(t.resultRelativePath)
	if problems := schema.ValidateValue("output", structured, schema.EffectiveOutputSchema(node)); len(problems) > 0 {
		return t.failed(state, schema.FormatContractErrors("output schema validation failed", problems)), nil
	}
	if resultObject, ok := structured.(map[string]any); ok {
		if returnedSession, ok := resultObject["session_id"].(string); ok && returnedSession != "" {
			state.SessionID = returnedSession
		}
		if returnedSession, ok := resultObject["sessionId"].(string); ok && returnedSession != "" {
			state.SessionID = returnedSession
		}
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

func resolveInput(run *model.Run, node model.Node, runContext map[string]any) any {
	input := map[string]any{}
	for _, field := range inputFields(node) {
		if value, ok := lookupDependencyField(run, node, field); ok {
			input[field] = value
		}
	}
	for target, expression := range node.InputMapping {
		if value, ok := resolveMappingExpression(run, node, expression, runContext); ok {
			input[target] = value
		}
	}
	if node.Input != nil {
		if rendered, ok := template.RenderValue(node.Input, runContext).(map[string]any); ok {
			for key, value := range rendered {
				input[key] = value
			}
		}
	}
	if len(input) == 0 && node.Input == nil && node.InputSchema == nil && len(node.InputFields) == 0 && len(node.InputMapping) == 0 {
		return nil
	}
	return input
}

func inputFields(node model.Node) []string {
	if len(node.InputFields) > 0 {
		fields := make([]string, 0, len(node.InputFields))
		for _, field := range node.InputFields {
			fields = append(fields, field.Name)
		}
		return fields
	}
	properties, _ := node.InputSchema["properties"].(map[string]any)
	fields := make([]string, 0, len(properties))
	for field := range properties {
		fields = append(fields, field)
	}
	return fields
}

func lookupDependencyField(run *model.Run, node model.Node, field string) (any, bool) {
	for _, dependencyID := range node.Needs {
		dependency := run.State.Nodes[dependencyID]
		if result, ok := dependency.Result.(map[string]any); ok {
			if value, exists := result[field]; exists {
				return value, true
			}
		}
	}
	return nil, false
}

func resolveMappingExpression(run *model.Run, node model.Node, expression string, runContext map[string]any) (any, bool) {
	expression = strings.TrimSpace(expression)
	if strings.HasPrefix(expression, "$node.") {
		expression = "nodes." + strings.TrimPrefix(expression, "$node.")
		expression = strings.Replace(expression, ".output.", ".result.", 1)
	} else if strings.HasPrefix(expression, "$output.") {
		field := strings.TrimPrefix(expression, "$output.")
		return lookupDependencyField(run, node, field)
	} else if strings.HasPrefix(expression, "$input.") {
		field := strings.TrimPrefix(expression, "$input.")
		value, ok := run.State.Inputs[field]
		return value, ok
	} else if strings.HasPrefix(expression, "{{") {
		return template.RenderValue(expression, runContext), true
	}
	return template.Lookup(runContext, expression)
}

func fieldSummary(fields []model.FieldSpec) string {
	data, _ := json.Marshal(fields)
	if len(data) == 0 || string(data) == "null" {
		return "[]"
	}
	return string(data)
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
		nodes[id] = map[string]any{"status": node.Status, "input": node.Input, "result": node.Result, "artifacts": node.Artifacts, "prompt": node.Prompt}
	}
	return map[string]any{"inputs": state.Inputs, "nodes": nodes}
}

func anyStatus(nodes []*model.NodeState, status string) bool {
	for _, node := range nodes {
		if node.Status == status {
			return true
		}
	}
	return false
}

func hasWaitingHuman(state model.RunState) bool {
	for _, node := range state.Nodes {
		if node.Status == "waiting_human" {
			return true
		}
	}
	return false
}
