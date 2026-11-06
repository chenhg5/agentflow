package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/hub"
	"github.com/chenhg5/agentflow/internal/hub/store/memory"
	"github.com/chenhg5/agentflow/internal/hubapi"
)

// hubFixture runs an in-memory Hub with two runs on two machines: one
// waiting for a decision (its machine takes input from the Hub) and one
// that failed.
func hubFixture(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{"AGENTFLOW_HUB_URL", "AGENTFLOW_HUB_TOKEN", "AGENTFLOW_HUB_VIEW_TOKEN", "AGENTFLOW_MACHINE"} {
		t.Setenv(name, "")
	}
	handler, err := hub.Handler(memory.New(), hub.Options{IngestToken: "ingest", ViewToken: "view"}, true)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	started := time.Now().Add(-time.Hour).UTC()
	at := func(d time.Duration) string { return started.Add(d).Format(time.RFC3339Nano) }
	schema := map[string]any{"type": "object", "required": []any{"decision"},
		"properties": map[string]any{"decision": map[string]any{"type": "string", "enum": []any{"approve", "hold"}}}}
	batch := hubapi.Batch{Reports: []hubapi.RunReport{
		{Protocol: 1, Source: hubapi.Source{Machine: "wsl", Version: "v1", Capabilities: []string{hubapi.CapabilitySubmit}},
			RunID: "run_wait", Workflow: "review", WorkflowDigest: "d1", Status: "waiting_input", Seq: 1, CreatedAt: at(0), StartedAt: at(0),
			Steps: []hubapi.StepReport{
				{ID: "build", Type: "process", Status: "succeeded", Attempts: 1, StartedAt: at(0), FinishedAt: at(time.Minute), StdoutTail: "built ok"},
				{ID: "decide", Type: "external", Status: "waiting_input", StartedAt: at(time.Minute)}},
			Graph: &hubapi.Graph{Entry: "build", Nodes: []hubapi.GraphNode{{ID: "build", Executors: []string{"process"}}, {ID: "decide", Executors: []string{"external"}, OutputSchema: schema}},
				Edges: []hubapi.GraphEdge{{Source: "build", Target: "decide"}, {Source: "decide", Target: "$end"}}}},
		{Protocol: 1, Source: hubapi.Source{Machine: "mac", Version: "v1"},
			RunID: "run_fail", Workflow: "review", WorkflowDigest: "d1", Status: "failed", Seq: 1, CreatedAt: at(0), StartedAt: at(0), FinishedAt: at(2 * time.Minute),
			Steps: []hubapi.StepReport{{ID: "build", Type: "process", Status: "failed", Attempts: 2, StartedAt: at(0), FinishedAt: at(2 * time.Minute),
				StderrTail: "compiling\nerror: missing module foo\n"}}},
	}}
	body, _ := json.Marshal(batch)
	request, _ := http.NewRequest(http.MethodPost, srv.URL+hubapi.IngestPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer ingest")
	response, err := http.DefaultClient.Do(request)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("ingest: %v %v", response, err)
	}
	response.Body.Close()
	t.Setenv("AGENTFLOW_HUB_URL", srv.URL)
	t.Setenv("AGENTFLOW_HUB_VIEW_TOKEN", "view")
	return srv.URL
}

// hubJSON runs an agentflow command and decodes what it printed.
func hubJSON(t *testing.T, args ...string) (map[string]any, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = writer
	done := make(chan []byte)
	go func() { data, _ := io.ReadAll(reader); done <- data }()
	runErr := Run(append(args, "--format", "json"))
	writer.Close()
	os.Stdout = previous
	data := <-done
	var out map[string]any
	if runErr == nil {
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("%v printed %q: %v", args, data, err)
		}
	}
	return out, runErr
}

func TestHubCLIReadsWhatThePagesShow(t *testing.T) {
	hubFixture(t)
	_, restore := chdirForTest(t, t.TempDir())
	defer restore()

	overview, err := hubJSON(t, "hub", "overview")
	if err != nil || overview["counts"].(map[string]any)["total"] != 2.0 || len(overview["waiting_human"].([]any)) != 1 || len(overview["recent_failures"].([]any)) != 1 {
		t.Fatalf("overview = %v, %v", overview, err)
	}
	runs, err := hubJSON(t, "hub", "runs", "--status", "failed")
	if err != nil || runs["total"] != 1.0 || runs["runs"].([]any)[0].(map[string]any)["run_id"] != "run_fail" {
		t.Fatalf("runs = %v, %v", runs, err)
	}

	// a bare run id finds its machine
	run, err := hubJSON(t, "hub", "run", "run_wait")
	if err != nil || run["machine"] != "wsl" {
		t.Fatalf("run = %v, %v", run, err)
	}
	graph := run["graph"].(map[string]any)
	if graph["entry"] != "build" || len(graph["nodes"].([]any)) != 2 || len(graph["edges"].([]any)) != 2 {
		t.Fatalf("graph = %v", graph)
	}
	inputs := run["waiting_inputs"].([]any)
	if len(inputs) != 1 || inputs[0].(map[string]any)["node_id"] != "decide" || inputs[0].(map[string]any)["hub_can_submit"] != true || inputs[0].(map[string]any)["output_schema"] == nil {
		t.Fatalf("waiting inputs = %v", inputs)
	}
	if steps := run["steps"].([]any); steps[0].(map[string]any)["stdout_tail"] != "built ok" {
		t.Fatalf("log tails are in the run: %v", steps)
	}
	if run["machine_info"].(map[string]any)["capabilities"].([]any)[0] != "submit" {
		t.Fatalf("machine = %v", run["machine_info"])
	}

	machines, err := hubJSON(t, "hub", "machines")
	if err != nil || len(machines["machines"].([]any)) != 2 {
		t.Fatalf("machines = %v, %v", machines, err)
	}
	workflows, err := hubJSON(t, "hub", "workflows")
	if err != nil || workflows["workflows"].([]any)[0].(map[string]any)["name"] != "review" {
		t.Fatalf("workflows = %v, %v", workflows, err)
	}

	steps, err := hubJSON(t, "hub", "steps", "--workflow", "review")
	if err != nil {
		t.Fatal(err)
	}
	first := steps["steps"].([]any)[0].(map[string]any)
	errs := first["errors"].([]any)
	if first["step"] != "build" || first["retried"] != 1.0 || first["failure_rate"] != 0.5 || errs[0].(map[string]any)["message"] != "error: missing module foo" || errs[0].(map[string]any)["last_run_id"] != "run_fail" || errs[0].(map[string]any)["last_stderr"] != "error: missing module foo" {
		t.Fatalf("the failing step comes first, with its error: %v", first)
	}
}

func TestHubCLISubmit(t *testing.T) {
	hubFixture(t)
	_, restore := chdirForTest(t, t.TempDir())
	defer restore()
	var commandErr *CommandError

	_, err := hubJSON(t, "hub", "submit", "wsl/run_wait", "decide", "--output", `{"decision":"ship it"}`)
	if !errors.As(err, &commandErr) || commandErr.Kind != "invalid_output" {
		t.Fatalf("output is checked against output_schema before it is sent: %v", err)
	}
	_, err = hubJSON(t, "hub", "submit", "wsl/run_wait", "build", "--output", `{}`)
	if !errors.As(err, &commandErr) || commandErr.Kind != "conflict" {
		t.Fatalf("a step that does not wait: %v", err)
	}
	result, err := hubJSON(t, "hub", "submit", "wsl/run_wait", "decide", "--output", `{"decision":"approve"}`)
	if err != nil || result["status"] != "pending" || !strings.HasPrefix(result["command_id"].(string), "cmd_") {
		t.Fatalf("submit = %v, %v", result, err)
	}
	run, _ := hubJSON(t, "hub", "run", "wsl/run_wait")
	commands := run["commands"].([]any)
	if len(commands) != 1 || commands[0].(map[string]any)["id"] != result["command_id"] || commands[0].(map[string]any)["status"] != "pending" {
		t.Fatalf("the run lists the command: %v", commands)
	}

	t.Setenv("AGENTFLOW_HUB_VIEW_TOKEN", "wrong")
	if _, err := hubJSON(t, "hub", "runs"); !errors.As(err, &commandErr) || commandErr.Kind != "hub_unauthorized" {
		t.Fatalf("a wrong view token: %v", err)
	}
}
