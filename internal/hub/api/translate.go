package api

import (
	"fmt"
	"strings"
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
		Capabilities:  report.Source.Capabilities,
		Orchestration: orchestration,
		Workflow:      report.Workflow,
		Digest:        report.WorkflowDigest,
		Title:         clip(report.WorkflowTitle, 120),
		Description:   clip(report.WorkflowDescription, 2000),
		Tags:          tagsOf(report.Tags),
		Group:         clip(report.Group, 80),
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
			graph.Nodes = append(graph.Nodes, domain.GraphNode{ID: node.ID, Executors: node.Executors, Parallel: node.Parallel, Join: node.Join, OutputSchema: node.OutputSchema})
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
			StdoutTail: step.StdoutTail, StderrTail: step.StderrTail, Result: step.Result, Artifacts: artifacts(step.Artifacts),
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

func artifacts(in []hubapi.Artifact) []domain.Artifact {
	var out []domain.Artifact
	for _, item := range in {
		out = append(out, domain.Artifact{Name: item.Name, Bytes: item.Bytes})
	}
	return out
}

// clip bounds a label from a report; runners clip too, but the Hub does not
// rely on every client doing so.
func clip(text string, limit int) string {
	text = strings.TrimSpace(text)
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit])
	}
	return text
}

// tagsOf keeps at most 20 distinct, non-empty tags of at most 40 characters.
func tagsOf(tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tag := range tags {
		if tag = clip(tag, 40); tag != "" && !seen[tag] && len(out) < 20 {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}
