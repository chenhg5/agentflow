package api

import (
	"time"

	"github.com/chenhg5/agentflow/internal/hub/app"
	"github.com/chenhg5/agentflow/internal/hub/domain"
)

// The read API's JSON shapes. They are separate from the domain types so the
// domain can change without breaking API clients.

type runJSON struct {
	Machine       string            `json:"machine"`
	RunID         string            `json:"run_id"`
	Hostname      string            `json:"hostname,omitempty"`
	Project       string            `json:"project,omitempty"`
	Path          string            `json:"path,omitempty"`
	Version       string            `json:"version,omitempty"`
	Orchestration string            `json:"orchestration"`
	Workflow      string            `json:"workflow"`
	Title         string            `json:"title"`
	Description   string            `json:"description,omitempty"`
	Tags          []string          `json:"tags"`
	Group         string            `json:"group,omitempty"`
	Digest        string            `json:"workflow_digest,omitempty"`
	Status        domain.Status     `json:"status"`
	Seq           int64             `json:"seq"`
	CreatedAt     *time.Time        `json:"created_at,omitempty"`
	StartedAt     *time.Time        `json:"started_at,omitempty"`
	FinishedAt    *time.Time        `json:"finished_at,omitempty"`
	UpdatedAt     *time.Time        `json:"updated_at,omitempty"`
	ReceivedAt    *time.Time        `json:"received_at,omitempty"`
	DurationMs    int64             `json:"duration_ms"`
	WaitingMs     int64             `json:"waiting_ms"`
	TriggerKind   string            `json:"trigger_kind,omitempty"`
	TriggerID     string            `json:"trigger_id,omitempty"`
	Inputs        map[string]string `json:"inputs,omitempty"`
	Error         string            `json:"error,omitempty"`
	Steps         []stepJSON        `json:"steps,omitempty"`
}

type stepJSON struct {
	ID          string         `json:"id"`
	Type        string         `json:"type,omitempty"`
	Provider    string         `json:"provider,omitempty"`
	Status      domain.Status  `json:"status"`
	SkipReason  string         `json:"skip_reason,omitempty"`
	Attempts    int            `json:"attempts"`
	StartedAt   *time.Time     `json:"started_at,omitempty"`
	FinishedAt  *time.Time     `json:"finished_at,omitempty"`
	DurationMs  int64          `json:"duration_ms"`
	WaitingMs   int64          `json:"waiting_ms"`
	Error       string         `json:"error,omitempty"`
	Summary     string         `json:"summary,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	OutputBytes int64          `json:"output_bytes,omitempty"`
	LogBytes    int64          `json:"log_bytes,omitempty"`
	StdoutTail  string         `json:"stdout_tail,omitempty"`
	StderrTail  string         `json:"stderr_tail,omitempty"`
	Result      string         `json:"result,omitempty"`
	Artifacts   []artifactJSON `json:"artifacts,omitempty"`
}

type artifactJSON struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type machineJSON struct {
	ID           string     `json:"id"`
	Hostname     string     `json:"hostname,omitempty"`
	Version      string     `json:"version,omitempty"`
	Capabilities []string   `json:"capabilities"`
	FirstSeen    *time.Time `json:"first_seen,omitempty"`
	LastSeen     *time.Time `json:"last_seen,omitempty"`
}

type workflowJSON struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	Tags        []string        `json:"tags"`
	Group       string          `json:"group,omitempty"`
	Counts      app.Counts      `json:"counts"`
	SuccessRate *float64        `json:"success_rate"`
	P50Ms       int64           `json:"p50_ms"`
	P95Ms       int64           `json:"p95_ms"`
	LastStatus  domain.Status   `json:"last_status"`
	LastAt      *time.Time      `json:"last_at,omitempty"`
	Machines    []string        `json:"machines"`
	Recent      []domain.Status `json:"recent"`
}

type dayJSON struct {
	Date   string     `json:"date"`
	Counts app.Counts `json:"counts"`
}

type overviewJSON struct {
	Days        int            `json:"days"`
	Counts      app.Counts     `json:"counts"`
	SuccessRate *float64       `json:"success_rate"`
	AvgMs       int64          `json:"avg_ms"`
	Machines    int            `json:"machines"`
	Daily       []dayJSON      `json:"daily"`
	Failures    []runJSON      `json:"recent_failures"`
	Waiting     []runJSON      `json:"waiting_human"`
	Running     []runJSON      `json:"running"`
	Workflows   []workflowJSON `json:"workflows"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func rate(counts app.Counts) *float64 {
	value := counts.SuccessRate()
	if value < 0 {
		return nil
	}
	return &value
}

func toRunJSON(run domain.Run) runJSON {
	out := runJSON{Machine: run.Key.Machine, RunID: run.Key.RunID, Hostname: run.Hostname, Project: run.Project, Path: run.Path,
		Version: run.Version, Orchestration: run.Orchestration, Workflow: run.Workflow, Title: run.Label(), Description: run.Description,
		Tags: tagList(run.Tags), Group: run.Group, Digest: run.Digest, Status: run.Status,
		Seq: run.Seq, CreatedAt: timePtr(run.CreatedAt), StartedAt: timePtr(run.StartedAt), FinishedAt: timePtr(run.FinishedAt),
		UpdatedAt: timePtr(run.UpdatedAt), ReceivedAt: timePtr(run.ReceivedAt), DurationMs: run.DurationMs, WaitingMs: run.WaitingMs,
		TriggerKind: run.TriggerKind, TriggerID: run.TriggerID, Inputs: run.Inputs, Error: run.Error}
	for _, step := range run.Steps {
		out.Steps = append(out.Steps, stepJSON{ID: step.ID, Type: step.Type, Provider: step.Provider, Status: step.Status,
			SkipReason: step.SkipReason, Attempts: step.Attempts, StartedAt: timePtr(step.StartedAt), FinishedAt: timePtr(step.FinishedAt),
			DurationMs: step.DurationMs, WaitingMs: step.WaitingMs, Error: step.Error, Summary: step.Summary, Reason: step.Reason,
			OutputBytes: step.OutputBytes, LogBytes: step.LogBytes, StdoutTail: step.StdoutTail, StderrTail: step.StderrTail, Result: step.Result,
			Artifacts: artifactsJSON(step.Artifacts)})
	}
	return out
}

func toRunsJSON(runs []domain.Run) []runJSON {
	out := make([]runJSON, len(runs))
	for index, run := range runs {
		out[index] = toRunJSON(run)
	}
	return out
}

func toWorkflowJSON(stat app.WorkflowStat) workflowJSON {
	return workflowJSON{Name: stat.Name, Title: stat.Label(), Description: stat.Description, Tags: tagList(stat.Tags), Group: stat.Group, Counts: stat.Counts, SuccessRate: rate(stat.Counts), P50Ms: stat.P50Ms, P95Ms: stat.P95Ms,
		LastStatus: stat.LastStatus, LastAt: timePtr(stat.LastAt), Machines: stat.Machines, Recent: stat.Recent}
}

func toWorkflowsJSON(stats []app.WorkflowStat) []workflowJSON {
	out := make([]workflowJSON, len(stats))
	for index, stat := range stats {
		out[index] = toWorkflowJSON(stat)
	}
	return out
}

func toOverviewJSON(overview app.Overview) overviewJSON {
	out := overviewJSON{Days: overview.Days, Counts: overview.Counts, SuccessRate: rate(overview.Counts), AvgMs: overview.AvgMs,
		Machines: overview.Machines, Failures: toRunsJSON(overview.Failures), Waiting: toRunsJSON(overview.Waiting),
		Running: toRunsJSON(overview.Running), Workflows: toWorkflowsJSON(overview.Workflows)}
	for _, day := range overview.Daily {
		out.Daily = append(out.Daily, dayJSON{Date: day.Date.Format(time.DateOnly), Counts: day.Counts})
	}
	return out
}

func toMachinesJSON(machines []domain.Machine) []machineJSON {
	out := make([]machineJSON, len(machines))
	for index, machine := range machines {
		out[index] = toMachineJSON(machine)
	}
	return out
}

func toMachineJSON(machine domain.Machine) machineJSON {
	capabilities := machine.Capabilities
	if capabilities == nil {
		capabilities = []string{}
	}
	return machineJSON{ID: machine.ID, Hostname: machine.Hostname, Version: machine.Version, Capabilities: capabilities,
		FirstSeen: timePtr(machine.FirstSeen), LastSeen: timePtr(machine.LastSeen)}
}

func artifactsJSON(in []domain.Artifact) []artifactJSON {
	var out []artifactJSON
	for _, item := range in {
		out = append(out, artifactJSON(item))
	}
	return out
}

// runDetailJSON is GET /api/v1/runs/{machine}/{run}: the run with its steps
// and everything else its page shows.
type runDetailJSON struct {
	runJSON
	Graph    *graphJSON    `json:"graph,omitempty"`
	Machine  machineJSON   `json:"machine_info"`
	Inputs   []inputJSON   `json:"waiting_inputs"`
	Commands []commandJSON `json:"commands"`
}

type graphJSON struct {
	Entry string          `json:"entry"`
	Nodes []graphNodeJSON `json:"nodes"`
	Edges []graphEdgeJSON `json:"edges"`
}

type graphNodeJSON struct {
	ID           string         `json:"id"`
	Executors    []string       `json:"executors,omitempty"`
	Parallel     bool           `json:"parallel,omitempty"`
	Join         []string       `json:"join,omitempty"`
	OutputSchema map[string]any `json:"output_schema,omitempty"`
}

type graphEdgeJSON struct {
	Source        string `json:"from"`
	Target        string `json:"to"`
	Condition     string `json:"condition,omitempty"`
	MaxIterations int    `json:"max_iterations,omitempty"`
	Parallel      bool   `json:"parallel,omitempty"`
}

type inputJSON struct {
	NodeID       string         `json:"node_id"`
	Executor     string         `json:"executor,omitempty"`
	Since        *time.Time     `json:"since,omitempty"`
	Accepts      bool           `json:"hub_can_submit"`
	OutputSchema map[string]any `json:"output_schema,omitempty"`
}

type commandJSON struct {
	ID            string               `json:"id"`
	Kind          string               `json:"kind"`
	NodeID        string               `json:"node_id"`
	Output        map[string]any       `json:"output,omitempty"`
	By            string               `json:"by,omitempty"`
	Status        domain.CommandStatus `json:"status"`
	Error         string               `json:"error,omitempty"`
	StepStartedAt *time.Time           `json:"step_started_at,omitempty"`
	CreatedAt     *time.Time           `json:"created_at,omitempty"`
	UpdatedAt     *time.Time           `json:"updated_at,omitempty"`
}

type stepStatJSON struct {
	Workflow    string           `json:"workflow"`
	Step        string           `json:"step"`
	Type        string           `json:"type,omitempty"`
	Provider    string           `json:"provider,omitempty"`
	Runs        int              `json:"runs"`
	Counts      app.StepCounts   `json:"counts"`
	FailureRate *float64         `json:"failure_rate"`
	Retried     int              `json:"retried"`
	P50Ms       int64            `json:"p50_ms"`
	P95Ms       int64            `json:"p95_ms"`
	WaitP50Ms   int64            `json:"wait_p50_ms"`
	LastAt      *time.Time       `json:"last_at,omitempty"`
	Errors      []errorGroupJSON `json:"errors,omitempty"`
}

type errorGroupJSON struct {
	Message     string     `json:"message"`
	Count       int        `json:"count"`
	LastMachine string     `json:"last_machine"`
	LastRunID   string     `json:"last_run_id"`
	LastAt      *time.Time `json:"last_at,omitempty"`
	LastStderr  string     `json:"last_stderr,omitempty"`
}

func toRunDetailJSON(detail app.RunDetail) runDetailJSON {
	out := runDetailJSON{runJSON: toRunJSON(detail.Run), Machine: toMachineJSON(detail.Machine),
		Inputs: []inputJSON{}, Commands: []commandJSON{}}
	if graph := detail.Run.Graph; graph != nil {
		out.Graph = &graphJSON{Entry: graph.Entry, Nodes: []graphNodeJSON{}, Edges: []graphEdgeJSON{}}
		for _, node := range graph.Nodes {
			out.Graph.Nodes = append(out.Graph.Nodes, graphNodeJSON(node))
		}
		for _, edge := range graph.Edges {
			out.Graph.Edges = append(out.Graph.Edges, graphEdgeJSON(edge))
		}
	}
	for _, input := range detail.Inputs {
		out.Inputs = append(out.Inputs, inputJSON{NodeID: input.NodeID, Executor: input.Executor, Since: timePtr(input.Since),
			Accepts: input.Accepts, OutputSchema: input.OutputSchema})
	}
	for _, command := range detail.Commands {
		out.Commands = append(out.Commands, toCommandJSON(command))
	}
	return out
}

func toCommandJSON(command domain.Command) commandJSON {
	return commandJSON{ID: command.ID, Kind: command.Kind, NodeID: command.NodeID, Output: command.Output, By: command.By,
		Status: command.Status, Error: command.Error, StepStartedAt: timePtr(command.StepStartedAt),
		CreatedAt: timePtr(command.CreatedAt), UpdatedAt: timePtr(command.UpdatedAt)}
}

func toStepStatsJSON(stats []app.StepStat) []stepStatJSON {
	out := make([]stepStatJSON, len(stats))
	for index, stat := range stats {
		row := stepStatJSON{Workflow: stat.Workflow, Step: stat.Step, Type: stat.Type, Provider: stat.Provider, Runs: stat.Runs,
			Counts: stat.Counts, Retried: stat.Retried, P50Ms: stat.P50Ms, P95Ms: stat.P95Ms, WaitP50Ms: stat.WaitP50Ms, LastAt: timePtr(stat.LastAt)}
		if value := stat.Counts.FailureRate(); value >= 0 {
			row.FailureRate = &value
		}
		for _, group := range stat.Errors {
			row.Errors = append(row.Errors, errorGroupJSON{Message: group.Message, Count: group.Count,
				LastMachine: group.Last.Machine, LastRunID: group.Last.RunID, LastAt: timePtr(group.LastAt), LastStderr: group.Stderr})
		}
		out[index] = row
	}
	return out
}

func tagList(tags []string) []string {
	if tags == nil {
		return []string{}
	}
	return tags
}
