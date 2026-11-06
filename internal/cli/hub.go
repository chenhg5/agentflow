package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chenhg5/agentflow/internal/hub"
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
	if len(parsed.Positionals) != 0 {
		return usageError(fmt.Sprintf("hub %s accepts no positional arguments", verb), "Run `agentflow hub --help`.", "hub_arguments")
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
	payload := map[string]any{"ok": true, "enabled": config.Enabled(), "hub": config.URL, "token_set": config.Token != "",
		"machine": source.Machine, "project": source.Project, "pending": len(pending), "last_flush": last}
	return writeData(payload, parsed.Output())
}

func printHubHelp() {
	fmt.Println(`Usage:
  agentflow hub serve [--host 127.0.0.1] [--port 7075] [--db sqlite:<path>|memory:] [--retain 90d]
  agentflow hub push [--all] [--format json|table]
  agentflow hub status [--format json|table]

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

Examples:
  AGENTFLOW_HUB_TOKEN=$(openssl rand -hex 24) agentflow hub serve --host 0.0.0.0
  AGENTFLOW_HUB_URL=http://hub.local:7075 AGENTFLOW_HUB_TOKEN=... agentflow hub push --all`)
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
