package driver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Provider struct {
	// Kind selects a built-in provider implementation. Supported values are
	// claude-code, codex, and command. When Command is set it takes precedence
	// and preserves an explicitly configured command provider.
	Kind string `json:"kind,omitempty"`
	// Binary overrides the executable used by a built-in provider.
	Binary string `json:"binary,omitempty"`
	Model  string `json:"model,omitempty"`
	// Sandbox and provider-specific execution switches are intentionally kept
	// as configuration instead of being embedded in workflow JSON.
	Sandbox                    string   `json:"sandbox,omitempty"`
	DangerouslyBypassApprovals bool     `json:"dangerouslyBypassApprovalsAndSandbox,omitempty"`
	SkipGitRepoCheck           bool     `json:"skipGitRepoCheck,omitempty"`
	Ephemeral                  bool     `json:"ephemeral,omitempty"`
	ExtraArgs                  []string `json:"extraArgs,omitempty"`
	// Command starts a new agent session.
	Command []string `json:"command"`
	// ResumeCommand sends a prompt to an existing session. It is required when
	// a workflow node sets session.
	ResumeCommand []string `json:"resumeCommand,omitempty"`
}

// Request contains the runtime values needed to start or resume a provider
// session. The workflow engine owns these values; providers only translate
// them into their native CLI invocation.
type Request struct {
	Prompt     string
	Workspace  string
	SessionID  string
	ResultFile string
	RunID      string
	NodeID     string
}

// Status describes whether a provider's executable is available on the host.
// It is used by `agentflow doctor`; availability is checked separately from
// model authentication, which only the provider CLI can verify.
type Status struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Binary    string `json:"binary"`
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

type CommandOptions struct {
	Dir       string
	Env       map[string]string
	Stdin     string
	TimeoutMs int
}

type CommandResult struct {
	Code     int
	Signal   string
	Stdout   string
	Stderr   string
	TimedOut bool
}

func LoadProviders(cwd string) (map[string]Provider, error) {
	providers := BuiltInProviders()
	files := []string{filepath.Join(cwd, ".agentflow", "providers.json")}
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".agentflow", "providers.json"))
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("cannot read providers file %s: %w", file, err)
		}
		var config struct {
			Providers map[string]Provider `json:"providers"`
			Drivers   map[string]Provider `json:"drivers"`
		}
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("invalid providers file %s: %w", file, err)
		}
		configured := config.Providers
		if len(configured) == 0 {
			configured = config.Drivers
		}
		for name, configuredProvider := range configured {
			if base, ok := providers[name]; ok {
				providers[name] = merge(base, configuredProvider)
			} else {
				if configuredProvider.Kind == "" {
					configuredProvider.Kind = "command"
				}
				providers[name] = configuredProvider
			}
		}
		return providers, nil
	}
	return providers, nil
}

// BuiltInProviders returns providers that can run without a project-local
// providers.json. Configuration can override their binary and execution
// options while workflow definitions keep stable provider names.
func BuiltInProviders() map[string]Provider {
	return map[string]Provider{
		"claude-code": {Kind: "claude-code", Binary: "claude"},
		"codex":       {Kind: "codex", Binary: "codex"},
	}
}

// CheckProvider verifies that the executable needed by a provider can be
// resolved. It deliberately does not make a network request or consume model
// quota.
func CheckProvider(name string, provider Provider) Status {
	kind := provider.Kind
	if kind == "" {
		kind = name
	}
	binary := provider.Binary
	if len(provider.Command) > 0 {
		binary = provider.Command[0]
	}
	if binary == "" {
		switch kind {
		case "claude", "claude-code":
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
	if _, err := exec.LookPath(binary); err != nil {
		status.Error = err.Error()
		return status
	}
	status.Available = true
	return status
}

func merge(base, override Provider) Provider {
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
	return base
}

// BuildCommand translates a provider request into an executable command.
// Explicit command templates take precedence, which keeps the generic
// provider useful for custom agent CLIs and test doubles.
func BuildCommand(name string, provider Provider, request Request) ([]string, error) {
	if request.SessionID != "" && len(provider.ResumeCommand) > 0 {
		return ExpandCommand(provider.ResumeCommand, request.Prompt, request.Workspace, request.SessionID, request.ResultFile, request.RunID, request.NodeID), nil
	}
	if request.SessionID == "" && len(provider.Command) > 0 {
		return ExpandCommand(provider.Command, request.Prompt, request.Workspace, request.SessionID, request.ResultFile, request.RunID, request.NodeID), nil
	}
	if request.SessionID != "" && len(provider.ResumeCommand) == 0 && len(provider.Command) > 0 {
		return nil, fmt.Errorf("provider %s does not define resumeCommand for session %s; configure a command that resumes an existing session", name, request.SessionID)
	}

	kind := provider.Kind
	if kind == "" {
		kind = name
	}
	binary := provider.Binary
	if binary == "" {
		switch kind {
		case "claude", "claude-code":
			binary = "claude"
		case "codex":
			binary = "codex"
		}
	}
	if binary == "" {
		return nil, fmt.Errorf("provider %s has no binary or command configured", name)
	}

	switch kind {
	case "claude", "claude-code":
		return buildClaudeCommand(provider, binary, request), nil
	case "codex":
		return buildCodexCommand(provider, binary, request), nil
	case "command":
		return nil, fmt.Errorf("provider %s requires command or resumeCommand", name)
	default:
		return nil, fmt.Errorf("provider %s has unsupported kind %q", name, kind)
	}
}

func buildClaudeCommand(provider Provider, binary string, request Request) []string {
	command := []string{binary}
	if request.SessionID != "" {
		command = append(command, "--resume", request.SessionID)
	}
	command = append(command, "--print")
	command = append(command, provider.ExtraArgs...)
	return append(command, request.Prompt)
}

func buildCodexCommand(provider Provider, binary string, request Request) []string {
	command := []string{binary, "exec"}
	if request.SessionID != "" {
		command = append(command, "resume")
	}
	if provider.Model != "" {
		command = append(command, "-m", provider.Model)
	}
	// `codex exec resume` has a smaller option surface than `codex exec`; in
	// particular, sandbox is selected by the existing session and cannot be
	// changed while resuming it.
	if provider.Sandbox != "" && request.SessionID == "" {
		command = append(command, "-s", provider.Sandbox)
	}
	if provider.DangerouslyBypassApprovals {
		command = append(command, "--dangerously-bypass-approvals-and-sandbox")
	}
	if provider.SkipGitRepoCheck && request.SessionID == "" {
		command = append(command, "--skip-git-repo-check")
	}
	if provider.Ephemeral {
		command = append(command, "--ephemeral")
	}
	command = append(command, provider.ExtraArgs...)
	if request.SessionID != "" {
		command = append(command, request.SessionID)
	}
	return append(command, request.Prompt)
}

func RunCommand(command []string, options CommandOptions) CommandResult {
	if len(command) == 0 {
		return CommandResult{Code: 1, Stderr: "command must be non-empty"}
	}
	ctx := context.Background()
	var cancel context.CancelFunc
	if options.TimeoutMs > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(options.TimeoutMs)*time.Millisecond)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = options.Dir
	cmd.Env = os.Environ()
	for key, value := range options.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	if options.Stdin != "" {
		cmd.Stdin = strings.NewReader(options.Stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := CommandResult{Code: 0, Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() == context.DeadlineExceeded {
		result.Code = 124
		result.TimedOut = true
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
