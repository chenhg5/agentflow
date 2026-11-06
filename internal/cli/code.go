package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chenhg5/agentflow/internal/coderun"
	"github.com/chenhg5/agentflow/internal/model"
)

// runIDEnv carries the code run id from `code run` / `code start` to the
// script's own `code step` and `code end` calls.
const runIDEnv = "AGENTFLOW_RUN_ID"

// codeCommand records runs whose steps a script drives: code orchestration.
//
//	agentflow code run <name> [--input k=v] -- <command...>
//	agentflow code start <name> [--input k=v]
//	agentflow code step <step-id> [--run ID] [--summary S] [--output JSON] [--status S] [--reason R] [-- <command...>]
//	agentflow code end [--run ID] [--status S] [--reason R]
func codeCommand(args []string, root string) error {
	if len(args) == 0 || isHelp(args[0]) {
		printCodeHelp()
		return nil
	}
	verb := args[0]
	flags, command := splitCommand(args[1:])
	parsed, err := parseArgs(flags)
	if err != nil {
		return withFormat(err, parsed.Format)
	}
	if parsed.Help {
		printCodeHelp()
		return nil
	}
	switch verb {
	case "run":
		return codeRun(parsed, command, root)
	case "start":
		if len(parsed.Positionals) != 1 || len(command) > 0 {
			return usageError("usage: agentflow code start <name> [--input k=v]", "Use `agentflow code run <name> -- <command>` to wrap a whole script.", "code_arguments")
		}
		run, err := coderun.Start(root, parsed.Positionals[0], parsed.Inputs)
		if err != nil {
			return internalError(err)
		}
		return writeData(map[string]any{"ok": true, "run_id": run.RunID, "env": runIDEnv + "=" + run.RunID}, parsed.Output())
	case "step":
		return codeStep(parsed, command, root)
	case "end":
		runID, err := codeRunID(parsed)
		if err != nil {
			return err
		}
		if len(parsed.Statuses) > 1 || len(parsed.Positionals) > 0 || len(command) > 0 {
			return usageError("usage: agentflow code end [--run ID] [--status succeeded|failed|cancelled] [--reason R]", "", "code_arguments")
		}
		run, err := coderun.Update(root, runID, func(run *model.Run) error { return coderun.End(run, first(parsed.Statuses), parsed.Reason) })
		if err != nil {
			return codeError(err, runID)
		}
		return writeData(map[string]any{"ok": true, "run_id": run.RunID, "status": run.State.Status}, parsed.Output())
	default:
		return usageError(fmt.Sprintf("unknown code action: %s", verb), "Use `agentflow code --help`.", "code_action")
	}
}

// codeRun wraps a whole script as one run: the script's exit code decides how
// the run ends unless the script ended it itself.
func codeRun(parsed parsedArgs, command []string, root string) error {
	if len(parsed.Positionals) != 1 || len(command) == 0 {
		return usageError("usage: agentflow code run <name> [--input k=v] -- <command...>", "Example: agentflow code run nightly -- python3 nightly.py", "code_arguments")
	}
	run, err := coderun.Start(root, parsed.Positionals[0], parsed.Inputs)
	if err != nil {
		return internalError(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	process := exec.CommandContext(ctx, command[0], command[1:]...)
	process.Stdin, process.Stdout, process.Stderr = os.Stdin, os.Stdout, os.Stderr
	process.Env = append(os.Environ(), runIDEnv+"="+run.RunID, "AGENTFLOW_ROOT="+root)
	runErr := process.Run()
	status, message := "", ""
	if runErr != nil {
		status, message = "failed", runErr.Error()
		if ctx.Err() != nil {
			status, message = "cancelled", "interrupted"
		}
	}
	ended, err := coderun.Update(root, run.RunID, func(current *model.Run) error {
		err := coderun.End(current, status, message)
		if errors.Is(err, coderun.ErrFinished) {
			return nil // the script ended it with `code end`
		}
		return err
	})
	if err != nil {
		return internalError(err)
	}
	if ended.State.Status != "succeeded" {
		return &CommandError{Code: exitCodeOf(runErr), Kind: "workflow_failed", Message: fmt.Sprintf("code run %s %s", run.RunID, ended.State.Status), Silent: true}
	}
	return nil
}

// codeStep records one step: by running a command, or from the flags when the
// script did the work itself. The step's result JSON goes to stdout.
func codeStep(parsed parsedArgs, command []string, root string) error {
	if len(parsed.Positionals) != 1 {
		return usageError("usage: agentflow code step <step-id> [--run ID] [-- <command...>]", "Inside `agentflow code run`, the run id comes from $AGENTFLOW_RUN_ID.", "code_arguments")
	}
	stepID := parsed.Positionals[0]
	runID, err := codeRunID(parsed)
	if err != nil {
		return err
	}
	var output map[string]any
	if parsed.SubmittedOutput != "" {
		if err := json.Unmarshal([]byte(parsed.SubmittedOutput), &output); err != nil || output == nil {
			return usageError("--output must be a JSON object", "", "invalid_output")
		}
	}
	executor := coderun.Executor
	if len(command) > 0 {
		executor = filepath.Base(command[0])
	}
	run, err := coderun.Update(root, runID, func(run *model.Run) error { return coderun.Begin(run, stepID, executor) })
	if err != nil {
		return codeError(err, runID)
	}
	visit := run.State.Visits[stepID]
	status, message := first(parsed.Statuses), parsed.Reason
	artifacts := map[string]string{}
	exitErr := error(nil)
	if len(command) > 0 {
		var result map[string]any
		result, artifacts, exitErr = runStepCommand(run, stepID, visit, command)
		if output == nil {
			output = result
		}
		if status == "" {
			status = "succeeded"
			if exitErr != nil {
				status, message = "failed", firstNonEmpty(message, exitErr.Error())
			}
		}
	}
	if status == "" {
		status = "succeeded"
	}
	if parsed.Summary != "" {
		if output == nil {
			output = map[string]any{}
		}
		output["summary"] = parsed.Summary
	}
	if output != nil && artifacts["result"] == "" {
		if path, err := writeStepResult(run, stepID, visit, output); err == nil {
			artifacts["result"] = path
		}
	}
	if _, err := coderun.Update(root, runID, func(run *model.Run) error {
		return coderun.Finish(run, stepID, status, output, message, artifacts)
	}); err != nil {
		return codeError(err, runID)
	}
	if output == nil {
		output = map[string]any{}
	}
	data, _ := json.Marshal(output)
	fmt.Println(string(data))
	if status == "failed" {
		code := exitCodeOf(exitErr)
		if exitErr == nil {
			code = ExitGeneralError
		}
		return &CommandError{Code: code, Kind: "step_failed", Message: fmt.Sprintf("step %s failed", stepID), Silent: true}
	}
	return nil
}

// runStepCommand runs a step's command with the same file contract as an
// executor: it may write a JSON object to $AGENTFLOW_RESULT_FILE; its stdout
// and stderr are logged and also shown, on stderr, to whoever runs the script.
func runStepCommand(run *model.Run, stepID string, visit int, command []string) (map[string]any, map[string]string, error) {
	base := fmt.Sprintf("%s.%d", safeName(stepID), visit)
	relative := func(name string) string { return "artifacts/" + base + "." + name }
	stdoutFile, err := os.Create(filepath.Join(run.RunDir, filepath.FromSlash(relative("stdout.log"))))
	if err != nil {
		return nil, nil, err
	}
	defer stdoutFile.Close()
	stderrFile, err := os.Create(filepath.Join(run.RunDir, filepath.FromSlash(relative("stderr.log"))))
	if err != nil {
		return nil, nil, err
	}
	defer stderrFile.Close()
	resultPath := filepath.Join(run.RunDir, filepath.FromSlash(relative("result.json")))
	process := exec.Command(command[0], command[1:]...)
	process.Stdin = os.Stdin
	process.Stdout = io.MultiWriter(stdoutFile, os.Stderr)
	process.Stderr = io.MultiWriter(stderrFile, os.Stderr)
	process.Env = append(os.Environ(), runIDEnv+"="+run.RunID, "AGENTFLOW_NODE_ID="+stepID, "AGENTFLOW_RESULT_FILE="+resultPath)
	runErr := process.Run()
	artifacts := map[string]string{"stdoutLog": relative("stdout.log"), "stderrLog": relative("stderr.log")}
	var result map[string]any
	if data, err := os.ReadFile(resultPath); err == nil {
		artifacts["result"] = relative("result.json")
		if json.Unmarshal(data, &result) != nil {
			result = nil
			if runErr == nil {
				runErr = errors.New("the result file is not a JSON object")
			}
		}
	}
	return result, artifacts, runErr
}

func writeStepResult(run *model.Run, stepID string, visit int, output map[string]any) (string, error) {
	path := fmt.Sprintf("artifacts/%s.%d.result.json", safeName(stepID), visit)
	data, err := json.Marshal(output)
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(filepath.Join(run.RunDir, filepath.FromSlash(path)), data, 0o644)
}

func codeRunID(parsed parsedArgs) (string, error) {
	if parsed.RunID != "" {
		return parsed.RunID, nil
	}
	if id := os.Getenv(runIDEnv); id != "" {
		return id, nil
	}
	return "", usageError("no code run: pass --run or run inside `agentflow code run`", "Start one with `agentflow code start <name>` and export AGENTFLOW_RUN_ID.", "code_run_missing")
}

func codeError(err error, runID string) error {
	switch {
	case os.IsNotExist(errors.Unwrap(err)) || os.IsNotExist(err):
		return notFoundError("run", runID, "")
	case errors.Is(err, coderun.ErrNotCode):
		return usageError(fmt.Sprintf("run %s is a workflow run, not a code run", runID), "Use `agentflow run ...` for workflow runs.", "not_code_run")
	case errors.Is(err, coderun.ErrFinished):
		return conflictError(fmt.Sprintf("run %s already finished", runID), "Start a new run.", "")
	default:
		return usageError(err.Error(), "", "code_step")
	}
}

// splitCommand separates flags from the command after "--".
func splitCommand(args []string) (flags, command []string) {
	for index, arg := range args {
		if arg == "--" {
			return args[:index], args[index+1:]
		}
	}
	return args, nil
}

func exitCodeOf(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() > 0 {
		return exit.ExitCode()
	}
	if err != nil {
		return ExitGeneralError
	}
	return ExitSuccess
}

func safeName(id string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r < ' ' {
			return '_'
		}
		return r
	}, id)
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func printCodeHelp() {
	fmt.Println(`Usage:
  agentflow code run <name> [--input k=v] -- <command...>
  agentflow code step <step-id> [--summary S] [--output JSON] [--status S] [--reason R] [-- <command...>]
  agentflow code end [--status succeeded|failed|cancelled] [--reason R]
  agentflow code start <name> [--input k=v]

Code orchestration: a script decides the steps, agentflow records them as a run
(workflow version "code") that run list/get, metrics and the Hub all understand.

"code run" wraps a whole script: it starts the run, gives the script
AGENTFLOW_RUN_ID, and ends the run by the script's exit code (unless the script
called "code end"). Inside the script, "code step" records one step:

  with a command:  agentflow code step fetch -- ./fetch.sh
                   runs it like an executor (it may write a JSON object to
                   $AGENTFLOW_RESULT_FILE), logs stdout/stderr, ends failed on a
                   nonzero exit, and prints the result JSON on stdout
  without one:     agentflow code step decide --output '{"go":true}' --summary "will ship"
                   records work the script did itself

A step reached again is a new visit. The graph grows in the order the script
reaches steps. Outside "code run", use "code start" and pass --run or export
AGENTFLOW_RUN_ID.

Example (bash):
  agentflow code run nightly -- bash -c '
    count=$(agentflow code step fetch -- ./fetch.sh | jq .count)
    agentflow code step report --summary "fetched $count items" -- ./report.sh'`)
}
