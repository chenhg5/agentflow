package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	schemaassets "github.com/chenhg5/agentflow"
	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/engine"
	"github.com/chenhg5/agentflow/internal/graph"
	"github.com/chenhg5/agentflow/internal/metrics"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/queue"
	"github.com/chenhg5/agentflow/internal/schema"
	"github.com/chenhg5/agentflow/internal/store"
	"github.com/chenhg5/agentflow/internal/trigger"
	"github.com/chenhg5/agentflow/internal/version"
	"github.com/chenhg5/agentflow/internal/web"
	"github.com/chenhg5/agentflow/internal/workspace"
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
	root := cwd
	switch args[0] {
	case "workspace":
		return workspaceCommand(args[1:], cwd)
	case "executor":
		return executorCommand(args[1:], cwd)
	case "workflow":
		return workflowCommand(args[1:])
	case "resource":
		return resourceCommand(args[1:], root)
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
	case "trigger":
		return triggerCommand(args[1:], root, cwd)
	case "version":
		return versionCommand(args[1:])
	default:
		return usageError(fmt.Sprintf("unknown command: %s", args[0]), "Run `agentflow --help` to see the command tree.", "command")
	}
}

func triggerCommand(args []string, root, cwd string) error {
	if len(args) == 0 || isHelp(args[0]) {
		printTriggerHelp()
		return nil
	}
	verb := args[0]
	parsed, err := parseArgs(args[1:])
	if err != nil {
		return withFormat(err, parsed.Format)
	}
	if parsed.Help {
		printTriggerHelp()
		return nil
	}
	config, err := trigger.LoadConfig(root)
	if err != nil {
		if os.IsNotExist(err) {
			return notFoundError("trigger config", filepath.Join(root, "triggers.json"), parsed.Format)
		}
		return usageError(err.Error(), "Run `agentflow schema trigger` and correct triggers.json.", "invalid_trigger_config")
	}
	switch verb {
	case "list":
		return writeData(map[string]any{"ok": true, "triggers": config.Triggers}, parsed.Output())
	case "status":
		cursors, err := trigger.CronStatus(root)
		if err != nil {
			return internalError(err)
		}
		deliveries, err := trigger.DeliveryStatus(root)
		if err != nil {
			return internalError(err)
		}
		return writeData(map[string]any{"ok": true, "schedules": cursors, "deliveries": deliveries}, parsed.Output())
	case "validate":
		return writeData(map[string]any{"ok": true, "valid": true, "count": len(config.Triggers)}, parsed.Output())
	case "fire":
		if len(parsed.Positionals) != 1 {
			return usageError("trigger id is required", "Run `agentflow trigger fire <trigger-id>`.", "trigger_id")
		}
		definition, err := findTrigger(config, parsed.Positionals[0])
		if err != nil {
			return notFoundError("trigger", parsed.Positionals[0], parsed.Format)
		}
		runtime, err := trigger.NewRuntime(root, cwd, 1)
		if err != nil {
			return internalError(err)
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		now := time.Now().UTC()
		if parsed.ScheduledAt != "" {
			parsedTime, parseErr := time.Parse(time.RFC3339, parsed.ScheduledAt)
			if parseErr != nil {
				return usageError("--scheduled-at must be RFC3339", "Example: --scheduled-at 2026-10-13T02:00:00+08:00", "invalid_scheduled_at")
			}
			now = parsedTime.UTC()
		}
		deliveryID := parsed.DeliveryID
		if deliveryID == "" {
			deliveryID = definition.ID + ":" + now.Format(time.RFC3339)
		}
		event := trigger.Event{Manual: true, TriggerID: definition.ID, SourceKind: definition.Source.Kind, DeliveryID: deliveryID, ReceivedAt: time.Now().UTC(), ScheduledAt: &now, Attributes: map[string]string{}}
		dispatcher := &trigger.Dispatcher{Root: root, Config: config, Starter: runtime}
		result, err := dispatcher.Dispatch(ctx, event)
		if err != nil {
			if errors.Is(err, store.ErrIdempotencyConflict) {
				return conflictError(err.Error(), "Reuse the original workflow and inputs or choose a new delivery ID.", parsed.Format)
			}
			return internalError(err)
		}
		payload := map[string]any{"ok": true, "trigger": result}
		if result.Run.RunID != "" && result.Status == "queued" {
			run, waitErr := queue.Drive(ctx, root, cwd, result.Run.RunID)
			if waitErr != nil && ctx.Err() != context.Canceled {
				return internalError(waitErr)
			}
			if run != nil {
				payload["run_id"] = run.RunID
				if job, err := store.LoadDispatch(root, run.RunID); err == nil {
					payload["dispatch"] = job
					payload["queued"] = job.Generation > job.Completed
				}
				payload["status"] = run.State.Status
				if run.State.Status == "failed" {
					_ = writeData(payload, parsed.Output())
					return &CommandError{Code: ExitGeneralError, Kind: "workflow_failed", Message: "workflow failed", Format: parsed.Format, Silent: true}
				}
			}
		}
		return writeData(payload, parsed.Output())
	case "serve":
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		err := trigger.Serve(ctx, root, cwd, trigger.ServeOptions{Host: parsed.Host, Port: parsed.Port, Workers: parsed.workerConcurrency()})
		if errors.Is(err, trigger.ErrServiceBusy) {
			return conflictError(err.Error(), "Use the trigger service already running for this workspace.", parsed.Format)
		}
		return err
	default:
		return usageError(fmt.Sprintf("unknown trigger action: %s", verb), "Use `agentflow trigger --help` to list actions.", "trigger_action")
	}
}

func findTrigger(config trigger.Config, id string) (trigger.Definition, error) {
	for _, definition := range config.Triggers {
		if definition.ID == id {
			return definition, nil
		}
	}
	return trigger.Definition{}, fmt.Errorf("trigger not found: %s", id)
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
	if parsed.CheckExecutors {
		cwd, err := os.Getwd()
		if err != nil {
			return internalError(err)
		}
		executors, err := driver.LoadExecutors(cwd)
		if err != nil {
			return usageError(err.Error(), "Correct executors.json.", "invalid_executor_config")
		}
		for _, node := range workflow.Nodes {
			for index, candidate := range node.Execution.Candidates {
				if _, ok := executors[candidate.Executor]; !ok {
					return usageError(fmt.Sprintf("node %s candidate %d references unregistered executor %q", node.ID, index+1, candidate.Executor), "Register its command in executors.json or choose a built-in executor.", "unregistered_executor")
				}
			}
		}
	}
	return writeData(map[string]any{
		"ok":       true,
		"valid":    true,
		"workflow": workflow.Name,
		"version":  workflow.Version,
		"graph":    graph.Describe(workflow),
	}, parsed.Output())
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
	case "enqueue":
		parsed.Enqueue = true
		return runStart(parsed, root, cwd)
	case "worker":
		return runWorker(parsed, root, cwd)
	case "queue":
		data, err := store.QueueSnapshot(root)
		if err != nil {
			return internalError(err)
		}
		if resources, err := store.ResourceStatus(context.Background(), root); err == nil {
			data["resources"] = resources
		} else {
			return internalError(err)
		}
		return writeData(data, parsed.Output())
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
	case "evaluate":
		return runEvaluate(parsed, root)
	case "compare":
		return runCompare(parsed, root)
	case "latest":
		return runLatest(parsed, root)
	case "submit":
		return runSubmit(parsed, root, cwd)
	case "resume":
		return runResume(parsed, root, cwd)
	case "cancel":
		return runCancel(parsed, root, cwd)
	case "retry":
		return runRetry(parsed, root, cwd)
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
	executors, err := driver.LoadExecutors(cwd)
	if err != nil {
		return internalError(err)
	}
	if err := engine.ValidateExecutors(workflow, executors); err != nil {
		return usageError(err.Error(), "Register every candidate in executors.json or use a built-in executor.", "unregistered_executor")
	}
	if parsed.DryRun {
		nodes := make([]map[string]any, 0, len(workflow.Nodes))
		for _, node := range workflow.Nodes {
			plan := map[string]any{"id": node.ID, "inputSource": "run-input-or-incoming-edge", "execution": node.Execution}
			nodes = append(nodes, plan)
		}
		if err := writeData(map[string]any{"ok": true, "dry_run": true, "action": "run.start", "workflow": workflow.Name, "inputs": parsed.Inputs, "entry": workflow.Entry, "nodes": nodes, "edges": workflow.Edges}, parsed.Output()); err != nil {
			return err
		}
		return &CommandError{Code: ExitDryRunPassed, Kind: "dry_run_passed", Message: "dry run passed", Format: parsed.Format, Silent: true}
	}
	if err := store.EnsureDir(root); err != nil {
		return internalError(err)
	}
	if parsed.Enqueue || parsed.Async {
		if err := store.EnableQueue(context.Background(), root); err != nil {
			return internalError(err)
		}
	}
	created, err := store.CreateOrGetRun(context.Background(), root, store.CreateRequest{Workflow: workflow, Inputs: parsed.Inputs, IdempotencyKey: parsed.IdempotencyKey, Queued: true, ParentRunID: parsed.ParentRunID, ParentNodeID: parsed.ParentNodeID, Correlation: parsed.Correlation})
	if errors.Is(err, store.ErrIdempotencyConflict) {
		return conflictError(err.Error(), "Reuse the original workflow and inputs or choose a new `--idempotency-key`.", parsed.Format)
	}
	if err != nil {
		return internalError(err)
	}
	run := created.Run
	if !parsed.Enqueue && !parsed.Async {
		run, err = queue.Drive(context.Background(), root, cwd, run.RunID)
		if err != nil {
			return internalError(err)
		}
	}

	payload := runPayload(run)
	if !created.Created {
		payload["idempotent_reused"] = true
	}
	if err := writeData(payload, parsed.Output()); err != nil {
		return err
	}
	if run.State.Status == "failed" && !dispatchPending(run) {
		return &CommandError{Code: ExitGeneralError, Kind: "workflow_failed", Message: "workflow failed", Suggestion: fmt.Sprintf("Inspect with `agentflow run get %s` and `agentflow run logs %s`.", run.RunID, run.RunID), Retryable: false, Format: parsed.Format, Silent: true}
	}
	return nil
}

func runList(parsed parsedArgs, root string) error {
	runs, err := selectRuns(root, parsed.Workflow, parsed.Statuses)
	if err != nil {
		return err
	}
	summaries := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		if parsed.ParentRunID != "" && run.State.ParentRunID != parsed.ParentRunID {
			continue
		}
		matches := true
		for key, value := range parsed.Correlation {
			if actual, ok := run.State.Correlation[key]; !ok || actual != value {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		summary := map[string]any{"run_id": run.RunID, "workflow": run.State.WorkflowName, "workflow_digest": run.WorkflowDigest(), "status": run.State.Status, "created_at": run.State.CreatedAt, "updated_at": run.State.UpdatedAt}
		if run.State.FinishedAt != "" {
			summary["finished_at"] = run.State.FinishedAt
		}
		summary["parent_run_id"] = run.State.ParentRunID
		summary["parent_node_id"] = run.State.ParentNodeID
		summary["correlation"] = run.State.Correlation
		summaries = append(summaries, summary)
		if parsed.PageSize > 0 && len(summaries) >= parsed.PageSize && !parsed.PageAll {
			break
		}
	}
	return writeData(map[string]any{"ok": true, "runs": summaries}, parsed.Output())
}

// runLatest gives schedulers and watchdogs a stable way to ask "how did the
// last run of this workflow go" without reading the run directory layout.
func runLatest(parsed parsedArgs, root string) error {
	if len(parsed.Positionals) != 1 {
		return usageError("workflow name is required", "Run `agentflow run latest <workflow-name> [--status succeeded,failed]`.", "workflow_name")
	}
	runs, err := selectRuns(root, parsed.Positionals[0], parsed.Statuses)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return notFoundError("run for workflow", parsed.Positionals[0], parsed.Format)
	}
	return writeData(runPayload(runs[0]), parsed.Output())
}

// selectRuns returns matching runs, newest first.
func selectRuns(root, workflow string, statuses []string) ([]*model.Run, error) {
	runs, err := loadRuns(root)
	if err != nil {
		return nil, err
	}
	selected := make([]*model.Run, 0, len(runs))
	for _, run := range runs {
		if workflow != "" && run.State.WorkflowName != workflow {
			continue
		}
		if len(statuses) > 0 && !slices.Contains(statuses, run.State.Status) {
			continue
		}
		selected = append(selected, run)
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].State.CreatedAt != selected[j].State.CreatedAt {
			return selected[i].State.CreatedAt > selected[j].State.CreatedAt
		}
		return selected[i].RunID > selected[j].RunID
	})
	return selected, nil
}

func runGet(parsed parsedArgs, root string) error {
	if len(parsed.Positionals) != 1 {
		return usageError("run id is required", "Run `agentflow run get <run-id>`.", "run_id")
	}
	run, err := store.LoadRun(root, parsed.Positionals[0])
	if err != nil {
		return notFoundError("run", parsed.Positionals[0], parsed.Format)
	}
	payload := runPayload(run)
	children, counts, err := store.Children(root, run.RunID)
	if err != nil {
		return internalError(err)
	}
	refs := []map[string]any{}
	for _, child := range children {
		refs = append(refs, map[string]any{"run_id": child.RunID, "status": child.State.Status, "parent_node_id": child.State.ParentNodeID, "correlation": child.State.Correlation})
	}
	payload["children"] = refs
	payload["child_status_counts"] = counts
	return writeData(payload, parsed.Output())
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

func runEvaluate(parsed parsedArgs, root string) error {
	if len(parsed.Positionals) != 1 || parsed.EvaluationJSON == "" {
		return usageError("usage: agentflow run evaluate <run-id> --evaluation '{...}'", "Provide numeric measurements with optional direction, unit, labels, and notes.", "evaluation_arguments")
	}
	run, err := store.LoadRun(root, parsed.Positionals[0])
	if err != nil {
		return notFoundError("run", parsed.Positionals[0], parsed.Format)
	}
	var evaluation model.Evaluation
	decoder := json.NewDecoder(strings.NewReader(parsed.EvaluationJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&evaluation); err != nil {
		return usageError(fmt.Sprintf("--evaluation must be a JSON object: %v", err), "Use {\"measurements\":{\"quality\":{\"value\":0.9,\"direction\":\"higher\"}}}.", "invalid_evaluation")
	}
	if evaluation.EvaluatedAt == "" {
		evaluation.EvaluatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if len(evaluation.Measurements) == 0 {
		return usageError("evaluation must contain at least one measurement", "Add a numeric measurements entry with value and optional direction/unit.", "empty_evaluation")
	}
	if err := metrics.ValidateEvaluation(evaluation); err != nil {
		return usageError(err.Error(), "Measurement directions are higher, lower, or neutral.", "invalid_evaluation")
	}
	release, _, err := store.AcquireRunLock(run.RunDir)
	if err != nil {
		if errors.Is(err, store.ErrRunBusy) {
			return conflictError(err.Error(), "Wait for the run to pause before recording an evaluation.", parsed.Format)
		}
		return internalError(err)
	}
	defer release()
	run.State.Evaluation = &evaluation
	run.State.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := store.SaveEvaluation(run); err != nil {
		return internalError(err)
	}
	if err := store.SaveState(run); err != nil {
		return internalError(err)
	}
	names := make([]string, 0, len(evaluation.Measurements))
	for name := range evaluation.Measurements {
		names = append(names, name)
	}
	sort.Strings(names)
	if err := store.AppendEvent(run.RunDir, map[string]any{"type": "run.evaluated", "runId": run.RunID, "source": evaluation.Source, "measurements": names}); err != nil {
		return internalError(err)
	}
	return writeData(runPayload(run), parsed.Output())
}

func runCompare(parsed parsedArgs, root string) error {
	if len(parsed.Positionals) < 1 || len(parsed.Positionals) > 2 {
		return usageError("usage: agentflow run compare <run-id> [baseline-run-id]", "Omit the baseline id to compare with the previous finished run of the same workflow.", "compare_arguments")
	}
	current, err := store.LoadRun(root, parsed.Positionals[0])
	if err != nil {
		return notFoundError("run", parsed.Positionals[0], parsed.Format)
	}
	var baseline *model.Run
	if len(parsed.Positionals) == 2 {
		baseline, err = store.LoadRun(root, parsed.Positionals[1])
		if err != nil {
			return notFoundError("baseline run", parsed.Positionals[1], parsed.Format)
		}
	} else {
		runs, loadErr := loadRuns(root)
		if loadErr != nil {
			return loadErr
		}
		baseline = previousFinishedRun(current, runs)
		if baseline == nil {
			return notFoundError("previous finished run for workflow", current.State.WorkflowName, parsed.Format)
		}
	}
	return writeData(metrics.Compare(current, baseline, time.Now().UTC()), parsed.Output())
}

func previousFinishedRun(current *model.Run, runs []*model.Run) *model.Run {
	var previous *model.Run
	for _, candidate := range runs {
		if candidate == nil || candidate.RunID == current.RunID || candidate.State.WorkflowName != current.State.WorkflowName {
			continue
		}
		if candidate.State.Status != "succeeded" && candidate.State.Status != "failed" && candidate.State.Status != "cancelled" {
			continue
		}
		if current.State.CreatedAt != "" && candidate.State.CreatedAt >= current.State.CreatedAt {
			continue
		}
		if previous == nil || candidate.State.CreatedAt > previous.State.CreatedAt {
			previous = candidate
		}
	}
	return previous
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
	asOf := time.Now().UTC()
	if parsed.ByVersion {
		return writeData(map[string]any{"ok": true, "workflow": workflowName, "as_of": asOf.Format(time.RFC3339Nano), "versions": metrics.AggregateByDigest(runs, workflowName, asOf)}, parsed.Output())
	}
	return writeData(metrics.Aggregate(runs, metrics.Filter{Workflow: workflowName, Digest: parsed.Digest}, asOf), parsed.Output())
}

func loadRuns(root string) ([]*model.Run, error) {
	ids, err := store.RunIDs(root)
	if err != nil {
		return nil, internalError(err)
	}
	runs := make([]*model.Run, 0, len(ids))
	for _, id := range ids {
		run, err := store.LoadRun(root, id)
		if err != nil {
			return nil, internalError(err)
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func runSubmit(parsed parsedArgs, root, cwd string) error {
	if len(parsed.Positionals) != 2 || parsed.SubmittedOutput == "" {
		return usageError("usage: agentflow run submit <run-id> <node-id> --output '{...}'", "The node must be waiting_input.", "submit_arguments")
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(parsed.SubmittedOutput), &output); err != nil || output == nil {
		return usageError("--output must be a JSON object", "Supply an object matching the node outputSchema.", "invalid_output")
	}
	run, err := store.LoadRun(root, parsed.Positionals[0])
	if err != nil {
		return notFoundError("run", parsed.Positionals[0], parsed.Format)
	}
	nodeID := parsed.Positionals[1]
	if run.State.Status == "cancelled" || run.State.Status == "succeeded" || run.State.Status == "failed" {
		return conflictError("cannot submit output to a terminal run", "Inspect the run before submitting.", parsed.Format)
	}
	node, ok := run.State.Nodes[nodeID]
	if !ok {
		return notFoundError("node", nodeID, parsed.Format)
	}
	if node.Status != "waiting_input" {
		return conflictError(fmt.Sprintf("node %s is not waiting for input", nodeID), "Inspect the run before submitting.", parsed.Format)
	}
	if parsed.SubmissionToken != "" && parsed.SubmissionToken != node.LeaseToken {
		return conflictError("submission token does not match the current invocation", "Read the current node leaseToken before submitting.", parsed.Format)
	}
	for _, definition := range run.Workflow.Nodes {
		if definition.ID == nodeID {
			if problems := schema.ValidateValue("output", output, definition.OutputSchema); len(problems) > 0 {
				return &CommandError{Code: ExitUsageError, Kind: "invalid_output", Message: schema.FormatContractErrors("output does not satisfy outputSchema", problems), Format: parsed.Format}
			}
			break
		}
	}
	if parsed.DryRun {
		if err := writeData(map[string]any{"ok": true, "dry_run": true, "action": "run.submit", "run_id": run.RunID, "node_id": nodeID, "output": output}, parsed.Output()); err != nil {
			return err
		}
		return &CommandError{Code: ExitDryRunPassed, Kind: "dry_run_passed", Message: "dry run passed", Format: parsed.Format, Silent: true}
	}
	// Bind even interactive submissions to the invocation observed above. A queued
	// control must not affect a later visit of the same node.
	return enqueueAndExecute(run, model.Control{Kind: engine.ControlSubmit, NodeID: nodeID, Token: node.LeaseToken, Output: output}, parsed, cwd)
}

// enqueueAndExecute hands a request to the run's executor. When another
// process is executing the run, the request stays queued for it and the
// current state is returned with queued=true.
func enqueueAndExecute(run *model.Run, control model.Control, parsed parsedArgs, cwd string) error {
	if err := store.EnqueueControl(run.RunDir, control); err != nil {
		return internalError(err)
	}
	root := filepath.Dir(filepath.Dir(run.RunDir))
	if _, err := store.WakeRun(context.Background(), root, run.RunID, control.Kind); err != nil {
		return internalError(err)
	}
	return executeRun(run, parsed, cwd, true)
}

func executeRun(run *model.Run, parsed parsedArgs, cwd string, queueOnBusy bool) error {
	root := filepath.Dir(filepath.Dir(run.RunDir))
	if parsed.Async {
		if err := store.EnableQueue(context.Background(), root); err != nil {
			return internalError(err)
		}
	}
	if !queueOnBusy {
		if _, err := store.WakeRun(context.Background(), root, run.RunID, "resume"); err != nil {
			return internalError(err)
		}
	}
	run, err := queue.Drive(context.Background(), root, cwd, run.RunID)
	if err != nil {
		return internalError(err)
	}

	if err := writeData(runPayload(run), parsed.Output()); err != nil {
		return err
	}
	if run.State.Status == "failed" && !dispatchPending(run) {
		return &CommandError{Code: ExitGeneralError, Kind: "workflow_failed", Message: "workflow failed", Suggestion: fmt.Sprintf("Inspect with `agentflow run get %s`, then `agentflow run retry %s` after fixing the cause.", run.RunID, run.RunID), Format: parsed.Format, Silent: true}
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
			if node.Status == "pending" || node.Status == "running" {
				pending = append(pending, id)
			}
		}
		sort.Strings(pending)
		if err := writeData(map[string]any{"ok": true, "dry_run": true, "action": "run.resume", "run_id": run.RunID, "current_status": run.State.Status, "pending_nodes": pending}, parsed.Output()); err != nil {
			return err
		}
		return &CommandError{Code: ExitDryRunPassed, Kind: "dry_run_passed", Message: "dry run passed", Format: parsed.Format, Silent: true}
	}
	return executeRun(run, parsed, cwd, false)
}

func runRetry(parsed parsedArgs, root, cwd string) error {
	if len(parsed.Positionals) != 1 {
		return usageError("run id is required", "Run `agentflow run retry <run-id> [--from node-id]`.", "run_id")
	}
	run, err := store.LoadRun(root, parsed.Positionals[0])
	if err != nil {
		return notFoundError("run", parsed.Positionals[0], parsed.Format)
	}
	nodes, err := engine.RetryPlan(run, parsed.From)
	if err != nil {
		return conflictError(err.Error(), "Pass `--from <node-id>` to choose where to restart.", parsed.Format)
	}
	if parsed.DryRun {
		if err := writeData(map[string]any{"ok": true, "dry_run": true, "action": "run.retry", "run_id": run.RunID, "reset_nodes": nodes}, parsed.Output()); err != nil {
			return err
		}
		return &CommandError{Code: ExitDryRunPassed, Kind: "dry_run_passed", Message: "dry run passed", Format: parsed.Format, Silent: true}
	}
	return enqueueAndExecute(run, model.Control{Kind: engine.ControlRetry, Nodes: parsed.From}, parsed, cwd)
}

func runCancel(parsed parsedArgs, root, cwd string) error {
	if len(parsed.Positionals) != 1 {
		return usageError("run id is required", "Run `agentflow run cancel <run-id>`.", "run_id")
	}
	run, err := store.LoadRun(root, parsed.Positionals[0])
	if err != nil {
		return notFoundError("run", parsed.Positionals[0], parsed.Format)
	}
	if run.State.Status == "succeeded" || run.State.Status == "failed" || run.State.Status == "cancelled" {
		return conflictError("run is already terminal", "Inspect the completed run instead of cancelling it.", parsed.Format)
	}
	if parsed.DryRun {
		if err := writeData(map[string]any{"ok": true, "dry_run": true, "action": "run.cancel", "run_id": run.RunID, "current_status": run.State.Status}, parsed.Output()); err != nil {
			return err
		}
		return &CommandError{Code: ExitDryRunPassed, Kind: "dry_run_passed", Message: "dry run passed", Format: parsed.Format, Silent: true}
	}
	return enqueueAndExecute(run, model.Control{Kind: engine.ControlCancel}, parsed, cwd)
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
	directory := cwd
	target := filepath.Join(directory, "executors.json")
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
		return conflictError(fmt.Sprintf("executor config already exists: %s", target), "Edit the existing file or remove it intentionally.", parsed.Format)
	} else if !os.IsNotExist(err) {
		return internalError(err)
	}
	const example = `{
  "executors": {
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
	case "workspace":
		return writeData(workspace.Contract(), parsed.Output())
	case "--all", "all":
		return writeData(commandSchema(), parsed.Output())
	case "workflow", "trigger", "executors", "resources":
		data, err := schemaassets.ReadSchema(target)
		if err != nil {
			return internalError(err)
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
	executors, err := driver.LoadExecutors(cwd)
	if err != nil {
		return internalError(err)
	}
	drivers := make([]string, 0, len(executors))
	statuses := make([]driver.Status, 0, len(executors))
	for name := range executors {
		drivers = append(drivers, name)
		statuses = append(statuses, driver.CheckExecutor(name, executors[name]))
	}
	sort.Strings(drivers)
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return writeData(map[string]any{"ok": true, "cwd": cwd, "executors": drivers, "executor_status": statuses, "run_store": filepath.Join(cwd, "runs")}, parsed.Output())
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
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&workflow); err != nil {
		return workflow, &CommandError{Code: ExitUsageError, Kind: "invalid_json", Message: fmt.Sprintf("invalid workflow JSON: %v", err), Suggestion: "Fix the JSON and validate it again.", Retryable: false, Format: format}
	}
	if problems := schema.ValidateWorkflowJSON(data); len(problems) > 0 {
		return workflow, &CommandError{Code: ExitUsageError, Kind: "invalid_workflow", Message: strings.Join(problems, "; "), Suggestion: "Run `agentflow schema workflow` and correct the definition.", Retryable: false, Format: format, Input: workflow.Name}
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
	parsed := parsedArgs{Inputs: map[string]string{}, Correlation: map[string]string{}}
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
		case "--confirm-stopped":
			parsed.ConfirmStopped = true
			continue
		case "--async":
			parsed.Async = true
			continue
		case "--check-executors":
			parsed.CheckExecutors = true
			continue
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
		case "--by-version":
			parsed.ByVersion = true
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
		case "--kind":
			parsed.Kind = value
		case "--workspace":
			parsed.ExecutorWorkspace = value
		case "--command":
			parsed.CommandJSON = value
		case "--model":
			parsed.Model = value
		case "--format":
			if value != "json" && value != "table" && value != "ndjson" {
				return parsed, usageError("--format must be json, table, or ndjson", "Use `--format json` for agents.", "invalid_format")
			}
			parsed.Format = value
		case "--reason":
			parsed.Reason = value
		case "--concurrency":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 {
				return parsed, usageError("--concurrency must be positive", "Choose the maximum active run count.", "invalid_concurrency")
			}
			parsed.Concurrency = n
		case "--parent":
			parsed.ParentRunID = value
		case "--parent-node":
			parsed.ParentNodeID = value
		case "--correlation":
			name, v, ok := strings.Cut(value, "=")
			if !ok || name == "" {
				return parsed, usageError("--correlation must use key=value", "Repeat for multiple labels.", "invalid_correlation")
			}
			parsed.Correlation[name] = v
		case "--input":
			name, input, ok := strings.Cut(value, "=")
			if !ok || name == "" {
				return parsed, usageError("--input must use key=value", "Example: `--input issue=ABC-123`.", "invalid_input")
			}
			parsed.Inputs[name] = input
		case "--output":
			parsed.SubmittedOutput = value
		case "--token":
			if strings.TrimSpace(value) == "" {
				return parsed, usageError("--token cannot be empty", "Use the waiting node leaseToken.", "invalid_token")
			}
			parsed.SubmissionToken = value
		case "--evaluation":
			parsed.EvaluationJSON = value
		case "--workflow":
			parsed.Workflow = value
		case "--status":
			for _, status := range strings.Split(value, ",") {
				if status = strings.TrimSpace(status); status != "" {
					parsed.Statuses = append(parsed.Statuses, status)
				}
			}
		case "--digest":
			parsed.Digest = value
		case "--from":
			for _, node := range strings.Split(value, ",") {
				if node = strings.TrimSpace(node); node != "" {
					parsed.From = append(parsed.From, node)
				}
			}
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
		case "--scheduled-at":
			parsed.ScheduledAt = value
		case "--delivery-id":
			if !validIdempotencyKey(value) {
				return parsed, usageError("--delivery-id must be a non-empty token without whitespace", "Use a stable delivery identifier.", "invalid_delivery_id")
			}
			parsed.DeliveryID = value
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
	ConfirmStopped    bool
	Reason            string
	Enqueue           bool
	Async             bool
	Concurrency       int
	ParentRunID       string
	ParentNodeID      string
	Correlation       map[string]string
	Kind              string
	ExecutorWorkspace string
	CommandJSON       string
	Model             string
	CheckExecutors    bool
	Positionals       []string
	Inputs            map[string]string
	Format            string
	SubmittedOutput   string
	SubmissionToken   string
	EvaluationJSON    string
	Workflow          string
	Statuses          []string
	Digest            string
	From              []string
	IdempotencyKey    string
	RunID             string
	Host              string
	Port              int
	PortSet           bool
	ScheduledAt       string
	DeliveryID        string
	PageSize          int
	DryRun            bool
	NoInteractive     bool
	PageAll           bool
	All               bool
	Force             bool
	ByVersion         bool
	Help              bool
}

func (p parsedArgs) workerConcurrency() int {
	if p.Concurrency == 0 {
		return 1
	}
	return p.Concurrency
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

func dispatchPending(run *model.Run) bool {
	job, err := store.LoadDispatch(filepath.Dir(filepath.Dir(run.RunDir)), run.RunID)
	return err == nil && job.Generation > job.Completed
}
func runPayload(run *model.Run) map[string]any {
	history := run.State.History
	if history == nil {
		history = []model.NodeVisit{}
	}
	activeNodes := run.State.ActiveNodes
	if activeNodes == nil && run.State.ActiveNode != "" {
		activeNodes = []string{run.State.ActiveNode}
	}
	if activeNodes == nil {
		activeNodes = []string{}
	}
	payload := map[string]any{"ok": run.State.Status != "failed", "run_id": run.RunID, "workflow": run.State.WorkflowName, "workflow_digest": run.WorkflowDigest(), "status": run.State.Status, "outcome": run.State.Outcome, "active_node": run.State.ActiveNode, "active_nodes": activeNodes, "visits": run.State.Visits, "history": history, "inputs": run.State.Inputs, "nodes": run.State.Nodes, "created_at": run.State.CreatedAt, "updated_at": run.State.UpdatedAt}
	if job, err := store.LoadDispatch(filepath.Dir(filepath.Dir(run.RunDir)), run.RunID); err == nil {
		payload["dispatch"] = job
		payload["queued"] = job.Generation > job.Completed
	}
	payload["parent_run_id"] = run.State.ParentRunID
	payload["parent_node_id"] = run.State.ParentNodeID
	payload["correlation"] = run.State.Correlation
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
	if run.State.Trigger != nil {
		payload["trigger"] = run.State.Trigger
	}
	if run.State.Evaluation != nil {
		payload["evaluation"] = run.State.Evaluation
	}
	return payload
}

func commandSchema() map[string]any {
	flag := func(name, valueType, description string, required bool) map[string]any {
		return map[string]any{"name": name, "type": valueType, "description": description, "required": required}
	}
	formatFlag := map[string]any{"name": "--format", "type": "enum", "enum": []string{"json", "table", "ndjson"}, "default": "json_when_piped_table_when_tty", "description": "Output format", "required": false}
	commands := []map[string]any{
		{"name": "resource list", "description": "Inspect workspace resource capacity, holders and orphaned claims.", "flags": []map[string]any{formatFlag}},
		{"name": "resource resolve", "description": "Release an orphan only after its complete execution scope is stopped; rejects a live recorded process.", "arguments": []map[string]any{{"name": "claim-id", "type": "string", "required": true}}, "flags": []map[string]any{flag("--confirm-stopped", "boolean", "Acknowledge all relevant processes and descendants have stopped", true), flag("--reason", "string", "Observed cleanup evidence", true), formatFlag}},
		{"name": "run enqueue", "description": "Atomically persist a graph snapshot, inputs and queue request; return without executing. Requires run worker or trigger serve.", "arguments": []map[string]any{{"name": "workflow.json", "type": "path", "required": true}}, "flags": []map[string]any{flag("--input", "key=value", "Run inputs (repeatable)", false), flag("--idempotency-key", "string", "Immutable request identity", false), flag("--parent", "run-id", "Existing parent in this workspace", false), flag("--parent-node", "node-id", "Existing node in parent snapshot", false), flag("--correlation", "key=value", "Immutable opaque string labels (repeatable)", false), formatFlag}},
		{"name": "run worker", "description": "Own and recover this workspace durable queue; concurrency counts active runs. Waiting input/resources releases admission.", "flags": []map[string]any{flag("--concurrency", "integer", "Maximum active runs; default 1", false), formatFlag}},
		{"name": "run queue", "description": "Inspect durable jobs, generations, owner heartbeat and queue counts.", "flags": []map[string]any{formatFlag}},
		{"name": "workspace init", "description": "Create a standalone layout-v1 workspace with a runnable program; never overwrite files.", "arguments": []map[string]any{{"name": "directory", "required": false, "type": "path"}}, "flags": []map[string]any{flag("--dry-run", "boolean", "Preview file creation", false), formatFlag}},
		{"name": "workspace validate", "description": "Check graph, trigger targets and executor paths; discover workspace-local agent instructions, skills and tool configuration without executing them.", "flags": []map[string]any{flag("--workflow", "path", "Graph path; default workflow.json", false), flag("--input", "key=value", "Representative run input for resolving candidate paths; repeatable", false), formatFlag}},
		{"name": "executor init", "description": "Create and register an owned Codex or Claude agent or a program in the layout-v1 directories.", "arguments": []map[string]any{{"name": "name", "required": true, "type": "string"}}, "flags": []map[string]any{flag("--kind", "codex|claude-code|command", "Scaffold kind", true), flag("--model", "string", "Optional agent model", false), flag("--dry-run", "boolean", "Preview creation", false), formatFlag}},
		{"name": "executor add", "description": "Register an existing directory or symlink without creating or modifying agent files.", "arguments": []map[string]any{{"name": "name", "required": true, "type": "string"}}, "flags": []map[string]any{flag("--kind", "codex|claude-code|command|external", "Executor kind", true), flag("--workspace", "path", "Existing working directory", false), flag("--command", "JSON-array", "Command executor argument array", false), flag("--model", "string", "Optional model", false), flag("--dry-run", "boolean", "Preview registration", false), formatFlag}},
		{"name": "workflow validate", "description": "Validate a workflow JSON definition.", "arguments": []map[string]any{{"name": "workflow.json", "required": true, "type": "path"}}, "flags": []map[string]any{flag("--check-executors", "boolean", "Check named executor registrations in this working directory", false), formatFlag}, "examples": []string{"agentflow workflow validate workflow.json --check-executors --format json"}},
		{"name": "run start", "description": "Create and execute a workflow run.", "arguments": []map[string]any{{"name": "workflow.json", "required": true, "type": "path"}}, "flags": []map[string]any{flag("--input", "key=value", "Workflow input; repeat for multiple inputs", false), flag("--idempotency-key", "string", "Stable request token used to safely retry a start", false), flag("--dry-run", "boolean", "Preview without creating or executing a run", false), formatFlag}, "examples": []string{"agentflow run start workflow.json --input issue=ABC-123 --idempotency-key 7f9c2b1e-4d9c-4f4e-8bb2-8b3d7d6d0a11"}},
		{"name": "run list", "description": "List workflow runs, newest first.", "flags": []map[string]any{flag("--workflow", "string", "Only runs of this workflow name", false), flag("--status", "comma-separated", "Only runs with these statuses", false), flag("--page-size", "integer", "Maximum number of runs to return", false), flag("--page-all", "boolean", "Return every run", false), formatFlag}},
		{"name": "run latest", "description": "Get the newest run of a workflow; exits 3 when there is none.", "arguments": []map[string]any{{"name": "workflow-name", "required": true, "type": "string"}}, "flags": []map[string]any{flag("--status", "comma-separated", "Only consider runs with these statuses", false), formatFlag}, "examples": []string{"agentflow run latest release-window --status succeeded,failed --format json"}},
		{"name": "run get", "description": "Get one run and its node states.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}}, "flags": []map[string]any{formatFlag}},
		{"name": "run logs", "description": "Read the append-only event log.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}}, "flags": []map[string]any{formatFlag}},
		{"name": "run metrics", "description": "Get one run's timing, attempts, and byte metrics.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}}, "flags": []map[string]any{formatFlag}},
		{"name": "run evaluate", "description": "Record user-defined numeric measurements for a run.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}}, "flags": []map[string]any{flag("--evaluation", "JSON", "Evaluation object with measurements, labels, and notes", true), formatFlag}},
		{"name": "run compare", "description": "Compare a run with a baseline or the previous finished run.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}, {"name": "baseline-run-id", "required": false, "type": "string"}}, "flags": []map[string]any{formatFlag}},
		{"name": "run stats", "description": "Aggregate metrics across runs, optionally filtered by workflow name and version. Run-level distributions cover finished runs and split wall-clock time into active and input waiting time.", "arguments": []map[string]any{{"name": "workflow-name", "required": false, "type": "string"}}, "flags": []map[string]any{flag("--digest", "string", "Only runs of this workflow version", false), flag("--by-version", "boolean", "Return one aggregate per workflow version, oldest first", false), formatFlag}},
		{"name": "run submit", "description": "Submit structured JSON output to a waiting node.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}, {"name": "node-id", "required": true, "type": "string"}}, "flags": []map[string]any{flag("--output", "string", "JSON output object", true), flag("--token", "string", "Expected waiting invocation leaseToken; rejects stale submissions", false), flag("--dry-run", "boolean", "Preview without changing the run", false), formatFlag}},
		{"name": "run resume", "description": "Resume a persisted run.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}}, "flags": []map[string]any{flag("--dry-run", "boolean", "Preview pending nodes without executing them", false), formatFlag}},
		{"name": "run retry", "description": "Reset failed nodes (or --from nodes) and everything downstream, keep other results, and continue the run.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}}, "flags": []map[string]any{flag("--from", "comma-separated", "Nodes to restart from; default is every failed node", false), flag("--dry-run", "boolean", "List the nodes that would be reset", false), formatFlag}},
		{"name": "run cancel", "description": "Cancel a run.", "arguments": []map[string]any{{"name": "run-id", "required": true, "type": "string"}}, "flags": []map[string]any{flag("--dry-run", "boolean", "Preview without changing the run", false), formatFlag}},
		{"name": "schema", "description": "Inspect command metadata or workflow/trigger/executor schemas.", "arguments": []map[string]any{{"name": "target", "required": false, "type": "string", "examples": []string{"--all", "workflow", "trigger", "run start"}}}, "flags": []map[string]any{flag("--all", "boolean", "Return every command", false), formatFlag}},
		{"name": "config init", "description": "Create local executor configuration.", "flags": []map[string]any{flag("--dry-run", "boolean", "Preview without writing a file", false), formatFlag}},
		{"name": "doctor", "description": "Check local setup and configured executors.", "flags": []map[string]any{formatFlag}},
		{"name": "trigger status", "description": "Inspect durable schedule baselines, missed ranges, pending plans and delivery decisions.", "flags": []map[string]any{formatFlag}},
		{"name": "trigger list", "description": "List configured cron and webhook triggers.", "flags": []map[string]any{formatFlag}},
		{"name": "trigger validate", "description": "Validate triggers.json.", "flags": []map[string]any{formatFlag}},
		{"name": "trigger fire", "description": "Emit one trigger event through the durable dispatcher.", "arguments": []map[string]any{{"name": "trigger-id", "required": true, "type": "string"}}, "flags": []map[string]any{flag("--scheduled-at", "RFC3339", "Scheduled occurrence time", false), flag("--delivery-id", "string", "Stable event id used for idempotency", false), formatFlag}},
		{"name": "trigger serve", "description": "Run the persistent cron scheduler, webhook server, and run worker.", "flags": []map[string]any{flag("--host", "string", "HTTP bind host", false), flag("--port", "integer", "HTTP port; 0 chooses an available port", false)}},
		{"name": "web", "description": "Open a local React Flow visualization for a workflow or run.", "arguments": []map[string]any{{"name": "workflow.json", "required": false, "type": "path"}}, "flags": []map[string]any{flag("--run", "string", "Open an existing run instead of a workflow file", false), flag("--host", "string", "HTTP bind host", false), flag("--port", "integer", "HTTP port; 0 chooses an available port", false)}},
		{"name": "version", "description": "Print version, commit, build time, Go version, and platform.", "flags": []map[string]any{formatFlag}},
	}
	return map[string]any{"schema_version": "1", "name": "agentflow", "description": "Run durable local workflows for Claude Code, Codex, and other agents.", "commands": commands}
}

func isRunVerb(value string) bool {
	switch value {
	case "enqueue", "worker", "queue", "start", "list", "latest", "get", "logs", "metrics", "stats", "evaluate", "compare", "submit", "resume", "retry", "cancel":
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
	case "workspace":
		printWorkspaceHelp()
	case "executor":
		printExecutorHelp()
	case "workflow":
		printWorkflowHelp()
	case "resource":
		_ = resourceCommand(nil, "")
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
	case "trigger":
		printTriggerHelp()
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
  workspace init     Create a standalone runnable workspace
  workspace validate Check graph, paths, and executor bindings
  executor init      Create an owned agent or program executor
  executor add       Register an existing executor directory
  workflow validate  Validate a workflow JSON definition
  resource list      Inspect resource capacity, holders and orphaned claims
  resource resolve   Release a verified stopped orphan
  run start          Start a workflow run
  run list           List runs
  run get            Get one run
  run logs           Read run events
  run metrics        Show timing and size metrics
  run stats          Aggregate run metrics
  run evaluate       Record user-defined evaluation measurements
  run compare        Compare a run with its previous run or a baseline
  run submit        Submit JSON output to a waiting node
  run resume        Resume a persisted run
  run cancel        Cancel a run
  schema             Inspect commands or the workflow schema
  config init        Create executor configuration
  doctor             Check local setup
  trigger status     Inspect occurrence cursors and delivery decisions
  trigger list       List configured triggers
  trigger validate   Validate trigger configuration
  trigger fire       Emit one trigger event
  trigger serve      Run cron, webhook, and queued workers
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
  agentflow workflow validate <workflow.json> [--check-executors] [--format json|table]

Validates version, executor references, edges, required fields, and bounded cycles.

Examples:
  agentflow workflow validate workflow.json --format json`)
}

func printRunHelp() {
	fmt.Println(`Usage:
  agentflow run <verb> [arguments] [flags]

Verbs:
  start <workflow.json>   Execute inline outside a managed queue; otherwise enqueue
  enqueue <workflow.json> Persist a run and return immediately
  worker --concurrency N Serve this workspace queue (one owner)
  queue                   Inspect queue counts, jobs, owner and heartbeat
  list                    List runs (--workflow, --status, --parent, --correlation)
  latest <workflow-name>  Get the newest run of a workflow (--status)
  get <run-id>            Get one run
  logs <run-id>           Read events
  metrics <run-id>        Show timing and size metrics
  stats [workflow-name]   Aggregate metrics (--digest, --by-version)
  evaluate <run-id> --evaluation JSON
                            Record numeric measurements for a run
  compare <run-id> [baseline-run-id]
                            Compare runtime and evaluation measurements
  submit <run-id> <node-id> --output JSON
  resume <run-id>         Continue pending nodes (also recovers nodes orphaned by a crash)
  retry <run-id>          Re-run failed active nodes (--from node-id)
  cancel <run-id>

Common flags:
  --format json|table|ndjson   Output format (default: JSON when piped)
  --dry-run                    Preview side effects
  --idempotency-key UUID       Reuse an immutable creation request (start/enqueue)
  --parent ID --parent-node ID Associate creation with a run/node in this workspace
  --correlation key=value      Opaque immutable creation labels / list filter
  --async                      Persist and return; opt into service admission
  --no-interactive             Never prompt

Examples:
  agentflow run start workflow.json --input issue=ABC-123 --format json
  agentflow run submit <run-id> approval --output '{"decision":"happy"}' --format json`)
}

func printSchemaHelp() {
	fmt.Println(`Usage:
  agentflow schema --all
  agentflow schema workflow
  agentflow schema trigger
  agentflow schema resources
  agentflow schema executors
  agentflow schema workspace
  agentflow schema run start

Returns machine-readable command metadata or workflow.schema.json.`)
}

func printConfigHelp() {
	fmt.Println(`Usage:
  agentflow config init [--format json|table]

Creates executors.json from the executor template.`)
}

func printDoctorHelp() {
	fmt.Println(`Usage:
  agentflow doctor [--format json|table]

Checks executor configuration and the local run store.`)
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

func printTriggerHelp() {
	fmt.Println(`Usage:
  agentflow trigger list [--format json|table]
  agentflow trigger status [--format json|table]
  agentflow trigger validate [--format json|table]
  agentflow trigger fire <trigger-id> [--scheduled-at RFC3339] [--delivery-id ID]
  agentflow trigger serve [--host 127.0.0.1] [--port 7071]

The service reads triggers.json. Cron occurrences and webhook
deliveries are persisted before a queued worker executes the target workflow.
Run one service per workflow workspace. Cron-only workspaces do not bind an
HTTP listener. Webhooks default to an available port; choose --port for a stable endpoint.
Webhook endpoints use the path configured by each trigger, for example:
  POST /triggers/github-pull-request`)
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
