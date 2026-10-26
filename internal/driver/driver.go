package driver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chenhg5/agentflow/internal/template"
)

type Executor struct {
	Environment *EnvironmentPolicy `json:"environment,omitempty"`
	Env         map[string]string  `json:"env,omitempty"`
	Workspace   string             `json:"workspace,omitempty"`
	PromptMode  string             `json:"promptMode,omitempty"`
	// Kind selects a built-in executor implementation. Supported values are
	// claude-code, codex, and command. When Command is set it takes precedence
	// and preserves an explicitly configured command executor.
	Kind string `json:"kind,omitempty"`
	// Binary overrides the executable used by a built-in executor.
	Binary string `json:"binary,omitempty"`
	Model  string `json:"model,omitempty"`
	// Sandbox and executor-specific execution switches are intentionally kept
	// as configuration instead of being embedded in workflow JSON.
	Sandbox                    string   `json:"sandbox,omitempty"`
	DangerouslyBypassApprovals bool     `json:"dangerouslyBypassApprovalsAndSandbox,omitempty"`
	SkipGitRepoCheck           bool     `json:"skipGitRepoCheck,omitempty"`
	Ephemeral                  bool     `json:"ephemeral,omitempty"`
	ExtraArgs                  []string `json:"extraArgs,omitempty"`
	// Command starts a new agent session.
	Command []string `json:"command,omitempty"`
	// ResumeCommand sends a prompt to an existing session.
	ResumeCommand []string `json:"resumeCommand,omitempty"`
	// ForkCommand starts a new session from an existing session.
	ForkCommand []string `json:"forkCommand,omitempty"`
}

// Request contains the runtime values needed to start or resume a executor
// session. The workflow engine owns these values; executors only translate
// them into their native CLI invocation.
type Request struct {
	Prompt      string
	SessionMode string
	Workspace   string
	SessionID   string
	ResultFile  string
	RunID       string
	NodeID      string
}

// Status describes whether a executor's executable is available on the host.
// It is used by `agentflow doctor`; availability is checked separately from
// model authentication, which only the executor CLI can verify.
type Status struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Binary    string `json:"binary"`
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

type CommandOptions struct {
	Environment *EnvironmentPolicy
	Dir         string
	Env         map[string]string
	Stdin       string
	TimeoutMs   int
}

type CommandResult struct {
	Code     int
	Signal   string
	Stdout   string
	Stderr   string
	TimedOut bool
	Started  bool
	Error    string
}

// LoadExecutors reads this standalone workspace's registrations over the
// built-ins. It does not inherit registrations from other workspaces.
func LoadExecutors(cwd string) (map[string]Executor, error) {
	executors := BuiltInExecutors()
	file := filepath.Join(cwd, "executors.json")
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return executors, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read executors file %s: %w", file, err)
	}
	var config struct {
		Executors map[string]Executor `json:"executors"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("invalid executors file %s: %w", file, err)
	}
	if config.Executors == nil {
		return nil, fmt.Errorf("executor config %s must contain an executors object", file)
	}
	var raw struct {
		Executors map[string]map[string]json.RawMessage `json:"executors"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	for name, configuredExecutor := range config.Executors {
		for field := range raw.Executors[name] {
			if strings.EqualFold(field, "environment") && field != "environment" {
				return nil, fmt.Errorf("executor %s: use the environment field with its schema spelling", name)
			}
		}
		if configuredExecutor.Environment != nil && raw.Executors[name]["environment"] == nil {
			return nil, fmt.Errorf("executor %s: use the environment field with its schema spelling", name)
		}
		if value, ok := raw.Executors[name]["environment"]; ok {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return nil, fmt.Errorf("executor %s: environment must be an object", name)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(value, &fields); err != nil {
				return nil, fmt.Errorf("executor %s: invalid environment object", name)
			}
			for field := range fields {
				if field != "mode" && field != "inherit" {
					return nil, fmt.Errorf("executor %s: unknown environment policy field", name)
				}
			}
			if inherited, ok := fields["inherit"]; ok && bytes.Equal(bytes.TrimSpace(inherited), []byte("null")) {
				return nil, fmt.Errorf("executor %s: environment.inherit must be an array", name)
			}
		}
		if err := ValidateEnvironment(configuredExecutor); err != nil {
			return nil, fmt.Errorf("executor %s: %w", name, err)
		}
		if configuredExecutor.Kind == "" && len(configuredExecutor.Command) > 0 {
			configuredExecutor.Kind = "command"
		}
		if base, ok := executors[name]; ok {
			executors[name] = merge(base, configuredExecutor)
		} else {
			if configuredExecutor.Kind == "" {
				configuredExecutor.Kind = "command"
			}
			executors[name] = configuredExecutor
		}
	}
	return executors, nil
}

// BuiltInExecutors returns executors that can run without a project-local
// executors.json. Configuration can override their binary and execution
// options while workflow definitions keep stable executor names.
func BuiltInExecutors() map[string]Executor {
	return map[string]Executor{
		"claude-code": {Kind: "claude-code", Binary: "claude"},
		"codex":       {Kind: "codex", Binary: "codex"},
		"process":     {Kind: "command"},
		"external":    {Kind: "external"},
	}
}

// CheckExecutor verifies that the executable needed by a executor can be
// resolved. It deliberately does not make a network request or consume model
// quota.
func CheckExecutor(name string, executor Executor) Status {
	if executor.Kind == "external" || (executor.Kind == "command" && len(executor.Command) == 0 && len(executor.ResumeCommand) == 0 && len(executor.ForkCommand) == 0) {
		return Status{Name: name, Kind: executor.Kind, Available: true}
	}
	kind := executor.Kind
	if kind == "" {
		kind = name
	}
	binary := executor.Binary
	if len(executor.Command) > 0 {
		binary = executor.Command[0]
	} else if len(executor.ResumeCommand) > 0 {
		binary = executor.ResumeCommand[0]
	} else if len(executor.ForkCommand) > 0 {
		binary = executor.ForkCommand[0]
	}
	if binary == "" {
		switch kind {
		case "claude-code":
			binary = "claude"
		case "codex":
			binary = "codex"
		}
	}
	status := Status{Name: name, Kind: kind, Binary: binary}
	if binary == "" {
		status.Error = "no binary or command configured"
		return status
	}
	lookup := binary
	if !filepath.IsAbs(binary) && strings.ContainsAny(binary, `/\`) {
		lookup, _ = filepath.Abs(filepath.Join(executor.Workspace, binary))
	}
	if _, err := exec.LookPath(lookup); err != nil {
		status.Error = err.Error()
		return status
	}
	status.Available = true
	return status
}

func merge(base, override Executor) Executor {
	if override.Environment != nil {
		base.Environment = override.Environment
	}
	if override.Env != nil {
		base.Env = override.Env
	}
	if override.Workspace != "" {
		base.Workspace = override.Workspace
	}
	if override.PromptMode != "" {
		base.PromptMode = override.PromptMode
	}
	if override.Kind != "" {
		base.Kind = override.Kind
	}
	if override.Binary != "" {
		base.Binary = override.Binary
	}
	if override.Model != "" {
		base.Model = override.Model
	}
	if override.Sandbox != "" {
		base.Sandbox = override.Sandbox
	}
	if override.DangerouslyBypassApprovals {
		base.DangerouslyBypassApprovals = true
	}
	if override.SkipGitRepoCheck {
		base.SkipGitRepoCheck = true
	}
	if override.Ephemeral {
		base.Ephemeral = true
	}
	if len(override.ExtraArgs) > 0 {
		base.ExtraArgs = override.ExtraArgs
	}
	if len(override.Command) > 0 {
		base.Command = override.Command
	}
	if len(override.ResumeCommand) > 0 {
		base.ResumeCommand = override.ResumeCommand
	}
	if len(override.ForkCommand) > 0 {
		base.ForkCommand = override.ForkCommand
	}
	return base
}

// Invocation is the generic JSON/file contract shared by every executor.
type Invocation struct {
	Input        map[string]any
	Config       map[string]any
	InputSchema  map[string]any
	OutputSchema map[string]any
	SessionMode  string
	SessionID    string
	ResultFile   string
	RunID        string
	NodeID       string
}

type Prepared struct {
	Environment *EnvironmentPolicy
	Env         map[string]string
	Command     []string
	Workspace   string
	SessionMode string
	SessionID   string
	Stdin       string
	Waiting     bool
}

// Prepare owns adapter-specific input conventions. The graph engine only
// receives a process to invoke or a request to await external JSON output.
func Prepare(name string, executor Executor, invocation Invocation) (Prepared, error) {
	if err := ValidateEnvironment(executor); err != nil {
		return Prepared{}, err
	}
	if executor.Kind == "external" {
		if len(executor.Command) > 0 {
			return Prepared{}, fmt.Errorf("external executor %s cannot define command", name)
		}
		return Prepared{Waiting: true}, nil
	}
	context := map[string]any{"input": invocation.Input, "config": invocation.Config}
	workspace := template.Render(executor.Workspace, context)
	isAgent := executor.Kind == "codex" || executor.Kind == "claude-code"
	if workspace == "" && isAgent {
		workspace, _ = invocation.Config["workspace"].(string)
	}
	if workspace == "" {
		workspace = "."
	}
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return Prepared{}, err
	}
	prepared := Prepared{Workspace: workspace, Env: map[string]string{}, Environment: executor.Environment}
	for key, value := range executor.Env {
		rendered := template.Render(value, context)
		if strings.ContainsRune(rendered, 0) {
			return Prepared{}, fmt.Errorf("executor.env contains a rendered value with a NUL character")
		}
		prepared.Env[key] = rendered
	}
	if !isAgent {
		if executor.Kind != "command" && executor.Kind != "" {
			return Prepared{}, fmt.Errorf("executor %s has unsupported kind %q", name, executor.Kind)
		}
		prepared.SessionMode = invocation.SessionMode
		prepared.SessionID = invocation.SessionID
		if prepared.SessionID != "" {
			prepared.Command, err = BuildCommand(name, executor, Request{SessionMode: prepared.SessionMode, Workspace: workspace, SessionID: prepared.SessionID, ResultFile: invocation.ResultFile, RunID: invocation.RunID, NodeID: invocation.NodeID})
			return prepared, err
		}
		command := executor.Command
		if len(command) == 0 {
			switch values := invocation.Config["command"].(type) {
			case []string:
				command = values
			case []any:
				for _, value := range values {
					part, ok := value.(string)
					if !ok {
						return Prepared{}, fmt.Errorf("executor %s config.command must contain strings", name)
					}
					command = append(command, part)
				}
			}
		}
		if len(command) == 0 {
			return Prepared{}, fmt.Errorf("executor %s needs a configured command or config.command", name)
		}
		prepared.Command = template.RenderArray(command, context)
		return prepared, nil
	}
	prompt := instruction(invocation.Config)
	if strings.TrimSpace(prompt) == "" {
		prompt = "Process the structured input according to the executor instructions."
	}
	inputJSON, _ := json.Marshal(invocation.Input)
	inputSchema, _ := json.Marshal(invocation.InputSchema)
	outputSchema, _ := json.Marshal(invocation.OutputSchema)
	prompt += fmt.Sprintf("\n\nExecution contract: write one JSON object to AGENTFLOW_RESULT_FILE before exiting successfully. Exact result path: %s. Workspace: %s. Structured input: %s. Input schema: %s. Output schema: %s. stdout and stderr are logs only.", invocation.ResultFile, workspace, inputJSON, inputSchema, outputSchema)
	prepared.Env["AGENTFLOW_PROMPT"] = prompt
	prepared.SessionMode = invocation.SessionMode
	prepared.SessionID = invocation.SessionID
	prepared.Command, err = BuildCommand(name, executor, Request{Prompt: prompt, SessionMode: prepared.SessionMode, Workspace: workspace, SessionID: prepared.SessionID, ResultFile: invocation.ResultFile, RunID: invocation.RunID, NodeID: invocation.NodeID})
	if executor.PromptMode == "stdin" {
		prepared.Stdin = prompt
	}
	return prepared, err
}

func instruction(config map[string]any) string {
	for _, key := range []string{"instruction", "instructions"} {
		if value, ok := config[key].(string); ok {
			return value
		}
	}
	return ""
}

// SessionFromLogs extracts adapter metadata from native machine-readable logs.
// Custom executors may instead write a session ID to AGENTFLOW_SESSION_FILE.
func SessionFromLogs(executor Executor, logs string) string {
	if executor.Kind == "claude-code" {
		var object map[string]any
		if json.Unmarshal([]byte(logs), &object) == nil {
			return stringValue(object, "session_id", "sessionId")
		}
	}
	if executor.Kind == "codex" {
		scanner := bufio.NewScanner(strings.NewReader(logs))
		for scanner.Scan() {
			var object map[string]any
			if json.Unmarshal(scanner.Bytes(), &object) != nil {
				continue
			}
			if id := stringValue(object, "session_id", "sessionId", "thread_id", "threadId"); id != "" {
				return id
			}
			if object["type"] == "thread.started" {
				if id := stringValue(object, "thread_id", "threadId"); id != "" {
					return id
				}
			}
		}
	}
	return ""
}

// SessionCompatible reports whether an implicit inherit/fork session may be
// passed between two registrations. Native provider aliases are compatible;
// custom commands must keep the exact same registered identity.
func SessionCompatible(targetName string, target Executor, sourceName string, source Executor) bool {
	return SessionProtocol(targetName, target) == SessionProtocol(sourceName, source)
}

// SessionProtocol is the durable identity of the adapter that created a
// session. Native aliases share a provider protocol; custom commands are
// compatible only with the exact registered executor identity.
func SessionProtocol(name string, executor Executor) string {
	switch executor.Kind {
	case "codex", "claude-code":
		return "native:" + executor.Kind
	case "command", "":
		return "command:" + name
	default:
		return "kind:" + executor.Kind + ":" + name
	}
}

// SessionProtocolCompatible compares a target's current adapter against the
// protocol persisted when the source session was created. It deliberately
// does not consult the source's mutable executor registration.
func SessionProtocolCompatible(targetName string, target Executor, sourceProtocol string) bool {
	return sourceProtocol != "" && SessionProtocol(targetName, target) == sourceProtocol
}

func stringValue(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := object[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

// BuildCommand translates an executor request into an executable command.
// Explicit command templates take precedence, which keeps the generic
// executor useful for custom agent CLIs and test doubles.
func BuildCommand(name string, executor Executor, request Request) ([]string, error) {
	if executor.Kind == "codex" && executor.Ephemeral && request.SessionID != "" {
		return nil, fmt.Errorf("executor %s cannot use an existing session while ephemeral is enabled", name)
	}
	if request.SessionMode == "fork" && request.SessionID != "" && len(executor.ForkCommand) > 0 {
		return ExpandCommand(executor.ForkCommand, request.Prompt, request.Workspace, request.SessionID, request.ResultFile, request.RunID, request.NodeID), nil
	}
	if request.SessionMode != "fork" && request.SessionID != "" && len(executor.ResumeCommand) > 0 {
		return ExpandCommand(executor.ResumeCommand, request.Prompt, request.Workspace, request.SessionID, request.ResultFile, request.RunID, request.NodeID), nil
	}
	if request.SessionID == "" && len(executor.Command) > 0 {
		return ExpandCommand(executor.Command, request.Prompt, request.Workspace, request.SessionID, request.ResultFile, request.RunID, request.NodeID), nil
	}
	if request.SessionMode == "fork" && request.SessionID != "" && len(executor.ForkCommand) == 0 && len(executor.Command) > 0 {
		return nil, fmt.Errorf("executor %s does not define forkCommand for session %s; configure a command that forks an existing session", name, request.SessionID)
	}
	if request.SessionID != "" && len(executor.ResumeCommand) == 0 && len(executor.Command) > 0 {
		return nil, fmt.Errorf("executor %s does not define resumeCommand for session %s; configure a command that resumes an existing session", name, request.SessionID)
	}

	kind := executor.Kind
	if kind == "" {
		kind = name
	}
	binary := executor.Binary
	if binary == "" {
		switch kind {
		case "claude-code":
			binary = "claude"
		case "codex":
			binary = "codex"
		}
	}
	if binary == "" {
		return nil, fmt.Errorf("executor %s has no binary or command configured", name)
	}

	switch kind {
	case "claude-code":
		return buildClaudeCommand(executor, binary, request), nil
	case "codex":
		return buildCodexCommand(executor, binary, request), nil
	case "command":
		return nil, fmt.Errorf("executor %s requires command or resumeCommand", name)
	default:
		return nil, fmt.Errorf("executor %s has unsupported kind %q", name, kind)
	}
}

func buildClaudeCommand(executor Executor, binary string, request Request) []string {
	command := []string{binary}
	if request.SessionID != "" {
		command = append(command, "--resume", request.SessionID)
		if request.SessionMode == "fork" {
			command = append(command, "--fork-session")
		}
	}
	command = append(command, "--print")
	command = append(command, "--output-format", "json")
	if executor.Model != "" {
		command = append(command, "--model", executor.Model)
	}
	command = append(command, executor.ExtraArgs...)
	if executor.PromptMode == "stdin" {
		return command
	}
	// Native variadic options such as --allowedTools must not consume the
	// task as another option value. The task may itself begin with a dash.
	return append(command, "--", request.Prompt)
}

func buildCodexCommand(executor Executor, binary string, request Request) []string {
	command := []string{binary, "exec"}
	if request.SessionID != "" {
		if request.SessionMode == "fork" {
			command = append(command, "fork")
		} else {
			command = append(command, "resume")
		}
	}
	if executor.Model != "" {
		command = append(command, "-m", executor.Model)
	}
	// `codex exec resume` has a smaller option surface than `codex exec`; in
	// particular, sandbox is selected by the existing session and cannot be
	// changed while resuming it.
	if executor.Sandbox != "" && request.SessionID == "" {
		command = append(command, "-s", executor.Sandbox)
	}
	if executor.DangerouslyBypassApprovals {
		command = append(command, "--dangerously-bypass-approvals-and-sandbox")
	}
	if executor.SkipGitRepoCheck && (request.SessionID == "" || request.SessionMode == "fork") {
		command = append(command, "--skip-git-repo-check")
	}
	if executor.Ephemeral {
		command = append(command, "--ephemeral")
	}
	command = append(command, "--json")
	command = append(command, executor.ExtraArgs...)
	if request.SessionID != "" {
		command = append(command, request.SessionID)
	}
	return append(command, request.Prompt)
}

func RunCommand(command []string, options CommandOptions) CommandResult {
	if len(command) == 0 {
		return CommandResult{Code: -1, Error: "command must be non-empty"}
	}
	ctx := context.Background()
	var cancel context.CancelFunc
	if options.TimeoutMs > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(options.TimeoutMs)*time.Millisecond)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = options.Dir
	env, err := executionEnvironment(options.Environment, options.Env)
	if err != nil {
		return CommandResult{Code: -1, Error: err.Error()}
	}
	cmd.Env = env
	if options.Stdin != "" {
		cmd.Stdin = strings.NewReader(options.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return CommandResult{Code: -1, Stdout: stdout.String(), Stderr: stderr.String(), Error: err.Error()}
	}
	result := CommandResult{Code: 0, Started: true}
	err = cmd.Wait()
	result.Stdout, result.Stderr = stdout.String(), stderr.String()
	if ctx.Err() == context.DeadlineExceeded {
		result.Code = 124
		result.TimedOut = true
		result.Error = ctx.Err().Error()
		return result
	}
	if err == nil {
		return result
	}
	result.Code = 1
	if exitErr, ok := err.(*exec.ExitError); ok {
		result.Code = exitErr.ExitCode()
		if exitErr.ProcessState != nil && exitErr.ProcessState.Sys() != nil {
			result.Signal = exitErr.ProcessState.String()
		}
	}
	result.Error = err.Error()
	return result
}

func ExpandCommand(command []string, prompt, workspace, sessionID, resultFile, runID, nodeID string) []string {
	result := make([]string, len(command))
	replacer := strings.NewReplacer(
		"{{ prompt }}", prompt, "{{prompt}}", prompt,
		"{{ workspace }}", workspace, "{{workspace}}", workspace,
		"{{ session_id }}", sessionID, "{{session_id}}", sessionID,
		"{{ result_file }}", resultFile, "{{result_file}}", resultFile,
		"{{ run_id }}", runID, "{{run_id}}", runID,
		"{{ node_id }}", nodeID, "{{node_id}}", nodeID,
	)
	for index, part := range command {
		result[index] = replacer.Replace(part)
	}
	return result
}
