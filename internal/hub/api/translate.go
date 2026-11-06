package api

import (
	"fmt"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/domain"
	"github.com/chenhg5/agentflow/internal/hubapi"
)

// runFromReport translates the wire protocol into the domain model. It is the
// only place that knows both, so the protocol can evolve without touching the
// domain or the use cases.
func runFromReport(report hubapi.RunReport) (domain.Run, error) {
	if report.Protocol != hubapi.ProtocolVersion {
		return domain.Run{}, fmt.Errorf("unsupported protocol %d (this hub speaks %d)", report.Protocol, hubapi.ProtocolVersion)
	}
	orchestration := report.Orchestration
	if orchestration == "" {
		orchestration = hubapi.OrchestrationJSON
	}
	run := domain.Run{
		Key:           domain.RunKey{Machine: report.Source.Machine, RunID: report.RunID},
		Hostname:      report.Source.Hostname,
		Project:       report.Source.Project,
		Path:          report.Source.Path,
		Version:       report.Source.Version,
		Orchestration: orchestration,
		Workflow:      report.Workflow,
		Digest:        report.WorkflowDigest,
		Status:        domain.Status(report.Status),
		Seq:           report.Seq,
		CreatedAt:     parseTime(report.CreatedAt),
		StartedAt:     parseTime(report.StartedAt),
		FinishedAt:    parseTime(report.FinishedAt),
		UpdatedAt:     parseTime(report.UpdatedAt),
		DurationMs:    report.DurationMs,
		WaitingMs:     report.WaitingMs,
		Inputs:        report.Inputs,
		Error:         report.Error,
		Steps:         make([]domain.Step, 0, len(report.Steps)),
	}
	if report.Graph != nil {
		graph := domain.Graph{Entry: report.Graph.Entry}
		for _, node := range report.Graph.Nodes {
			graph.Nodes = append(graph.Nodes, domain.GraphNode{ID: node.ID, Executors: node.Executors, Parallel: node.Parallel, Join: node.Join})
		}
		for _, edge := range report.Graph.Edges {
			graph.Edges = append(graph.Edges, domain.GraphEdge{Source: edge.Source, Target: edge.Target, Condition: edge.Condition,
				MaxIterations: edge.MaxIterations, Parallel: edge.Parallel})
		}
		run.Graph = &graph
	}
	if report.Trigger != nil {
		run.TriggerKind, run.TriggerID = report.Trigger.Kind, report.Trigger.ID
	}
	for _, step := range report.Steps {
		run.Steps = append(run.Steps, domain.Step{
			ID: step.ID, Type: step.Type, Provider: step.Provider, Status: domain.Status(step.Status), SkipReason: step.SkipReason,
			Attempts: step.Attempts, StartedAt: parseTime(step.StartedAt), FinishedAt: parseTime(step.FinishedAt),
			DurationMs: step.DurationMs, WaitingMs: step.WaitingMs, Error: step.Error, Summary: step.Summary, Reason: step.Reason,
			OutputBytes: step.OutputBytes, LogBytes: step.LogBytes,
		})
	}
	return run, nil
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}
