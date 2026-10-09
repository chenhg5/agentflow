package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/engine"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/schema"
	"github.com/chenhg5/agentflow/internal/store"
)

type ExitError struct {
	Code int
	Err  error
}

func (e ExitError) Error() string { return e.Err.Error() }
func (e ExitError) Unwrap() error { return e.Err }
func (e ExitError) ExitCode() int { return e.Code }

func Run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		printHelp()
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root := filepath.Join(cwd, ".agentflow")
	switch args[0] {
	case "init":
		return initCommand(cwd)
	case "validate":
		return validateCommand(args[1:])
	case "run":
		return runCommand(args[1:], root, cwd)
	case "list":
		return listCommand(root)
	case "show":
		return showCommand(args[1:], root)
	case "logs":
		return logsCommand(args[1:], root)
	case "approve", "reject":
		return decisionCommand(args[0], args[1:], root, cwd)
	case "resume":
		return resumeCommand(args[1:], root, cwd)
	case "cancel":
		return cancelCommand(args[1:], root)
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func validateCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: agentflow validate <workflow.json>")
	}
	workflow, err := readWorkflow(args[0])
	if err != nil {
		return err
	}
	if err := assertValid(workflow); err != nil {
		return err
	}
	fmt.Printf("valid workflow: %s\n", workflow.Name)
	return nil
}

func initCommand(cwd string) error {
	directory := filepath.Join(cwd, ".agentflow")
	if err := store.EnsureDir(directory); err != nil {
		return err
	}
	target := filepath.Join(directory, "providers.json")
	if _, err := os.Stat(target); err == nil {
		fmt.Printf("already exists: %s\n", target)
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	const example = `{
  "drivers": {
    "claude-code": { "command": ["claude", "--print", "{{ prompt }}"] },
    "codex": { "command": ["codex", "exec", "{{ prompt }}"] }
  }
}
`
	if err := os.WriteFile(target, []byte(example), 0o644); err != nil {
		return err
	}
	fmt.Printf("created %s\n", target)
	return nil
}

func runCommand(args []string, root, cwd string) error {
	if len(args) == 0 {
		return errors.New("usage: agentflow run <workflow.json> --input key=value")
	}
	workflow, err := readWorkflow(args[0])
	if err != nil {
		return err
	}
	if err := assertValid(workflow); err != nil {
		return err
	}
	inputs, err := parseInputs(args[1:])
	if err != nil {
		return err
	}
	for name, definition := range workflow.Inputs {
		if definition.Required && inputs[name] == "" {
			return fmt.Errorf("missing required input: %s", name)
		}
	}
	if err := store.EnsureDir(root); err != nil {
		return err
	}
	run, err := store.CreateRun(root, workflow, inputs)
	if err != nil {
		return err
	}
	fmt.Printf("started %s\n", run.RunID)
	providers, err := driver.LoadProviders(cwd)
	if err != nil {
		return err
	}
	if err := engine.Execute(run, providers); err != nil {
		return err
	}
	printState(run.State)
	if run.State.Status == "failed" {
		return ExitError{Code: 1, Err: errors.New("workflow failed")}
	}
	return nil
}

func resumeCommand(args []string, root, cwd string) error {
	if len(args) == 0 {
		return errors.New("usage: agentflow resume <run-id>")
	}
	run, err := store.LoadRun(root, args[0])
	if err != nil {
		return err
	}
	providers, err := driver.LoadProviders(cwd)
	if err != nil {
		return err
	}
	if err := engine.Execute(run, providers); err != nil {
		return err
	}
	printState(run.State)
	if run.State.Status == "failed" {
		return ExitError{Code: 1, Err: errors.New("workflow failed")}
	}
	return nil
}

func decisionCommand(command string, args []string, root, cwd string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: agentflow %s <run-id> <node-id> [message]", command)
	}
	run, err := store.LoadRun(root, args[0])
	if err != nil {
		return err
	}
	node, ok := run.State.Nodes[args[1]]
	if !ok || node.Status != "waiting_human" {
		return fmt.Errorf("node is not waiting for approval: %s", args[1])
	}
	node.Result = map[string]any{"decision": command, "message": strings.Join(args[2:], " ")}
	node.FinishedAt = now()
	node.Status = "succeeded"
	if command == "reject" {
		node.Status = "failed"
	}
	if err := store.AppendEvent(run.RunDir, map[string]any{"type": "node." + command + "d", "nodeId": args[1], "message": node.Result.(map[string]any)["message"]}); err != nil {
		return err
	}
	if command == "reject" {
		run.State.Status = "failed"
		if err := store.SaveState(run); err != nil {
			return err
		}
		printState(run.State)
		return ExitError{Code: 1, Err: errors.New("workflow rejected")}
	}
	if err := store.SaveState(run); err != nil {
		return err
	}
	providers, err := driver.LoadProviders(cwd)
	if err != nil {
		return err
	}
	if err := engine.Execute(run, providers); err != nil {
		return err
	}
	printState(run.State)
	if run.State.Status == "failed" {
		return ExitError{Code: 1, Err: errors.New("workflow failed")}
	}
	return nil
}

func cancelCommand(args []string, root string) error {
	if len(args) == 0 {
		return errors.New("usage: agentflow cancel <run-id>")
	}
	run, err := store.LoadRun(root, args[0])
	if err != nil {
		return err
	}
	run.State.Status = "cancelled"
	if err := store.SaveState(run); err != nil {
		return err
	}
	_ = store.AppendEvent(run.RunDir, map[string]any{"type": "run.cancelled", "runId": run.RunID})
	printState(run.State)
	return nil
}
func showCommand(args []string, root string) error {
	if len(args) == 0 {
		return errors.New("usage: agentflow show <run-id>")
	}
	run, err := store.LoadRun(root, args[0])
	if err != nil {
		return err
	}
	printState(run.State)
	return nil
}
func logsCommand(args []string, root string) error {
	if len(args) == 0 {
		return errors.New("usage: agentflow logs <run-id>")
	}
	run, err := store.LoadRun(root, args[0])
	if err != nil {
		return err
	}
	data, err := store.ReadEvents(run.RunDir)
	if err != nil {
		return err
	}
	fmt.Print(string(data))
	return nil
}

func listCommand(root string) error {
	entries, err := os.ReadDir(filepath.Join(root, "runs"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if !entry.IsDir() {
			continue
		}
		run, err := store.LoadRun(root, entry.Name())
		if err != nil {
			return err
		}
		fmt.Printf("%s\t%s\t%s\n", run.RunID, run.State.Status, run.State.WorkflowName)
	}
	return nil
}

func readWorkflow(file string) (model.Workflow, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return model.Workflow{}, err
	}
	var workflow model.Workflow
	if err := json.Unmarshal(data, &workflow); err != nil {
		return workflow, fmt.Errorf("invalid workflow JSON: %w", err)
	}
	return workflow, nil
}
func assertValid(workflow model.Workflow) error {
	if errors := schema.Validate(workflow); len(errors) > 0 {
		return fmt.Errorf("workflow is invalid:\n- %s", strings.Join(errors, "\n- "))
	}
	return nil
}
func parseInputs(args []string) (map[string]string, error) {
	result := map[string]string{}
	for index := 0; index < len(args); index++ {
		if args[index] != "--input" {
			continue
		}
		index++
		if index >= len(args) {
			return nil, errors.New("inputs must use --input key=value")
		}
		pair := args[index]
		split := strings.Index(pair, "=")
		if split < 1 {
			return nil, errors.New("inputs must use --input key=value")
		}
		result[pair[:split]] = pair[split+1:]
	}
	return result, nil
}
func printState(state model.RunState) {
	fmt.Printf("status: %s\n", state.Status)
	ids := make([]string, 0, len(state.Nodes))
	for id := range state.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Printf("  %s: %s\n", id, state.Nodes[id].Status)
	}
}
func printHelp() {
	fmt.Println("agentflow - local workflow runner\n\nCommands:\n  init\n  validate <workflow.json>\n  run <workflow.json> --input key=value\n  list\n  show <run-id>\n  logs <run-id>\n  approve <run-id> <node-id> [message]\n  reject <run-id> <node-id> [message]\n  resume <run-id>\n  cancel <run-id>")
}
func now() string { return time.Now().UTC().Format(time.RFC3339) }
