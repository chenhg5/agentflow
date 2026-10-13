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

func Execute(run *model.Run, providers map[string]driver.Provider) error {
	metrics.MarkRunStarted(&run.State, time.Now().UTC())
	if hasWaitingHuman(run.State) {
		run.State.Status = "waiting_human"
		return store.SaveState(run)
	}
	run.State.Status = "running"
	if err := store.SaveState(run); err != nil {
		return err
	}
	if err := store.AppendEvent(run.RunDir, map[string]any{"type": "run.started", "runId": run.RunID}); err != nil {
		return err
	}
	progressed := true
	for progressed {
		progressed = false
		for _, node := range run.Workflow.Nodes {
			current := run.State.Nodes[node.ID]
			if current.Status != "pending" {
				continue
			}
			dependencies := make([]*model.NodeState, 0, len(node.Needs))
			blocked := false
			for _, dependencyID := range node.Needs {
				dependency := run.State.Nodes[dependencyID]
				dependencies = append(dependencies, dependency)
				if dependency.Status == "pending" || dependency.Status == "running" || dependency.Status == "waiting_human" {
					blocked = true
				}
			}
			if blocked {
				continue
			}
			if anyStatus(dependencies, "failed") {
				current.Status = "skipped"
				current.Reason = "dependency_failed"
				progressed = true
				continue
			}
			if anyStatus(dependencies, "skipped") {
				current.Status = "skipped"
				current.Reason = "dependency_skipped"
				progressed = true
				continue
			}
			if !template.EvaluateWhen(node.When, context(run.State)) {
				current.Status = "skipped"
				current.Reason = "when_false"
				progressed = true
				_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.skipped", "nodeId": node.ID, "reason": current.Reason})
				continue
			}
			result, err := executeNode(run, node, providers)
			if err != nil {
				return err
			}
			*current = result
			progressed = true
			if err := store.SaveState(run); err != nil {
				return err
			}
			if result.Status == "waiting_human" {
				run.State.Status = "waiting_human"
				return store.SaveState(run)
			}
			if result.Status == "failed" {
				run.State.Status = "failed"
				metrics.MarkRunFinished(&run.State, time.Now().UTC())
				return store.SaveState(run)
			}
		}
	}
	for _, node := range run.State.Nodes {
		if node.Status == "pending" {
			run.State.Status = "failed"
			metrics.MarkRunFinished(&run.State, time.Now().UTC())
			return store.SaveState(run)
		}
	}
	run.State.Status = "succeeded"
	metrics.MarkRunFinished(&run.State, time.Now().UTC())
	if err := store.SaveState(run); err != nil {
		return err
	}
	return store.AppendEvent(run.RunDir, map[string]any{"type": "run.succeeded", "runId": run.RunID})
}

func executeNode(run *model.Run, node model.Node, providers map[string]driver.Provider) (model.NodeState, error) {
	state := run.State.Nodes[node.ID]
	state.Status = "running"
	if state.Artifacts == nil {
		state.Artifacts = map[string]string{}
	}
	state.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	state.LeaseToken = store.NewToken()
	if err := store.AppendEvent(run.RunDir, map[string]any{"type": "node.started", "nodeId": node.ID, "nodeType": node.Type}); err != nil {
		return *state, err
	}
	if node.Type == "human" {
		state.Status = "waiting_human"
		runContext := context(run.State)
		state.Input = resolveInput(run, node, runContext)
		inputArtifact := node.ID + ".input.json"
		if err := writeJSONArtifact(run.RunDir, inputArtifact, state.Input); err != nil {
			return *state, err
		}
		state.Artifacts["input"] = "artifacts/" + filepath.ToSlash(inputArtifact)
		if problems := schema.ValidateValue("input", state.Input, schema.EffectiveInputSchema(node)); len(problems) > 0 {
			state.Status = "failed"
			state.Error = schema.FormatContractErrors("input schema validation failed", problems)
			state.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
			metrics.RefreshNode(state, time.Now().UTC())
			return *state, nil
		}
		state.Prompt = template.Render(node.Prompt, runContext)
		inputJSON, _ := json.Marshal(state.Input)
		state.InputBytes = int64(len([]byte(state.Prompt)) + len(inputJSON))
		_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.waiting_human", "nodeId": node.ID, "prompt": state.Prompt})
		return *state, nil
	}
	workspace := template.Render(node.Workspace, context(run.State))
	if workspace == "" {
		workspace = "."
	}
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return *state, err
	}
	state.Workspace = workspace
	runContext := context(run.State)
	state.Input = resolveInput(run, node, runContext)
	inputArtifact := node.ID + ".input.json"
	if err := writeJSONArtifact(run.RunDir, inputArtifact, state.Input); err != nil {
		return *state, err
	}
	state.Artifacts["input"] = "artifacts/" + filepath.ToSlash(inputArtifact)
	if problems := schema.ValidateValue("input", state.Input, schema.EffectiveInputSchema(node)); len(problems) > 0 {
		state.Status = "failed"
		state.Error = schema.FormatContractErrors("input schema validation failed", problems)
		state.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		metrics.RefreshNode(state, time.Now().UTC())
		return *state, nil
	}
	sessionID := template.Render(node.Session, runContext)
	state.SessionID = sessionID
	prompt := template.Render(node.Prompt, runContext)
	inputJSON, marshalErr := json.Marshal(state.Input)
	if marshalErr != nil {
		return *state, marshalErr
	}
	state.InputBytes = int64(len([]byte(prompt)) + len(inputJSON))
	resultRelativePath := node.ID + ".result.json"
	if node.Result != nil && node.Result.Path != "" {
		resultRelativePath = node.Result.Path
	}
	resultFile, err := store.ArtifactPath(run.RunDir, resultRelativePath)
	if err != nil {
		return *state, err
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
			state.Status = "failed"
			state.Error = fmt.Sprintf("provider %s is not configured; add .agentflow/providers.json", node.Provider)
			state.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
			metrics.RefreshNode(state, time.Now().UTC())
			return *state, nil
		}
		command, err = driver.BuildCommand(node.Provider, provider, driver.Request{
			Prompt: prompt, Workspace: workspace, SessionID: sessionID,
			ResultFile: resultFile, RunID: run.RunID, NodeID: node.ID,
		})
		if err != nil {
			state.Status = "failed"
			state.Error = err.Error()
			state.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
			metrics.RefreshNode(state, time.Now().UTC())
			return *state, nil
		}
	} else {
		command = template.RenderArray(command, runContext)
	}
	maxAttempts := node.Retry.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var result driver.CommandResult
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		state.Attempts++
		_ = os.Remove(resultFile)
		stdin := ""
		if node.PromptMode == "stdin" {
			stdin = prompt
		}
		result = driver.RunCommand(command, driver.CommandOptions{Dir: workspace, Env: env, Stdin: stdin, TimeoutMs: node.TimeoutMs})
		if result.Code == 0 {
			break
		}
		if attempt < maxAttempts {
			_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.retrying", "nodeId": node.ID, "attempt": attempt, "code": result.Code})
		}
	}
	stdoutArtifact := filepath.Join("logs", node.ID+".stdout.log")
	stderrArtifact := filepath.Join("logs", node.ID+".stderr.log")
	if err := store.WriteArtifact(run.RunDir, stdoutArtifact, []byte(result.Stdout)); err != nil {
		return *state, err
	}
	if err := store.WriteArtifact(run.RunDir, stderrArtifact, []byte(result.Stderr)); err != nil {
		return *state, err
	}
	state.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	state.StdoutBytes = int64(len([]byte(result.Stdout)))
	state.StderrBytes = int64(len([]byte(result.Stderr)))
	if result.Code != 0 {
		state.Status = "failed"
		if result.TimedOut {
			state.Error = "command timed out after " + strconv.Itoa(node.TimeoutMs) + "ms"
		} else {
			state.Error = fmt.Sprintf("command exited with code %d", result.Code)
		}
		_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.failed", "nodeId": node.ID, "error": state.Error})
		state.Artifacts["stdoutLog"] = "artifacts/" + filepath.ToSlash(stdoutArtifact)
		state.Artifacts["stderrLog"] = "artifacts/" + filepath.ToSlash(stderrArtifact)
		metrics.RefreshNode(state, time.Now().UTC())
		return *state, nil
	}
	structured, resultBytes, err := loadResult(resultFile)
	if err != nil {
		state.OutputBytes = resultBytes
		if node.Type == "agent" || (node.Result != nil && node.Result.Required) {
			state.Status = "failed"
			state.Error = err.Error()
			state.Artifacts["stdoutLog"] = "artifacts/" + filepath.ToSlash(stdoutArtifact)
			state.Artifacts["stderrLog"] = "artifacts/" + filepath.ToSlash(stderrArtifact)
			_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.failed", "nodeId": node.ID, "error": state.Error})
			metrics.RefreshNode(state, time.Now().UTC())
			return *state, nil
		}
		structured = map[string]any{"exitCode": result.Code}
		data, marshalErr := json.MarshalIndent(structured, "", "  ")
		if marshalErr != nil {
			return *state, marshalErr
		}
		if err := store.WriteArtifact(run.RunDir, resultRelativePath, append(data, '\n')); err != nil {
			return *state, err
		}
		resultBytes = int64(len(data) + 1)
	}
	state.OutputBytes = resultBytes
	state.Result = structured
	if problems := schema.ValidateValue("output", structured, schema.EffectiveOutputSchema(node)); len(problems) > 0 {
		state.Status = "failed"
		state.Error = schema.FormatContractErrors("output schema validation failed", problems)
		state.Artifacts["result"] = "artifacts/" + filepath.ToSlash(resultRelativePath)
		state.Artifacts["stdoutLog"] = "artifacts/" + filepath.ToSlash(stdoutArtifact)
		state.Artifacts["stderrLog"] = "artifacts/" + filepath.ToSlash(stderrArtifact)
		_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.failed", "nodeId": node.ID, "error": state.Error})
		metrics.RefreshNode(state, time.Now().UTC())
		return *state, nil
	}
	if resultObject, ok := structured.(map[string]any); ok {
		if returnedSession, ok := resultObject["session_id"].(string); ok && returnedSession != "" {
			state.SessionID = returnedSession
		}
		if returnedSession, ok := resultObject["sessionId"].(string); ok && returnedSession != "" {
			state.SessionID = returnedSession
		}
	}
	state.Artifacts["result"] = "artifacts/" + filepath.ToSlash(resultRelativePath)
	state.Artifacts["stdoutLog"] = "artifacts/" + filepath.ToSlash(stdoutArtifact)
	state.Artifacts["stderrLog"] = "artifacts/" + filepath.ToSlash(stderrArtifact)
	state.Status = "succeeded"
	metrics.RefreshNode(state, time.Now().UTC())
	_ = store.AppendEvent(run.RunDir, map[string]any{"type": "node.succeeded", "nodeId": node.ID, "resultArtifact": state.Artifacts["result"]})
	return *state, nil
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
