package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/chenhg5/agentflow/internal/schema"
	"github.com/chenhg5/agentflow/internal/telemetry"
)

// The Hub's read and manage commands. The pages are for people; these give an
// agent the same data, as JSON, to analyse runs across machines and act on
// them. Each command is one call to the Hub's read API (see docs/hub.md).

// hubClientVerbs are the hub actions that talk to a Hub as a client.
var hubClientVerbs = map[string]bool{"overview": true, "runs": true, "run": true, "workflows": true, "machines": true, "steps": true, "submit": true}

const (
	hubRequestTimeout = 30 * time.Second
	hubWaitTimeout    = 90 * time.Second
	hubMaxResponse    = 64 << 20
)

type hubClient struct {
	base  string
	token string
	http  *http.Client
}

func newHubClient(parsed parsedArgs, root string) (*hubClient, error) {
	config, err := telemetry.LoadConfig(root)
	if err != nil {
		return nil, usageError("hub config: "+err.Error(), "Fix hub.json in the workspace or ~/.agentflow/hub.json.", "hub_config")
	}
	base := config.URL
	if parsed.Hub != "" {
		base = strings.TrimRight(strings.TrimSpace(parsed.Hub), "/")
	}
	if base == "" {
		return nil, usageError("no Hub configured", "Pass --hub http://host:7075, or set AGENTFLOW_HUB_URL (and AGENTFLOW_HUB_VIEW_TOKEN), or write ~/.agentflow/hub.json.", "hub_not_configured")
	}
	token := config.ViewToken
	if token == "" {
		token = config.Token
	}
	return &hubClient{base: base, token: token, http: &http.Client{Timeout: hubRequestTimeout}}, nil
}

func (c *hubClient) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	address := c.base + path
	if len(query) > 0 {
		address += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return internalError(err)
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, address, reader)
	if err != nil {
		return usageError("bad Hub address: "+err.Error(), "Use --hub http://host:7075.", "hub_config")
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return &CommandError{Code: ExitGeneralError, Kind: "hub_unreachable", Message: "could not reach the Hub: " + err.Error(),
			Suggestion: "Check --hub / AGENTFLOW_HUB_URL and that `agentflow hub serve` is running there.", Retryable: true}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, hubMaxResponse))
	if err != nil {
		return &CommandError{Code: ExitGeneralError, Kind: "hub_unreachable", Message: "reading the Hub's answer: " + err.Error(), Retryable: true}
	}
	if response.StatusCode >= 300 {
		return hubHTTPError(response.StatusCode, data)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &CommandError{Code: ExitGeneralError, Kind: "hub_error", Message: "the Hub answered with something that is not JSON: " + err.Error(),
			Suggestion: "Check that --hub points at an agentflow Hub.", Retryable: false}
	}
	return nil
}

func hubHTTPError(status int, body []byte) error {
	var payload struct {
		Error string `json:"error"`
	}
	message := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &payload) == nil && payload.Error != "" {
		message = payload.Error
	}
	if message == "" {
		message = http.StatusText(status)
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &CommandError{Code: ExitPermission, Kind: "hub_unauthorized", Message: "the Hub refused: " + message,
			Suggestion: "Set AGENTFLOW_HUB_VIEW_TOKEN (or view_token in ~/.agentflow/hub.json) to the Hub's view token (the ingest token works only when the Hub has none)."}
	case http.StatusNotFound:
		return &CommandError{Code: ExitNotFound, Kind: "not_found", Message: message, Suggestion: "List runs with `agentflow hub runs`."}
	case http.StatusConflict:
		return &CommandError{Code: ExitConflict, Kind: "conflict", Message: message, Suggestion: "Read the run again with `agentflow hub run`."}
	case http.StatusBadRequest, http.StatusUnsupportedMediaType, http.StatusRequestEntityTooLarge:
		return &CommandError{Code: ExitUsageError, Kind: "hub_bad_request", Message: message, Suggestion: "Run `agentflow hub --help` for the flags."}
	default:
		return &CommandError{Code: ExitGeneralError, Kind: "hub_error", Message: fmt.Sprintf("the Hub answered %d: %s", status, message), Retryable: status >= 500}
	}
}

// hubClientCommand runs one client verb.
func hubClientCommand(verb string, parsed parsedArgs, root string) error {
	client, err := newHubClient(parsed, root)
	if err != nil {
		return err
	}
	ctx, cancel := signalContext()
	defer cancel()
	switch verb {
	case "overview":
		return client.show(ctx, verb, "/api/v1/overview", hubQuery("days", parsed.Days), parsed)
	case "runs":
		if err := noPositionals(verb, parsed); err != nil {
			return err
		}
		query := hubQuery("workflow", parsed.Workflow, "machine", parsed.Machine, "project", parsed.Project, "status", strings.Join(parsed.Statuses, ","),
			"since", parsed.Since, "until", parsed.Until, "limit", parsed.Limit, "offset", parsed.Offset, "run", parsed.RunID)
		return client.show(ctx, verb, "/api/v1/runs", query, parsed)
	case "workflows":
		return client.show(ctx, verb, "/api/v1/workflows", hubQuery("days", parsed.Days), parsed)
	case "machines":
		return client.show(ctx, verb, "/api/v1/machines", nil, parsed)
	case "steps":
		return client.show(ctx, verb, "/api/v1/steps", hubQuery("workflow", parsed.Workflow, "days", parsed.Days), parsed)
	case "run":
		if len(parsed.Positionals) != 1 {
			return usageError("usage: agentflow hub run <machine>/<run-id> | <run-id>", "Find runs with `agentflow hub runs`.", "hub_arguments")
		}
		machine, runID, err := client.resolveRun(ctx, parsed.Positionals[0], parsed.Machine)
		if err != nil {
			return err
		}
		return client.show(ctx, verb, runPath(machine, runID), nil, parsed)
	case "submit":
		return client.submit(ctx, parsed)
	}
	return usageError("unknown hub action: "+verb, "Use `agentflow hub --help` to list actions.", "hub_action")
}

func noPositionals(verb string, parsed parsedArgs) error {
	if len(parsed.Positionals) != 0 {
		return usageError(fmt.Sprintf("hub %s accepts no positional arguments", verb), "Run `agentflow hub --help`.", "hub_arguments")
	}
	return nil
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}

// hubQuery builds a query string from name, value pairs, leaving out empty values.
func hubQuery(pairs ...string) url.Values {
	query := url.Values{}
	for index := 0; index+1 < len(pairs); index += 2 {
		if value := strings.TrimSpace(pairs[index+1]); value != "" {
			query.Set(pairs[index], value)
		}
	}
	return query
}

func runPath(machine, runID string) string {
	return "/api/v1/runs/" + url.PathEscape(machine) + "/" + url.PathEscape(runID)
}

// resolveRun reads "<machine>/<run-id>", or finds the machine of a bare run id.
func (c *hubClient) resolveRun(ctx context.Context, ref, machine string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	if machine == "" {
		if index := strings.LastIndex(ref, "/"); index > 0 {
			machine, ref = ref[:index], ref[index+1:]
		}
	}
	if ref == "" {
		return "", "", usageError("empty run id", "Use <machine>/<run-id> or <run-id>.", "hub_arguments")
	}
	if machine != "" {
		return machine, ref, nil
	}
	var list struct {
		Runs []struct {
			Machine string `json:"machine"`
		} `json:"runs"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/runs", hubQuery("run", ref, "limit", "20"), nil, &list); err != nil {
		return "", "", err
	}
	switch len(list.Runs) {
	case 0:
		return "", "", &CommandError{Code: ExitNotFound, Kind: "not_found", Message: "no run " + ref + " on the Hub", Suggestion: "List runs with `agentflow hub runs`.", Input: ref}
	case 1:
		return list.Runs[0].Machine, ref, nil
	}
	machines := make([]string, len(list.Runs))
	for index, run := range list.Runs {
		machines[index] = run.Machine
	}
	return "", "", &CommandError{Code: ExitConflict, Kind: "ambiguous_run", Message: fmt.Sprintf("run %s exists on several machines: %s", ref, strings.Join(machines, ", ")),
		Suggestion: "Name it as <machine>/<run-id> or pass --machine."}
}

func (c *hubClient) show(ctx context.Context, verb, path string, query url.Values, parsed parsedArgs) error {
	if verb != "run" {
		if err := noPositionals(verb, parsed); err != nil {
			return err
		}
	}
	var data map[string]any
	if err := c.do(ctx, http.MethodGet, path, query, nil, &data); err != nil {
		return withFormat(err, parsed.Format)
	}
	return writeHub(verb, data, parsed.Output())
}

// submit hands input to a step that waits for it, through the Hub: the
// run's machine picks it up and applies it like `run submit`.
func (c *hubClient) submit(ctx context.Context, parsed parsedArgs) error {
	if len(parsed.Positionals) != 2 || parsed.SubmittedOutput == "" {
		return usageError("usage: agentflow hub submit <machine>/<run-id> <node-id> --output '{...}' [--wait]", "Read the waiting step and its output_schema with `agentflow hub run`.", "submit_arguments")
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(parsed.SubmittedOutput), &output); err != nil || output == nil {
		return usageError("--output must be a JSON object", "Supply an object matching the step's output_schema.", "invalid_output")
	}
	machine, runID, err := c.resolveRun(ctx, parsed.Positionals[0], parsed.Machine)
	if err != nil {
		return err
	}
	nodeID := parsed.Positionals[1]
	var detail struct {
		Inputs []struct {
			NodeID       string         `json:"node_id"`
			Accepts      bool           `json:"hub_can_submit"`
			OutputSchema map[string]any `json:"output_schema"`
		} `json:"waiting_inputs"`
	}
	if err := c.do(ctx, http.MethodGet, runPath(machine, runID), nil, nil, &detail); err != nil {
		return err
	}
	waiting := false
	for _, input := range detail.Inputs {
		if input.NodeID != nodeID {
			continue
		}
		waiting = true
		if !input.Accepts {
			return conflictError(fmt.Sprintf("machine %s does not take input from the Hub", machine),
				"Answer on that machine with `agentflow run submit`, or turn on accept_commands in its ~/.agentflow/hub.json.", parsed.Format)
		}
		if problems := schema.ValidateValue("output", output, input.OutputSchema); len(problems) > 0 {
			return usageError(schema.FormatContractErrors("output does not satisfy outputSchema", problems), "Match the step's output_schema from `agentflow hub run`.", "invalid_output")
		}
	}
	if !waiting {
		return conflictError(fmt.Sprintf("step %s of %s/%s is not waiting for input", nodeID, machine, runID), "Read the run with `agentflow hub run` to see what waits.", parsed.Format)
	}
	var answer struct {
		Command struct {
			ID string `json:"id"`
		} `json:"command"`
	}
	if err := c.do(ctx, http.MethodPost, runPath(machine, runID)+"/submit", nil, map[string]any{"node_id": nodeID, "output": output}, &answer); err != nil {
		return withFormat(err, parsed.Format)
	}
	result := map[string]any{"ok": true, "machine": machine, "run_id": runID, "node_id": nodeID, "command_id": answer.Command.ID, "status": "pending"}
	if parsed.Wait {
		command, err := c.waitCommand(ctx, machine, runID, answer.Command.ID)
		if err != nil {
			return withFormat(err, parsed.Format)
		}
		result["status"], result["command"] = command["status"], command
		if command["status"] == "rejected" {
			return &CommandError{Code: ExitConflict, Kind: "hub_command_rejected", Message: fmt.Sprintf("%s refused the input: %v", machine, command["error"]),
				Suggestion: "Read the run again with `agentflow hub run`; the step may have moved on.", Format: parsed.Format, Input: result}
		}
	}
	return writeData(result, parsed.Output())
}

// waitCommand polls the run until its machine answers the command.
func (c *hubClient) waitCommand(ctx context.Context, machine, runID, id string) (map[string]any, error) {
	deadline := time.Now().Add(hubWaitTimeout)
	for {
		var detail struct {
			Commands []map[string]any `json:"commands"`
		}
		if err := c.do(ctx, http.MethodGet, runPath(machine, runID), nil, nil, &detail); err != nil {
			return nil, err
		}
		for _, command := range detail.Commands {
			if command["id"] == id && command["status"] != "pending" {
				return command, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, &CommandError{Code: ExitGeneralError, Kind: "hub_wait_timeout", Retryable: true,
				Message:    fmt.Sprintf("%s has not picked up command %s yet", machine, id),
				Suggestion: "The machine pulls commands while a reporting agentflow command (run worker, trigger serve, run start) is running there; check later with `agentflow hub run`."}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// writeHub prints a Hub answer: JSON for agents, one JSON line per item with
// --format ndjson, a short table on a terminal.
func writeHub(verb string, data map[string]any, options OutputOptions) error {
	format := options.resolved()
	if format == "ndjson" {
		if key := hubListKey[verb]; key != "" {
			return writeNDJSON(hubItems(data[key]))
		}
	}
	if format != "table" {
		return writeData(data, OutputOptions{Format: "json"})
	}
	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer out.Flush()
	switch verb {
	case "overview":
		counts := hubMap(data["counts"])
		fmt.Fprintf(out, "last %v days\truns %v\tsucceeded %v\tfailed %v\twaiting %v\tactive %v\tsuccess %s\tmachines %v\n", data["days"], counts["total"],
			counts["succeeded"], counts["failed"], counts["waiting"], counts["active"], hubPercent(data["success_rate"]), data["machines"])
		for _, section := range []struct{ key, label string }{{"waiting_human", "waiting"}, {"recent_failures", "failed"}, {"running", "running"}} {
			for _, item := range hubItems(data[section.key]) {
				fmt.Fprintf(out, "%s\t%v/%v\t%v\t%v\n", section.label, item["machine"], item["run_id"], item["workflow"], hubText(item["error"]))
			}
		}
	case "runs":
		fmt.Fprintf(out, "RUN\tSTATUS\tWORKFLOW\tSTARTED\tDURATION\n")
		for _, run := range hubItems(data["runs"]) {
			fmt.Fprintf(out, "%v/%v\t%v\t%v\t%v\t%s\n", run["machine"], run["run_id"], run["status"], run["workflow"], hubText(run["created_at"]), hubDuration(run["duration_ms"]))
		}
		fmt.Fprintf(out, "(%v total)\n", data["total"])
	case "run":
		fmt.Fprintf(out, "%v/%v\t%v\t%v\t%s\n", data["machine"], data["run_id"], data["workflow"], data["status"], hubText(data["error"]))
		for _, step := range hubItems(data["steps"]) {
			fmt.Fprintf(out, "  %v\t%v\t%s\t%s\n", step["id"], step["status"], hubDuration(step["duration_ms"]), hubText(firstValue(step["error"], step["summary"])))
		}
		for _, input := range hubItems(data["waiting_inputs"]) {
			fmt.Fprintf(out, "waiting input\t%v\thub can submit: %v\n", input["node_id"], input["hub_can_submit"])
		}
	case "workflows":
		fmt.Fprintf(out, "WORKFLOW\tRUNS\tSUCCESS\tP50\tP95\tLAST\n")
		for _, workflow := range hubItems(data["workflows"]) {
			fmt.Fprintf(out, "%v\t%v\t%s\t%s\t%s\t%v\n", workflow["name"], hubMap(workflow["counts"])["total"], hubPercent(workflow["success_rate"]),
				hubDuration(workflow["p50_ms"]), hubDuration(workflow["p95_ms"]), workflow["last_status"])
		}
	case "machines":
		fmt.Fprintf(out, "MACHINE\tHOSTNAME\tVERSION\tLAST SEEN\tCAPABILITIES\n")
		for _, machine := range hubItems(data["machines"]) {
			fmt.Fprintf(out, "%v\t%v\t%v\t%s\t%v\n", machine["id"], machine["hostname"], machine["version"], hubText(machine["last_seen"]), machine["capabilities"])
		}
	case "steps":
		fmt.Fprintf(out, "WORKFLOW\tSTEP\tRUNS\tFAILED\tRETRIED\tP95\tTOP ERROR\n")
		for _, step := range hubItems(data["steps"]) {
			top := ""
			if errs := hubItems(step["errors"]); len(errs) > 0 {
				top = fmt.Sprintf("%v× %v", errs[0]["count"], errs[0]["message"])
				if stderr, _ := errs[0]["last_stderr"].(string); stderr != "" && stderr != errs[0]["message"] {
					top += " (stderr: " + hubText(stderr) + ")"
				}
			}
			fmt.Fprintf(out, "%v\t%v\t%v\t%v\t%v\t%s\t%s\n", step["workflow"], step["step"], step["runs"], hubMap(step["counts"])["failed"], step["retried"], hubDuration(step["p95_ms"]), top)
		}
	}
	return nil
}

var hubListKey = map[string]string{"runs": "runs", "workflows": "workflows", "machines": "machines", "steps": "steps"}

func hubItems(value any) []map[string]any {
	list, _ := value.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if entry, ok := item.(map[string]any); ok {
			out = append(out, entry)
		}
	}
	return out
}

func hubMap(value any) map[string]any {
	entry, _ := value.(map[string]any)
	return entry
}

func hubText(value any) string {
	if value == nil {
		return ""
	}
	text := strings.ReplaceAll(fmt.Sprint(value), "\n", " ")
	if runes := []rune(text); len(runes) > 80 {
		text = string(runes[:80]) + "…"
	}
	return text
}

func hubPercent(value any) string {
	if rate, ok := value.(float64); ok {
		return fmt.Sprintf("%.0f%%", rate*100)
	}
	return "—"
}

func hubDuration(value any) string {
	ms, ok := value.(float64)
	if !ok || ms <= 0 {
		return "—"
	}
	return (time.Duration(ms) * time.Millisecond).Round(time.Second / 10).String()
}

func firstValue(values ...any) any {
	for _, value := range values {
		if text, ok := value.(string); ok && text != "" {
			return text
		}
	}
	return nil
}
