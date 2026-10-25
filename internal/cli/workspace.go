package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/chenhg5/agentflow/internal/workspace"
)

func workspaceCommand(args []string, cwd string) error {
	if len(args) == 0 || isHelp(args[0]) {
		printWorkspaceHelp()
		return nil
	}
	parsed, err := parseArgs(args[1:])
	if err != nil {
		return withFormat(err, parsed.Format)
	}
	if parsed.Help {
		printWorkspaceHelp()
		return nil
	}
	switch args[0] {
	case "init":
		if len(parsed.Positionals) > 1 {
			return usageError("at most one directory is allowed", "Use workspace init [directory].", "workspace_path")
		}
		root := cwd
		if len(parsed.Positionals) == 1 {
			root = parsed.Positionals[0]
			if !filepath.IsAbs(root) {
				root = filepath.Join(cwd, root)
			}
		}
		change, err := workspace.Init(root, parsed.DryRun)
		return writeWorkspaceChange(change, err, parsed)
	case "validate":
		if len(parsed.Positionals) != 0 {
			return usageError("use --workflow to select another graph", "Use workspace validate [--workflow path].", "workspace_path")
		}
		report := workspace.ValidateWithInputs(cwd, parsed.Workflow, parsed.Inputs)
		if err := writeData(report, parsed.Output()); err != nil {
			return err
		}
		if !report.Valid {
			return &CommandError{Code: ExitUsageError, Kind: "invalid_workspace", Message: "workspace validation failed", Format: parsed.Format, Silent: true}
		}
		return nil
	default:
		return usageError("unknown workspace action", "Use workspace init or workspace validate.", "workspace_action")
	}
}

func executorCommand(args []string, cwd string) error {
	if len(args) == 0 || isHelp(args[0]) {
		printExecutorHelp()
		return nil
	}
	parsed, err := parseArgs(args[1:])
	if err != nil {
		return withFormat(err, parsed.Format)
	}
	if parsed.Help {
		printExecutorHelp()
		return nil
	}
	if len(parsed.Positionals) != 1 || parsed.Kind == "" {
		return usageError("executor name and --kind are required", "Use executor init <name> --kind codex|claude-code|command, or executor add <name> --kind <kind> --workspace <directory>.", "executor_configuration")
	}
	var change workspace.Change
	switch args[0] {
	case "init":
		if parsed.ExecutorWorkspace != "" || parsed.CommandJSON != "" {
			return usageError("executor init creates an owned directory; use executor add for existing entries", "Use executor add with --workspace and optional --command.", "executor_configuration")
		}
		change, err = workspace.InitExecutor(cwd, parsed.Positionals[0], parsed.Kind, parsed.Model, parsed.DryRun)
	case "add":
		var command []string
		if parsed.CommandJSON != "" {
			if err := json.Unmarshal([]byte(parsed.CommandJSON), &command); err != nil {
				return usageError("--command must be a JSON string array", "Example: --command '[\"python3\",\"main.py\"]'.", "executor_command")
			}
		}
		change, err = workspace.AddExecutor(cwd, parsed.Positionals[0], parsed.Kind, parsed.ExecutorWorkspace, parsed.Model, command, parsed.DryRun)
	default:
		return usageError("unknown executor action", "Use executor init or executor add.", "executor_action")
	}
	return writeWorkspaceChange(change, err, parsed)
}

func writeWorkspaceChange(change workspace.Change, err error, parsed parsedArgs) error {
	if errors.Is(err, workspace.ErrConflict) {
		return conflictError(err.Error(), "Inspect the existing files; use executor add to reuse an existing directory.", parsed.Format)
	}
	if err != nil {
		return usageError(err.Error(), "Check the requested directory and executor options.", "workspace_configuration")
	}
	if err := writeData(map[string]any{"ok": true, "dry_run": parsed.DryRun, "workspace": change}, parsed.Output()); err != nil {
		return err
	}
	if parsed.DryRun {
		return &CommandError{Code: ExitDryRunPassed, Kind: "dry_run_passed", Message: "dry run passed", Format: parsed.Format, Silent: true}
	}
	return nil
}

func printWorkspaceHelp() {
	fmt.Println(`Usage:
  agentflow workspace init [directory] [--dry-run]
  agentflow workspace validate [--workflow workflow.json] [--input key=value]

Initialize a standalone layout-v1 workspace with a runnable program, or inspect
graph/config/workspace bindings and discover agent instructions, skills and tool
configuration. Supply representative inputs to resolve templated paths. Discovery
does not verify provider loading, role quality or actual execution.`)
}

func printExecutorHelp() {
	fmt.Println(`Usage:
  agentflow executor init <name> --kind codex|claude-code|command [--model MODEL] [--dry-run]
  agentflow executor add <name> --kind codex|claude-code|command|external
      [--workspace existing-directory] [--command JSON-array] [--model MODEL] [--dry-run]

init creates a new owned directory and registers it. add only registers an
existing directory (including a symlink); it does not modify its contents.
Choose new/fixed/inherit/fork on each graph execution candidate separately.`)
}
