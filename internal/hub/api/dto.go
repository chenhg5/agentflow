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
	ID          string        `json:"id"`
	Type        string        `json:"type,omitempty"`
	Provider    string        `json:"provider,omitempty"`
	Status      domain.Status `json:"status"`
	SkipReason  string        `json:"skip_reason,omitempty"`
	Attempts    int           `json:"attempts"`
	StartedAt   *time.Time    `json:"started_at,omitempty"`
	FinishedAt  *time.Time    `json:"finished_at,omitempty"`
	DurationMs  int64         `json:"duration_ms"`
	WaitingMs   int64         `json:"waiting_ms"`
	Error       string        `json:"error,omitempty"`
	Summary     string        `json:"summary,omitempty"`
	Reason      string        `json:"reason,omitempty"`
	OutputBytes int64         `json:"output_bytes,omitempty"`
	LogBytes    int64         `json:"log_bytes,omitempty"`
}

type machineJSON struct {
	ID        string     `json:"id"`
	Hostname  string     `json:"hostname,omitempty"`
	Version   string     `json:"version,omitempty"`
	FirstSeen *time.Time `json:"first_seen,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
}

type workflowJSON struct {
	Name        string          `json:"name"`
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
		Version: run.Version, Orchestration: run.Orchestration, Workflow: run.Workflow, Digest: run.Digest, Status: run.Status,
		Seq: run.Seq, CreatedAt: timePtr(run.CreatedAt), StartedAt: timePtr(run.StartedAt), FinishedAt: timePtr(run.FinishedAt),
		UpdatedAt: timePtr(run.UpdatedAt), ReceivedAt: timePtr(run.ReceivedAt), DurationMs: run.DurationMs, WaitingMs: run.WaitingMs,
		TriggerKind: run.TriggerKind, TriggerID: run.TriggerID, Inputs: run.Inputs, Error: run.Error}
	for _, step := range run.Steps {
		out.Steps = append(out.Steps, stepJSON{ID: step.ID, Type: step.Type, Provider: step.Provider, Status: step.Status,
			SkipReason: step.SkipReason, Attempts: step.Attempts, StartedAt: timePtr(step.StartedAt), FinishedAt: timePtr(step.FinishedAt),
			DurationMs: step.DurationMs, WaitingMs: step.WaitingMs, Error: step.Error, Summary: step.Summary, Reason: step.Reason,
			OutputBytes: step.OutputBytes, LogBytes: step.LogBytes})
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
	return workflowJSON{Name: stat.Name, Counts: stat.Counts, SuccessRate: rate(stat.Counts), P50Ms: stat.P50Ms, P95Ms: stat.P95Ms,
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
		out[index] = machineJSON{ID: machine.ID, Hostname: machine.Hostname, Version: machine.Version,
			FirstSeen: timePtr(machine.FirstSeen), LastSeen: timePtr(machine.LastSeen)}
	}
	return out
}
