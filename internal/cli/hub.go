package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chenhg5/agentflow/internal/engine"
	"github.com/chenhg5/agentflow/internal/hub"
	"github.com/chenhg5/agentflow/internal/hubapi"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/schema"
	"github.com/chenhg5/agentflow/internal/store"
	"github.com/chenhg5/agentflow/internal/telemetry"
	"github.com/chenhg5/agentflow/internal/version"
)

// reportsRuns says whether a command can change run state, and so should
// report to the Hub while it works. Read-only commands never touch the network.
func reportsRuns(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[0] {
	case "run":
		switch args[1] {
		case "start", "enqueue", "worker", "submit", "resume", "retry", "cancel", "evaluate":
			return true
		}
	case "trigger":
		return args[1] == "fire" || args[1] == "serve"
	case "code":
		return true
	}
	return false
}

func hubCommand(args []string, root string) error {
	if len(args) == 0 || isHelp(args[0]) {
		printHubHelp()
		return nil
	}
	verb := args[0]
	parsed, err := parseArgs(args[1:])
	if err != nil {
		return withFormat(err, parsed.Format)
	}
	if parsed.Help {
		printHubHelp()
		return nil
	}
	if hubClientVerbs[verb] {
		return withFormat(hubClientCommand(verb, parsed, root), parsed.Format)
	}
	if err := noPositionals(verb, parsed); err != nil {
		return err
	}
	switch verb {
	case "serve":
		return hubServe(parsed)
	case "push":
		return hubPush(parsed, root)
	case "status":
		return hubStatus(parsed, root)
	default:
		return usageError(fmt.Sprintf("unknown hub action: %s", verb), "Use `agentflow hub --help` to list actions.", "hub_action")
	}
}

func hubServe(parsed parsedArgs) error {
	dsn := parsed.DB
	if dsn == "" {
		dsn = os.Getenv("AGENTFLOW_HUB_DB")
	}
	if dsn == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return internalError(err)
		}
		dsn = "sqlite:" + filepath.Join(home, ".agentflow", "hub", "hub.db")
	}
	retainText := parsed.Retain
	if retainText == "" {
		retainText = os.Getenv("AGENTFLOW_HUB_RETAIN")
	}
	retain, err := parseRetention(retainText)
	if err != nil {
		return usageError(err.Error(), "Use --retain 90d, --retain 720h, or --retain 0 to keep runs forever.", "invalid_retain")
	}
	port := 7075
	if parsed.PortSet {
		port = parsed.Port
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	server, err := hub.New(ctx, hub.Options{
		Host: parsed.Host, Port: port, Store: dsn, Version: version.Version,
		IngestToken: os.Getenv("AGENTFLOW_HUB_TOKEN"), ViewToken: os.Getenv("AGENTFLOW_HUB_VIEW_TOKEN"), Retain: retain,
	})
	if err != nil {
		return usageError(err.Error(), "Run `agentflow hub --help` for the options.", "hub_serve")
	}
	kept := "forever"
	if retain > 0 {
		kept = fmt.Sprintf("%dd", int(retain.Hours()/24))
	}
	fmt.Fprintf(os.Stderr, "agentflow hub listening on %s (store %s, finished runs kept %s)\n", server.URL(), dsn, kept)
	if err := server.Serve(ctx); err != nil {
		return internalError(err)
	}
	return nil
}

func hubReporter(root string) (*telemetry.Reporter, func(), telemetry.Config, error) {
	config, err := telemetry.LoadConfig(root)
	if err != nil {
		return nil, nil, config, usageError("hub config: "+err.Error(), "Fix hub.json in the workspace or ~/.agentflow/hub.json.", "hub_config")
	}
	if !config.Enabled() {
		return nil, nil, config, usageError("no Hub configured", "Set AGENTFLOW_HUB_URL (and AGENTFLOW_HUB_TOKEN), or write hub.json in the workspace with {\"url\": ..., \"token\": ...}.", "hub_not_configured")
	}
	reporter, remove, err := telemetry.Install(root)
	if err != nil {
		return nil, nil, config, internalError(err)
	}
	return reporter, remove, config, nil
}

func hubPush(parsed parsedArgs, root string) error {
	reporter, remove, config, err := hubReporter(root)
	if err != nil {
		return withFormat(err, parsed.Format)
	}
	defer remove()
	recorded := 0
	if parsed.All {
		runs, err := loadRuns(root)
		if err != nil && !os.IsNotExist(err) {
			return internalError(err)
		}
		for _, run := range runs {
			if err := reporter.Record(run); err != nil {
				return internalError(err)
			}
			recorded++
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := reporter.Flush(ctx)
	payload := map[string]any{"ok": err == nil, "hub": config.URL, "recorded": recorded, "result": result}
	if err != nil {
		return &CommandError{Code: ExitGeneralError, Kind: "hub_unreachable", Message: "could not deliver reports: " + err.Error(),
			Suggestion: fmt.Sprintf("%d reports stay in the outbox; check AGENTFLOW_HUB_URL / AGENTFLOW_HUB_TOKEN and retry `agentflow hub push`.", result.Pending),
			Retryable:  true, Format: parsed.Format, Input: payload}
	}
	return writeData(payload, parsed.Output())
}

func hubStatus(parsed parsedArgs, root string) error {
	config, err := telemetry.LoadConfig(root)
	if err != nil {
		return usageError("hub config: "+err.Error(), "Fix hub.json in the workspace or ~/.agentflow/hub.json.", "hub_config")
	}
	outbox := telemetry.NewOutbox(root)
	pending, err := outbox.Pending()
	if err != nil && !os.IsNotExist(err) {
		return internalError(err)
	}
	last, err := telemetry.LastStatus(outbox)
	if err != nil {
		return internalError(err)
	}
	source := telemetry.SourceFor(config, root)
	payload := map[string]any{"ok": true, "enabled": config.Enabled(), "hub": config.URL, "token_set": config.Token != "", "view_token_set": config.ViewToken != "",
		"accept_commands": config.AcceptCommands,
		"machine":         source.Machine, "project": source.Project, "pending": len(pending), "last_flush": last}
	return writeData(payload, parsed.Output())
}

func printHubHelp() {
	fmt.Println(`Usage:
  agentflow hub serve [--host 127.0.0.1] [--port 7075] [--db sqlite:<path>|memory:] [--retain 90d]
  agentflow hub push [--all] [--format json|table]
  agentflow hub status [--format json|table]

  agentflow hub overview [--days 14]
  agentflow hub runs [--workflow W] [--machine M] [--project P] [--status failed,...]
                     [--since 2026-11-01] [--until ...] [--run ID] [--limit 50] [--offset 0]
  agentflow hub run <machine>/<run-id> | <run-id>
  agentflow hub workflows [--days 30]
  agentflow hub machines
  agentflow hub steps [--workflow W] [--days 30]
  agentflow hub submit <machine>/<run-id> <node-id> --output '{...}' [--wait]

The Hub collects runs from every machine into one web console and read API.

Runner side: set AGENTFLOW_HUB_URL and AGENTFLOW_HUB_TOKEN (or hub.json in the
workflow workspace, or ~/.agentflow/hub.json, with {"url": ..., "token": ...,
"machine": ...}). Commands that change runs (run start / enqueue / worker /
submit / resume / retry / cancel / evaluate, trigger fire / serve) then record
each state change in <workspace>/hub-outbox and deliver it in the background.
A Hub that is down never fails a run: reports wait in the outbox. Use
"hub push --all" once to backfill runs from before the Hub was configured.

Hub side: "hub serve" stores runs in SQLite (default ~/.agentflow/hub/hub.db) and
removes finished runs older than --retain (default 90d, AGENTFLOW_HUB_RETAIN;
0 keeps them forever). Runs still going or waiting for input are always kept.
AGENTFLOW_HUB_TOKEN authenticates runners and is required beyond loopback; there
the pages ask for it as the password (any user name), or for
AGENTFLOW_HUB_VIEW_TOKEN when that is set.

Reading the Hub: overview, runs, run, workflows, machines and steps print the
Hub's data as JSON (a table on a terminal; --format ndjson gives one line per
run, workflow, machine or step). "run" has everything the run page shows: step
timings, errors, summaries, log tails, results, artifacts, the graph, steps
waiting for input with their output_schema, and the commands sent to the run.
"steps" ranks the steps of each workflow by failures and time spent, with the
most common errors. They use --hub or AGENTFLOW_HUB_URL, and the view token
(AGENTFLOW_HUB_VIEW_TOKEN, or "view_token" in ~/.agentflow/hub.json; the
ingest token works when the Hub has no separate view token). "submit" answers a waiting step; the run's machine
applies it when it accepts commands (accept_commands in its own config).

Examples:
  AGENTFLOW_HUB_TOKEN=$(openssl rand -hex 24) agentflow hub serve --host 0.0.0.0
  AGENTFLOW_HUB_URL=http://hub.local:7075 AGENTFLOW_HUB_TOKEN=... agentflow hub push --all
  agentflow hub runs --status failed --since 2026-11-01 --format ndjson
  agentflow hub steps --workflow pr-pipeline --days 7
  agentflow hub run wsl/run_20261106_ab12 | jq '.steps[] | select(.status=="failed")'`)
}

// hubApplier carries out commands from the Hub against the runs in root. Only
// "submit" exists: the same checks as `run submit`, bound to the wait the
// person saw on the Hub, then handed to the run's executor as a control.
func hubApplier(root string) telemetry.Applier {
	return func(ctx context.Context, command hubapi.Command) error {
		if command.Kind != hubapi.CommandKindSubmit {
			return fmt.Errorf("unsupported command %q", command.Kind)
		}
		run, err := store.LoadRun(root, command.RunID)
		if err != nil {
			return fmt.Errorf("run %s is not in this workspace", command.RunID)
		}
		switch run.State.Status {
		case "cancelled", "succeeded", "failed":
			return fmt.Errorf("run %s already %s", command.RunID, run.State.Status)
		}
		node, ok := run.State.Nodes[command.NodeID]
		if !ok {
			return fmt.Errorf("node %s not found", command.NodeID)
		}
		if node.Status != "waiting_input" {
			return fmt.Errorf("node %s is %s, not waiting for input", command.NodeID, node.Status)
		}
		if command.StepStartedAt != "" && !sameInstant(command.StepStartedAt, node.StartedAt) {
			return fmt.Errorf("node %s was entered again since this input was submitted", command.NodeID)
		}
		for _, definition := range run.Workflow.Nodes {
			if definition.ID == command.NodeID {
				if problems := schema.ValidateValue("output", command.Output, definition.OutputSchema); len(problems) > 0 {
					return errors.New(schema.FormatContractErrors("output does not satisfy outputSchema", problems))
				}
			}
		}
		control := model.Control{Kind: engine.ControlSubmit, NodeID: command.NodeID, Token: node.LeaseToken, Output: command.Output}
		if err := store.EnqueueControl(run.RunDir, control); err != nil {
			return err
		}
		_, err = store.WakeRun(ctx, root, run.RunID, control.Kind)
		return err
	}
}

// sameInstant compares two timestamps to the millisecond: the Hub stores
// times in milliseconds, the run state in nanoseconds.
func sameInstant(a, b string) bool {
	first, errA := time.Parse(time.RFC3339Nano, a)
	second, errB := time.Parse(time.RFC3339Nano, b)
	if errA != nil || errB != nil {
		return a == b
	}
	return first.UnixMilli() == second.UnixMilli()
}

// defaultRetention keeps finished runs for 90 days.
const defaultRetention = 90 * 24 * time.Hour

// parseRetention reads "90d", "720h", "45m" or "0"; empty means the default.
func parseRetention(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	switch {
	case value == "":
		return defaultRetention, nil
	case value == "0":
		return 0, nil
	case strings.HasSuffix(value, "d"):
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err != nil || days < 0 {
			return 0, fmt.Errorf("--retain %q: days must be a whole number", value)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 {
		return 0, fmt.Errorf("--retain %q is not a duration", value)
	}
	return duration, nil
}
