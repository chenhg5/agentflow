package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/engine"
	"github.com/chenhg5/agentflow/internal/metrics"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/schema"
	"github.com/chenhg5/agentflow/internal/store"
	"github.com/chenhg5/agentflow/internal/template"
	"github.com/chenhg5/agentflow/internal/version"
	"github.com/chenhg5/agentflow/internal/web"
)

const (
	ExitSuccess      = 0
	ExitGeneralError = 1
	ExitUsageError   = 2
	ExitNotFound     = 3
	ExitPermission   = 4
	ExitConflict     = 5
	ExitDryRunPassed = 10
)

type CommandError struct {
	Code       int
	Kind       string
	Message    string
	Suggestion string
	Retryable  bool
	Input      any
	Format     string
	Silent     bool
}

func (e *CommandError) Error() string { return e.Message }
func (e *CommandError) ExitCode() int { return e.Code }

func Run(args []string) error {
	if len(args) == 0 {
		printRootHelp()
		return nil
	}
	if isHelp(args[0]) {
		printHelp(args[1:])
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return internalError(err)
	}
	root := filepath.Join(cwd, ".agentflow")
	switch args[0] {
	case "workflow":
		return workflowCommand(args[1:])
	case "run":
		return runCommand(args[1:], root, cwd)
	case "schema":
		return schemaCommand(args[1:], cwd)
	case "config":
		return configCommand(args[1:], cwd)
	case "doctor":
		return doctorCommand(args[1:], cwd)
	case "web":
		return webCommand(args[1:], root, cwd)
	case "version":
		return versionCommand(args[1:])
	default:
		return usageError(fmt.Sprintf("unknown command: %s", args[0]), "Run `agentflow --help` to see the command tree.", "command")
	}
}

func versionCommand(args []string) error {
	parsed, err := parseArgs(args)
	if err != nil {
		return withFormat(err, parsed.Format)
	}
	if parsed.Help {
		printVersionHelp()
		return nil
	}
	if len(parsed.Positionals) != 0 {
		return usageError("version accepts no positional arguments", "Run `agentflow version --format json`.", "version_arguments")
	}
	info := version.Current()
	if parsed.Output().resolved() == "table" {
		fmt.Printf("agentflow %s (commit %s, built %s)\n", info.Version, info.Commit, info.Built)
		return nil
	}
	return writeData(info, parsed.Output())
}

func webCommand(args []string, root, cwd string) error {
	if len(args) > 0 && isHelp(args[0]) {
		printWebHelp()
		return nil
	}
	parsed, err := parseArgs(args)
	if err != nil {
		return withFormat(err, parsed.Format)
	}
	if parsed.Help {
		printWebHelp()
		return nil
	}
	if len(parsed.Positionals) > 1 {
		return usageError("web accepts at most one workflow file", "Run `agentflow web [workflow.json] [--run <run-id>]`.", "workflow_file")
	}
	if len(parsed.Positionals) == 1 && parsed.RunID != "" {
		return usageError("web accepts either a workflow file or a run id", "Use `agentflow web workflow.json` or `agentflow web --run <run-id>`.", "web_target")
	}
	workflowPath := ""
	if len(parsed.Positionals) == 1 {
		workflowPath, err = filepath.Abs(parsed.Positionals[0])
		if err != nil {
			return internalError(err)
		}
		workflow, workflowErr := readWorkflow(workflowPath, parsed.Format)
		if workflowErr != nil {
			return workflowErr
		}
		if validationErr := assertValid(workflow, parsed.Format); validationErr != nil {
			return validationErr
		}
	}
	if parsed.RunID != "" {
		if _, err := store.LoadRun(root, parsed.RunID); err != nil {
			return notFoundError("run", parsed.RunID, parsed.Format)
		}
	}
	host := parsed.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := parsed.Port
	if !parsed.PortSet {
		port = -1
	}
	return web.NewServer(web.Options{Root: root, WorkflowPath: workflowPath, RunID: parsed.RunID, Host: host, Port: port}).Serve()
}

func workflowCommand(args []string) error {
	if len(args) == 0 || isHelp(args[0]) {
		printWorkflowHelp()
		return nil
	}
	parsed, err := parseArgs(args[1:])
	if err != nil {
		return withFormat(err, parsed.Format)
	}
	if parsed.Help {
		printWorkflowHelp()
		return nil
	}
	if args[0] != "validate" {
		return usageError(fmt.Sprintf("unknown workflow action: %s", args[0]), "Use `agentflow workflow validate <workflow.json>`.", "workflow_action")
	}
	if len(parsed.Positionals) != 1 {
		return usageError("workflow file is required", "Run `agentflow workflow validate <workflow.json>`.", "workflow_file")
	}
	workflow, err := readWorkflow(parsed.Positionals[0], parsed.Format)
	if err != nil {
		return err
	}
	if err := assertValid(workflow, parsed.Format); err != nil {
		return err
	}
	return writeData(map[string]any{"ok": true, "valid": true, "workflow": workflow.Name, "version": workflow.Version}, parsed.Output())
}

func runCommand(args []string, root, cwd string) error {
	if len(args) == 0 || isHelp(args[0]) {
		printRunHelp()
		return nil
	}
	verb := args[0]
	if !isRunVerb(verb) {
		return usageError(fmt.Sprintf("unknown run action: %s", verb), "Use `agentflow run --help` to list actions.", "run_action")
	}
	parsed, err := parseArgs(args[1:])
	if err != nil {
		return withFormat(err, parsed.Format)
	}
	if parsed.Help {
		printRunHelp()
		return nil
	}
	switch verb {
	case "start":
		return runStart(parsed, root, cwd)
	case "list":
		return runList(parsed, root)
	case "get":
		return runGet(parsed, root)
	case "logs":
		return runLogs(parsed, root)
	case "metrics":
		return runMetrics(parsed, root)
	case "stats":
		return runStats(parsed, root)
	case "approve", "reject":
		return runDecision(verb, parsed, root, cwd)
	case "resume":
		return runResume(parsed, root, cwd)
	case "cancel":
		return runCancel(parsed, root)
	default:
		return usageError("unsupported run action", "Use `agentflow run --help` to list actions.", "run_action")
	}
}

func runStart(parsed parsedArgs, root, cwd string) error {
	if len(parsed.Positionals) != 1 {
		return usageError("workflow file is required", "Run `agentflow run start <workflow.json> --input key=value`.", "workflow_file")
	}
	workflow, err := readWorkflow(parsed.Positionals[0], parsed.Format)
	if err != nil {
		return err
	}
	if err := assertValid(workflow, parsed.Format); err != nil {
		return err
	}
	for name, definition := range workflow.Inputs {
		if definition.Required {
			if value, ok := parsed.Inputs[name]; !ok || value == "" {
				return usageError(fmt.Sprintf("missing required input: %s", name), fmt.Sprintf("Add `--input %s=<value>`.", name), "missing_input")
			}
		}
	}
	if parsed.DryRun {
		runContext := map[string]any{"inputs": parsed.Inputs}
		nodes := make([]map[string]any, 0, len(workflow.Nodes))
		for _, node := range workflow.Nodes {
			needs := node.Needs
			if needs == nil {
				needs = []string{}
			}
			plan := map[string]any{"id": node.ID, "type": node.Type, "needs": needs}
			if node.Workspace != "" {
				workspace := template.Render(node.Workspace, runContext)
				if workspace != "" {
					if absolute, err := filepath.Abs(workspace); err == nil {
						workspace = absolute
					}
					plan["workspace"] = workspace
				}
			}
			if node.Session != "" {
				plan["session"] = template.Render(node.Session, runContext)
			}
			nodes = append(nodes, plan)
		}
		if err := writeData(map[string]any{"ok": true, "dry_run": true, "action": "run.start", "workflow": workflow.Name, "inputs": parsed.Inputs, "nodes": nodes}, parsed.Output()); err != nil {
			return err
		}
		return &CommandError{Code: ExitDryRunPassed, Kind: "dry_run_passed", Message: "dry run passed", Format: parsed.Format, Silent: true}
	}
	if err := store.EnsureDir(root); err != nil {
		return internalError(err)
	}
	if parsed.IdempotencyKey != "" {
		existing, err := store.FindRunByIdempotency(root, workflow.Name, parsed.IdempotencyKey)
		if err != nil {
			return internalError(err)
		}
		if existing != nil {
			if !sameInputs(existing.State.Inputs, parsed.Inputs) || !reflect.DeepEqual(existing.Workflow, workflow) {
				return conflictError(fmt.Sprintf("idempotency key already belongs to run %s with a different request", existing.RunID), "Reuse the original workflow and inputs or choose a new `--idempotency-key`.", parsed.Format)
			}
			payload := runPayload(existing)
			payload["idempotent_reused"] = true
			return writeData(payload, parsed.Output())
		}
	}
	providers, err := driver.LoadProviders(cwd)
	if err != nil {
		return internalError(err)
	}
	run, err := store.CreateRun(root, workflow, parsed.Inputs, parsed.IdempotencyKey)
	if err != nil {
		return internalError(err)
	}
	if err := engine.Execute(run, providers); err != nil {
		return internalError(err)
	}
	if err := writeData(runPayload(run), parsed.Output()); err != nil {
		return err
	}
	if run.State.Status == "failed" {
		return &CommandError{Code: ExitGeneralError, Kind: "workflow_failed", Message: "workflow failed", Suggestion: fmt.Sprintf("Inspect with `agentflow run get %s` and `agentflow run logs %s`.", run.RunID, run.RunID), Retryable: false, Format: parsed.Format, Silent: true}
	}
	return nil
}

func runList(parsed parsedArgs, root string) error {
	entries, err := os.ReadDir(filepath.Join(root, "runs"))
	if os.IsNotExist(err) {
		return writeData(map[string]any{"ok": true, "runs": []any{}}, parsed.Output())
	}
	if err != nil {
		return internalError(err)
	}
	summaries := make([]map[string]any, 0, len(entries))
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if !entry.IsDir() {
			continue
		}
		run, err := store.LoadRun(root, entry.Name())
		if err != nil {
			return internalError(err)
		}
		summaries = append(summaries, map[string]any{"run_id": run.RunID, "workflow": run.State.WorkflowName, "status": run.State.Status, "created_at": run.State.CreatedAt, "updated_at": run.State.UpdatedAt})
		if parsed.PageSize > 0 && len(summaries) >= parsed.PageSize && !parsed.PageAll {
			break
		}
	}
	return writeData(map[string]any{"ok": true, "runs": summaries}, parsed.Output())
}

func runGet(parsed parsedArgs, root string) error {
	if len(parsed.Positionals) != 1 {
		return usageError("run id is required", "Run `agentflow run get <run-id>`.", "run_id")
	}
	run, err := store.LoadRun(root, parsed.Positionals[0])
	if err != nil {
		return notFoundError("run", parsed.Positionals[0], parsed.Format)
	}
	return writeData(runPayload(run), parsed.Output())
}

func runLogs(parsed parsedArgs, root string) error {
	if len(parsed.Positionals) != 1 {
		return usageError("run id is required", "Run `agentflow run logs <run-id>`.", "run_id")
	}
	run, err := store.LoadRun(root, parsed.Positionals[0])
	if err != nil {
		return notFoundError("run", parsed.Positionals[0], parsed.Format)
	}
	data, err := store.ReadEvents(run.RunDir)
	if err != nil {
		return internalError(err)
	}
	if parsed.Output().Format == "table" {
		_, err = os.Stdout.Write(data)
		return err
	}
	events := make([]map[string]any, 0)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return internalError(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if parsed.Output().Format == "ndjson" {
		return writeNDJSON(events)
	}
	return writeData(map[string]any{"ok": true, "run_id": run.RunID, "events": events}, parsed.Output())
}

func runMetrics(parsed parsedArgs, root string) error {
	if len(parsed.Positionals) != 1 {
		return usageError("run id is required", "Run `agentflow run metrics <run-id>`.", "run_id")
	}
	run, err := store.LoadRun(root, parsed.Positionals[0])
	if err != nil {
		return notFoundError("run", parsed.Positionals[0], parsed.Format)
	}
	return writeData(metrics.Snapshot(run, time.Now().UTC()), parsed.Output())
}

func runStats(parsed parsedArgs, root string) error {
	if len(parsed.Positionals) > 1 {
		return usageError("run stats accepts at most one workflow name", "Run `agentflow run stats [workflow-name]`.", "workflow_name")
	}
	workflowName := ""
	if len(parsed.Positionals) == 1 {
		workflowName = parsed.Positionals[0]
	}
	runs, err := loadRuns(root)
	if err != nil {
		return err
	}
	return writeData(metrics.Aggregate(runs, workflowName, time.Now().UTC()), parsed.Output())
}

func loadRuns(root string) ([]*model.Run, error) {
	entries, err := os.ReadDir(filepath.Join(root, "runs"))
	if os.IsNotExist(err) {
		return []*model.Run{}, nil
	}
	if err != nil {
		return nil, internalError(err)
	}
	runs := make([]*model.Run, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		run, err := store.LoadRun(root, entry.Name())
		if err != nil {
			return nil, internalError(err)
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func runDecision(verb string, parsed parsedArgs, root, cwd string) error {
	if len(parsed.Positionals) < 2 {
		return usageError(fmt.Sprintf("usage: agentflow run %s <run-id> <node-id> [message]", verb), "The node must be waiting_human.", "approval_arguments")
	}
	run, err := store.LoadRun(root, parsed.Positionals[0])
	if err != nil {
		return notFoundError("run", parsed.Positionals[0], parsed.Format)
	}
	node, ok := run.State.Nodes[parsed.Positionals[1]]
	if !ok {
		return notFoundError("node", parsed.Positionals[1], parsed.Format)
	}
	if node.Status != "waiting_human" {
		return conflictError(fmt.Sprintf("node %s is not waiting for human input", parsed.Positionals[1]), "Inspect the run before approving it.", parsed.Format)
	}
	message := parsed.Message
	if message == "" && len(parsed.Positionals) > 2 {
		message = strings.Join(parsed.Positionals[2:], " ")
	}
	if parsed.DryRun {
		if err := writeData(map[string]any{"ok": true, "dry_run": true, "action": "run." + verb, "run_id": run.RunID, "node_id": parsed.Positionals[1], "decision": verb, "message": message}, parsed.Output()); err != nil {
			return err
		}
		return &CommandError{Code: ExitDryRunPassed, Kind: "dry_run_passed", Message: "dry run passed", Format: parsed.Format, Silent: true}
	}
	node.Result = map[string]any{"decision": verb, "message": message}
	node.Attempts = 1
	resultBytes, marshalErr := json.Marshal(node.Result)
	if marshalErr != nil {
		return internalError(marshalErr)
	}
	node.OutputBytes = int64(len(resultBytes))
	node.FinishedAt = now()
	metrics.RefreshNode(node, time.Now().UTC())
	if verb == "approve" {
		node.Status = "succeeded"
	} else {
		node.Status = "failed"
	}
	if err := store.AppendEvent(run.RunDir, map[string]any{"type": "node." + verb + "d", "nodeId": parsed.Positionals[1], "message": message}); err != nil {
		return internalError(err)
	}
	if verb == "reject" {
		run.State.Status = "failed"
		metrics.MarkRunFinished(&run.State, time.Now().UTC())
		if err := store.SaveState(run); err != nil {
			return internalError(err)
		}
		if err := writeData(runPayload(run), parsed.Output()); err != nil {
			return err
		}
		return &CommandError{Code: ExitGeneralError, Kind: "workflow_rejected", Message: "workflow rejected", Retryable: false, Format: parsed.Format, Silent: true}
	}
	if err := store.SaveState(run); err != nil {
		return internalError(err)
	}
	providers, err := driver.LoadProviders(cwd)
	if err != nil {
		return internalError(err)
	}
	if err := engine.Execute(run, providers); err != nil {
		return internalError(err)
	}
	if err := writeData(runPayload(run), parsed.Output()); err != nil {
		return err
	}
	if run.State.Status == "failed" {
		return &CommandError{Code: ExitGeneralError, Kind: "workflow_failed", Message: "workflow failed", Format: parsed.Format, Silent: true}
	}
	return nil
}

func runResume(parsed parsedArgs, root, cwd string) error {
	if len(parsed.Positionals) != 1 {
		return usageError("run id is required", "Run `agentflow run resume <run-id>`.", "run_id")
	}
	run, err := store.LoadRun(root, parsed.Positionals[0])
	if err != nil {
		return notFoundError("run", parsed.Positionals[0], parsed.Format)
	}
	if parsed.DryRun {
		pending := make([]string, 0)
		for id, node := range run.State.Nodes {
			if node.Status == "pending" {
				pending = append(pending, id)
			}
		}
		sort.Strings(pending)
		if err := writeData(map[string]any{"ok": true, "dry_run": true, "action": "run.resume", "run_id": run.RunID, "current_status": run.State.Status, "pending_nodes": pending}, parsed.Output()); err != nil {
			return err
		}
		return &CommandError{Code: ExitDryRunPassed, Kind: "dry_run_passed", Message: "dry run passed", Format: parsed.Format, Silent: true}
	}
	providers, err := driver.LoadProviders(cwd)
	if err != nil {
		return internalError(err)
	}
	if err := engine.Execute(run, providers); err != nil {
		return internalError(err)
	}
	if err := writeData(runPayload(run), parsed.Output()); err != nil {
		return err
	}
	if run.State.Status == "failed" {
		return &CommandError{Code: ExitGeneralError, Kind: "workflow_failed", Message: "workflow failed", Format: parsed.Format, Silent: true}
	}
	return nil
}

func runCancel(parsed parsedArgs, root string) error {
	if len(parsed.Positionals) != 1 {
		return usageError("run id is required", "Run `agentflow run cancel <run-id>`.", "run_id")
	}
	run, err := store.LoadRun(root, parsed.Positionals[0])
	if err != nil {
		return notFoundError("run", parsed.Positionals[0], parsed.Format)
	}
	if parsed.DryRun {
		if err := writeData(map[string]any{"ok": true, "dry_run": true, "action": "run.cancel", "run_id": run.RunID, "current_status": run.State.Status}, parsed.Output()); err != nil {
			return err
		}
		return &CommandError{Code: ExitDryRunPassed, Kind: "dry_run_passed", Message: "dry run passed", Format: parsed.Format, Silent: true}
	}
	run.State.Status = "cancelled"
	metrics.MarkRunFinished(&run.State, time.Now().UTC())
	if err := store.SaveState(run); err != nil {
		return internalError(err)
	}
	if err := store.AppendEvent(run.RunDir, map[string]any{"type": "run.cancelled", "runId": run.RunID}); err != nil {
		return internalError(err)
	}
	return writeData(runPayload(run), parsed.Output())
}

func configCommand(args []string, cwd string) error {
	if len(args) == 0 || isHelp(args[0]) {
		printConfigHelp()
		return nil
	}
	parsed, err := parseArgs(args[1:])
	if err != nil {
		return withFormat(err, parsed.Format)
	}
	if parsed.Help {
		printConfigHelp()
		return nil
	}
	if args[0] != "init" {
		return usageError("unknown config action", "Use `agentflow config init`.", "config_action")
	}
	directory := filepath.Join(cwd, ".agentflow")
	target := filepath.Join(directory, "providers.json")
	if parsed.DryRun {
		if err := writeData(map[string]any{"ok": true, "dry_run": true, "action": "config.init", "path": target}, parsed.Output()); err != nil {
			return err
		}
		return &CommandError{Code: ExitDryRunPassed, Kind: "dry_run_passed", Message: "dry run passed", Format: parsed.Format, Silent: true}
	}
	if err := store.EnsureDir(directory); err != nil {
		return internalError(err)
	}
	if _, err := os.Stat(target); err == nil {
		return conflictError(fmt.Sprintf("provider config already exists: %s", target), "Edit the existing file or remove it intentionally.", parsed.Format)
	} else if !os.IsNotExist(err) {
		return internalError(err)
	}
	const example = `{
  "providers": {
    "claude-code": {
      "kind": "claude-code",
      "binary": "claude"
    },
    "codex": {
      "kind": "codex",
      "binary": "codex"
    }
  }
}
`
	if err := os.WriteFile(target, []byte(example), 0o644); err != nil {
		return internalError(err)
	}
	return writeData(map[string]any{"ok": true, "path": target, "created": true}, parsed.Output())
}

func schemaCommand(args []string, cwd string) error {
	if len(args) == 0 || isHelp(args[0]) {
		printSchemaHelp()
		return nil
	}
	parsed, err := parseArgs(args[1:])
	if err != nil {
		return withFormat(err, parsed.Format)
	}
	if parsed.Help {
		printSchemaHelp()
		return nil
	}
	targetParts := append([]string{args[0]}, parsed.Positionals...)
	target := strings.Join(targetParts, " ")
	switch target {
	case "--all", "all":
		return writeData(commandSchema(), parsed.Output())
	case "workflow":
		data, err := os.ReadFile(filepath.Join(cwd, "workflow.schema.json"))
		if err != nil {
			return notFoundError("workflow schema", "workflow.schema.json", parsed.Format)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			return internalError(err)
		}
		return writeData(value, parsed.Output())
	}
	commands, _ := commandSchema()["commands"].([]map[string]any)
	for _, command := range commands {
		if command["name"] == target {
			return writeData(map[string]any{"ok": true, "command": command}, parsed.Output())
		}
	}
	return notFoundError("command schema", target, parsed.Format)
}

func doctorCommand(args []string, cwd string) error {
	parsed, err := parseArgs(args)
	if err != nil {
		return withFormat(err, parsed.Format)
	}
	if parsed.Help {
		fmt.Println("Usage: agentflow doctor [--format json|table]")
		return nil
	}
	providers, err := driver.LoadProviders(cwd)
	if err != nil {
		return internalError(err)
	}
	drivers := make([]string, 0, len(providers))
	statuses := make([]driver.Status, 0, len(providers))
	for name := range providers {
		drivers = append(drivers, name)
		statuses = append(statuses, driver.CheckProvider(name, providers[name]))
	}
	sort.Strings(drivers)
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return writeData(map[string]any{"ok": true, "cwd": cwd, "provider_configured": len(drivers) > 0, "providers": drivers, "drivers": drivers, "provider_status": statuses, "run_store": filepath.Join(cwd, ".agentflow", "runs")}, parsed.Output())
}

func readWorkflow(file, format string) (model.Workflow, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return model.Workflow{}, notFoundError("workflow", file, format)
		}
		return model.Workflow{}, internalError(err)
	}
	var workflow model.Workflow
	if err := json.Unmarshal(data, &workflow); err != nil {
		return workflow, &CommandError{Code: ExitUsageError, Kind: "invalid_json", Message: fmt.Sprintf("invalid workflow JSON: %v", err), Suggestion: "Fix the JSON and validate it again.", Retryable: false, Format: format}
	}
	return workflow, nil
}

func assertValid(workflow model.Workflow, format string) error {
	if problems := schema.Validate(workflow); len(problems) > 0 {
		return &CommandError{Code: ExitUsageError, Kind: "invalid_workflow", Message: strings.Join(problems, "; "), Suggestion: "Run `agentflow schema workflow` and correct the definition.", Retryable: false, Format: format, Input: workflow.Name}
	}
	return nil
}

func parseArgs(args []string) (parsedArgs, error) {
	parsed := parsedArgs{Inputs: map[string]string{}}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			parsed.Positionals = append(parsed.Positionals, args[index+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			parsed.Positionals = append(parsed.Positionals, arg)
			continue
		}
		if arg == "--json" {
			parsed.Format = "json"
			continue
		}
		if arg == "--help" || arg == "-h" {
			parsed.Help = true
			continue
		}
		switch arg {
		case "--dry-run":
			parsed.DryRun = true
			continue
		case "--no-interactive", "--yes":
			parsed.NoInteractive = true
			continue
		case "--page-all":
			parsed.PageAll = true
			continue
		case "--all":
			parsed.All = true
			continue
		case "--no-color":
			continue
		case "--force":
			parsed.Force = true
			continue
		}
		key, value, inline := splitFlag(arg)
		if !inline {
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "--") {
				return parsed, usageError(fmt.Sprintf("flag %s needs a value", key), "Provide a value using the long flag form.", "missing_flag_value")
			}
			index++
			value = args[index]
		}
		switch key {
		case "--format":
			if value != "json" && value != "table" && value != "ndjson" {
				return parsed, usageError("--format must be json, table, or ndjson", "Use `--format json` for agents.", "invalid_format")
			}
			parsed.Format = value
		case "--input":
			name, input, ok := strings.Cut(value, "=")
			if !ok || name == "" {
				return parsed, usageError("--input must use key=value", "Example: `--input issue=ABC-123`.", "invalid_input")
			}
			parsed.Inputs[name] = input
		case "--message":
			parsed.Message = value
		case "--run":
			parsed.RunID = value
		case "--host":
			parsed.Host = value
		case "--port":
			port, portErr := strconv.Atoi(value)
			if portErr != nil || port < 0 || port > 65535 {
				return parsed, usageError("--port must be an integer between 0 and 65535", "Use `--port 7070` or `--port 0` for an available port.", "invalid_port")
			}
			parsed.Port = port
			parsed.PortSet = true
		case "--idempotency-key":
			if !validIdempotencyKey(value) {
				return parsed, usageError("--idempotency-key must be a non-empty token without whitespace", "Use a stable UUID or request token.", "invalid_idempotency_key")
			}
			parsed.IdempotencyKey = value
		case "--page-size":
			size, err := strconv.Atoi(value)
			if err != nil || size < 1 {
				return parsed, usageError("--page-size must be a positive integer", "Use `--page-size 20`.", "invalid_page_size")
			}
			parsed.PageSize = size
		default:
			return parsed, usageError(fmt.Sprintf("unknown flag: %s", key), "Run the command with `--help` to see supported flags.", "unknown_flag")
		}
	}
	return parsed, nil
}

func splitFlag(arg string) (string, string, bool) {
	if index := strings.IndexByte(arg, '='); index > 0 {
		return arg[:index], arg[index+1:], true
	}
	return arg, "", false
}

func validIdempotencyKey(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\t\r\n")
}

type parsedArgs struct {
	Positionals    []string
	Inputs         map[string]string
	Format         string
	Message        string
	IdempotencyKey string
	RunID          string
	Host           string
	Port           int
	PortSet        bool
	PageSize       int
	DryRun         bool
	NoInteractive  bool
	PageAll        bool
	All            bool
	Force          bool
	Help           bool
}

func (p parsedArgs) Output() OutputOptions { return OutputOptions{Format: p.Format} }

func writeData(value any, options OutputOptions) error {
	format := options.resolved()
	switch format {
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	case "ndjson":
		return writeNDJSON(value)
	default:
		return writeTable(value)
	}
}

func writeNDJSON(value any) error {
	if values, ok := value.([]map[string]any); ok {
		for _, item := range values {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetEscapeHTML(false)
			if err := encoder.Encode(item); err != nil {
				return err
			}
		}
		return nil
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func writeTable(value any) error {
	if data, ok := value.(map[string]any); ok {
		if runs, ok := data["runs"].([]map[string]any); ok {
			for _, run := range runs {
				fmt.Printf("%s\t%s\t%s\n", run["run_id"], run["status"], run["workflow"])
			}
			return nil
		}
		if status, ok := data["status"]; ok {
			fmt.Printf("status: %v\n", status)
			if nodes, ok := data["nodes"].(map[string]*model.NodeState); ok {
				ids := make([]string, 0, len(nodes))
				for id := range nodes {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				for _, id := range ids {
					fmt.Printf("  %s: %s\n", id, nodes[id].Status)
				}
			}
			return nil
		}
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

type OutputOptions struct{ Format string }

func (o OutputOptions) resolved() string {
	if o.Format != "" {
		return o.Format
	}
	if stdoutTTY() {
		return "table"
	}
	return "json"
}

func stdoutTTY() bool {
	info, err := os.Stdout.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func runPayload(run *model.Run) map[string]any {
	payload := map[string]any{"ok": run.State.Status != "failed", "run_id": run.RunID, "workflow": run.State.WorkflowName, "status": run.State.Status, "inputs": run.State.Inputs, "nodes": run.State.Nodes, "created_at": run.State.CreatedAt, "updated_at": run.State.UpdatedAt}
	if run.State.StartedAt != "" {
		payload["started_at"] = run.State.StartedAt
	}
	if run.State.FinishedAt != "" {
		payload["finished_at"] = run.State.FinishedAt
	}
	if run.State.DurationMs > 0 {
		payload["duration_ms"] = run.State.DurationMs
	}
	if run.State.IdempotencyKey != "" {
		payload["idempotency_key"] = run.State.IdempotencyKey
	}
	return payload
}

func sameInputs(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func commandSchema() map[string]any {
	flag := func(name, valueType, description string, required bool) map[string]any {
		return map[string]any{"name": name, "type": valueType, "description": description, "required": required}
	}
	formatFlag := map[string]any{"name": "--format", "type": "enum", "enum": []string{"json", "table", "ndjson"}, "default": "json_when_piped_table_when_tty", "description": "Output format", "required": false}
	commands := []map[string]any{
		{"name": "workflow validate", "description": "Validate a workflow JSON definition.", "arguments": []map[string]any{{"name": "workflow.json", "required": true, "type": "path"}}, "flags": []map[string]any{formatFlag}, "examples": []string{"agentflow workflow validate workflow.json --format json"}},
		{"name": "run start", "description": "Create and execute a workflow run.", "arguments": []map[string]any{{"name": "workflow.json", "required": true, "type": "path"}}, "flags": []map[string]any{flag("--input", "key=value", "Workflow input; repeat for multiple inputs", false), flag("--idempotency-key", "string", "Stable request token used to safely retry a start", false), flag("--dry-run", "boolean", "Preview without creating or executing a run", false), formatFlag}, "examples": []string{"agentflow run start workflow.json --input issue=ABC-123 --idempotency-key 7f9c2b1e-4d9c-4f4e-8bb2-8b3d7d6d0a11"}},
		{"name": "run list", "description": "List workflow runs.", "flags": []map[string]any{flag("--page-size", "integer", "Maximum number of runs to return", false), flag("--page-all", "boolean", "Return every run", false), formatFlag}},
		{"name": "run get", "description": "Get one run and its node states.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}}, "flags": []map[string]any{formatFlag}},
		{"name": "run logs", "description": "Read the append-only event log.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}}, "flags": []map[string]any{formatFlag}},
		{"name": "run metrics", "description": "Get one run's timing, attempts, and byte metrics.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}}, "flags": []map[string]any{formatFlag}},
		{"name": "run stats", "description": "Aggregate metrics across runs, optionally filtered by workflow name.", "arguments": []map[string]any{{"name": "workflow-name", "required": false, "type": "string"}}, "flags": []map[string]any{formatFlag}},
		{"name": "run approve", "description": "Approve a waiting human node and continue the run.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}, {"name": "node-id", "required": true, "type": "string"}, {"name": "message", "required": false, "type": "string"}}, "flags": []map[string]any{flag("--message", "string", "Approval message", false), flag("--dry-run", "boolean", "Preview without changing the run", false), formatFlag}},
		{"name": "run reject", "description": "Reject a waiting human node.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}, {"name": "node-id", "required": true, "type": "string"}, {"name": "message", "required": false, "type": "string"}}, "flags": []map[string]any{flag("--message", "string", "Rejection message", false), flag("--dry-run", "boolean", "Preview without changing the run", false), formatFlag}},
		{"name": "run resume", "description": "Resume a persisted run.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}}, "flags": []map[string]any{flag("--dry-run", "boolean", "Preview pending nodes without executing them", false), formatFlag}},
		{"name": "run cancel", "description": "Cancel a run.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}}, "flags": []map[string]any{flag("--dry-run", "boolean", "Preview without changing the run", false), formatFlag}},
		{"name": "schema", "description": "Inspect command metadata or the workflow schema.", "arguments": []map[string]any{{"name": "target", "required": false, "type": "string", "examples": []string{"--all", "workflow", "run start"}}}, "flags": []map[string]any{flag("--all", "boolean", "Return every command", false), formatFlag}},
		{"name": "config init", "description": "Create local provider configuration.", "flags": []map[string]any{flag("--dry-run", "boolean", "Preview without writing a file", false), formatFlag}},
		{"name": "doctor", "description": "Check local setup and configured providers.", "flags": []map[string]any{formatFlag}},
		{"name": "web", "description": "Open a local React Flow visualization for a workflow or run.", "arguments": []map[string]any{{"name": "workflow.json", "required": false, "type": "path"}}, "flags": []map[string]any{flag("--run", "string", "Open an existing run instead of a workflow file", false), flag("--host", "string", "HTTP bind host", false), flag("--port", "integer", "HTTP port; 0 chooses an available port", false)}},
		{"name": "version", "description": "Print version, commit, build time, Go version, and platform.", "flags": []map[string]any{formatFlag}},
	}
	return map[string]any{"schema_version": "1", "name": "agentflow", "description": "Run durable local workflows for Claude Code, Codex, and other agents.", "commands": commands}
}

func isRunVerb(value string) bool {
	switch value {
	case "start", "list", "get", "logs", "metrics", "stats", "approve", "reject", "resume", "cancel":
		return true
	default:
		return false
	}
}

func isHelp(value string) bool { return value == "help" || value == "--help" || value == "-h" }

func printHelp(args []string) {
	if len(args) == 0 {
		printRootHelp()
		return
	}
	switch args[0] {
	case "workflow":
		printWorkflowHelp()
	case "run":
		printRunHelp()
	case "schema":
		printSchemaHelp()
	case "config":
		printConfigHelp()
	case "doctor":
		printDoctorHelp()
	case "web":
		printWebHelp()
	case "version":
		printVersionHelp()
	default:
		printRootHelp()
	}
}

func printRootHelp() {
	fmt.Println(`agentflow - local workflow runner for AI agents

Usage:
  agentflow <noun> <verb> [arguments] [flags]

Commands:
  workflow validate  Validate a workflow JSON definition
  run start          Start a workflow run
  run list           List runs
  run get            Get one run
  run logs           Read run events
  run approve       Approve a human node
  run reject        Reject a human node
  run resume        Resume a persisted run
  run cancel        Cancel a run
  schema             Inspect commands or the workflow schema
  config init        Create provider configuration
  doctor             Check local setup
  web                Open workflow/run visualization in a browser
  version             Print version and build information

Examples:
  agentflow workflow validate workflow.json --format json
  agentflow run start workflow.json --input issue=ABC-123 --format json
  agentflow run get <run-id> --format json

Output defaults to JSON when stdout is not a TTY. Use --format table for humans.

Exit codes: 0 success, 1 execution error, 2 usage error, 3 not found,
4 permission denied, 5 conflict, 10 dry-run passed.`)
}

func printWorkflowHelp() {
	fmt.Println(`Usage:
  agentflow workflow validate <workflow.json> [--format json|table]

Validates version, node types, dependencies, required fields, and cycles.

Examples:
  agentflow workflow validate workflow.json --format json`)
}

func printRunHelp() {
	fmt.Println(`Usage:
  agentflow run <verb> [arguments] [flags]

Verbs:
  start <workflow.json>   Execute a workflow
  list                    List runs
  get <run-id>            Get one run
  logs <run-id>           Read events
  metrics <run-id>        Show timing and size metrics
  stats [workflow-name]   Aggregate metrics across runs
  approve <run-id> <node-id> [message]
  reject <run-id> <node-id> [message]
  resume <run-id>
  cancel <run-id>

Common flags:
  --format json|table|ndjson   Output format (default: JSON when piped)
  --dry-run                    Preview side effects
  --idempotency-key UUID       Reuse an existing run safely (start only)
  --no-interactive             Never prompt

Examples:
  agentflow run start workflow.json --input issue=ABC-123 --format json
  agentflow run approve <run-id> approval --message "looks good" --format json`)
}

func printSchemaHelp() {
	fmt.Println(`Usage:
  agentflow schema --all
  agentflow schema workflow
  agentflow schema run start

Returns machine-readable command metadata or workflow.schema.json.`)
}

func printConfigHelp() {
	fmt.Println(`Usage:
  agentflow config init [--format json|table]

Creates .agentflow/providers.json from the provider template.`)
}

func printDoctorHelp() {
	fmt.Println(`Usage:
  agentflow doctor [--format json|table]

Checks provider configuration and the local run store.`)
}

func printWebHelp() {
	fmt.Println(`Usage:
  agentflow web [workflow.json] [--host 127.0.0.1] [--port 7070]
  agentflow web --run <run-id> [--host 127.0.0.1] [--port 7070]

Starts a local web UI with a React Flow graph. A workflow file opens a definition preview;
--run opens a persisted run with live node status and metrics. The page refreshes run data automatically.

Examples:
  agentflow web workflow.json
  agentflow web --run run_20261010002357_d9293c42a2cbf87b
  agentflow web workflow.json --port 8080`)
}

func printVersionHelp() {
	fmt.Println(`Usage:
  agentflow version [--format json|table]

Prints the version, commit, build time, Go version, and platform.`)
}

func NewCommandError(kind, message, suggestion string, code int) *CommandError {
	return &CommandError{Code: code, Kind: kind, Message: message, Suggestion: suggestion}
}

func usageError(message, suggestion, kind string) error {
	return &CommandError{Code: ExitUsageError, Kind: kind, Message: message, Suggestion: suggestion, Retryable: false}
}
func notFoundError(resource, value, format string) error {
	return &CommandError{Code: ExitNotFound, Kind: "not_found", Message: fmt.Sprintf("%s not found: %s", resource, value), Suggestion: "Check the identifier and run `agentflow run list`.", Retryable: false, Input: value, Format: format}
}
func conflictError(message, suggestion, format string) error {
	return &CommandError{Code: ExitConflict, Kind: "conflict", Message: message, Suggestion: suggestion, Retryable: false, Format: format}
}
func internalError(err error) error {
	return &CommandError{Code: ExitGeneralError, Kind: "internal_error", Message: err.Error(), Suggestion: "Inspect the command and run logs before retrying.", Retryable: true}
}
func withFormat(err error, format string) error {
	if commandErr, ok := err.(*CommandError); ok && commandErr.Format == "" {
		commandErr.Format = format
	}
	return err
}

func PrintError(err error) {
	if err == nil {
		return
	}
	commandErr, ok := err.(*CommandError)
	if !ok {
		commandErr = internalError(err).(*CommandError)
	}
	if commandErr.Silent {
		return
	}
	if commandErr.Format == "json" || !stderrTTY() {
		payload := map[string]any{"ok": false, "error": map[string]any{"code": commandErr.Kind, "message": commandErr.Message, "suggestion": commandErr.Suggestion, "retryable": commandErr.Retryable, "input": commandErr.Input}}
		encoder := json.NewEncoder(os.Stderr)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(payload)
		return
	}
	fmt.Fprintf(os.Stderr, "agentflow: %s\n", commandErr.Message)
	if commandErr.Suggestion != "" {
		fmt.Fprintf(os.Stderr, "suggestion: %s\n", commandErr.Suggestion)
	}
}

func ExitCode(err error) int {
	if commandErr, ok := err.(*CommandError); ok {
		return commandErr.Code
	}
	return ExitGeneralError
}

func stderrTTY() bool {
	info, err := os.Stderr.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
