package telemetry

import (
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/chenhg5/agentflow/internal/hubapi"
	"github.com/chenhg5/agentflow/internal/metrics"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/version"
)

const (
	maxErrorRunes   = 2000
	maxSummaryRunes = 500
	maxInputRunes   = 300
	maxInputs       = 30
)

// SourceFor describes this machine and the project whose .agentflow root is root.
func SourceFor(config Config, root string) hubapi.Source {
	hostname, _ := os.Hostname()
	machine := config.Machine
	if machine == "" {
		machine = hostname
	}
	if machine == "" {
		machine = "unknown"
	}
	project, err := filepath.Abs(root)
	if err != nil {
		project = root
	}
	return hubapi.Source{Machine: machine, Hostname: hostname, Project: filepath.Base(project), Path: project, Version: version.Version}
}

// FromRun adapts a run of the JSON engine to the reporting contract.
func FromRun(run *model.Run, source hubapi.Source, asOf time.Time) hubapi.RunReport {
	snapshot := metrics.Snapshot(run, asOf)
	report := hubapi.RunReport{
		Protocol:       hubapi.ProtocolVersion,
		Source:         source,
		Orchestration:  hubapi.OrchestrationJSON,
		RunID:          run.RunID,
		Workflow:       run.State.WorkflowName,
		WorkflowDigest: run.State.WorkflowDigest,
		Status:         run.State.Status,
		Seq:            sequence(run.State.UpdatedAt, asOf),
		CreatedAt:      run.State.CreatedAt,
		StartedAt:      run.State.StartedAt,
		FinishedAt:     run.State.FinishedAt,
		UpdatedAt:      run.State.UpdatedAt,
		DurationMs:     snapshot.DurationMs,
		WaitingMs:      snapshot.WaitingMs,
		Inputs:         inputs(run.State.Inputs),
		Steps:          make([]hubapi.StepReport, 0, len(run.Workflow.Nodes)),
	}
	if report.Workflow == "" {
		report.Workflow = run.Workflow.Name
	}
	report.Graph = graphOf(run.Workflow)
	if trigger := run.State.Trigger; trigger != nil {
		report.Trigger = &hubapi.Trigger{ID: trigger.ID, Kind: trigger.Kind, DeliveryID: trigger.DeliveryID, ScheduledAt: trigger.ScheduledAt}
	}
	durations := map[string]metrics.NodeSnapshot{}
	for _, node := range snapshot.Nodes {
		durations[node.ID] = node
	}
	for _, definition := range run.Workflow.Nodes {
		state := run.State.Nodes[definition.ID]
		if state == nil {
			continue
		}
		measured := durations[definition.ID]
		step := hubapi.StepReport{
			ID:          definition.ID,
			Type:        executorOf(definition, state),
			Status:      state.Status,
			SkipReason:  state.Reason,
			Attempts:    state.Attempts,
			StartedAt:   state.StartedAt,
			FinishedAt:  state.FinishedAt,
			DurationMs:  measured.DurationMs,
			WaitingMs:   measured.WaitingMs,
			Error:       clip(Redact(state.Error), maxErrorRunes),
			OutputBytes: state.OutputBytes,
			LogBytes:    state.StdoutBytes + state.StderrBytes,
		}
		if result, ok := state.Result.(map[string]any); ok {
			step.Summary = clip(Redact(text(result["summary"])), maxSummaryRunes)
			step.Reason = clip(Redact(text(result["reason"])), maxSummaryRunes)
		}
		if report.Error == "" && state.Status == "failed" && step.Error != "" {
			report.Error = definition.ID + ": " + step.Error
		}
		report.Steps = append(report.Steps, step)
	}
	return report
}

// sequence orders the reports of one run: later saves have later timestamps.
func sequence(updatedAt string, fallback time.Time) int64 {
	if parsed, err := time.Parse(time.RFC3339Nano, updatedAt); err == nil {
		return parsed.UnixNano()
	}
	return fallback.UnixNano()
}

func inputs(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	out := map[string]string{}
	for _, name := range names {
		if len(out) == maxInputs {
			break
		}
		out[name] = clip(redactValue(name, values[name]), maxInputRunes)
	}
	return out
}

func text(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

// executorOf names what ran a node: the candidate the engine selected, or the
// first candidate before one was selected.
func executorOf(node model.Node, state *model.NodeState) string {
	if state.SelectedExecutor != "" {
		return state.SelectedExecutor
	}
	if len(node.Execution.Candidates) > 0 {
		return node.Execution.Candidates[0].Executor
	}
	return ""
}

// graphOf describes the workflow for the Hub's canvas. Conditions are
// expressions from the workflow file, not run data, but they are clipped and
// masked like any other text that leaves the machine.
func graphOf(workflow model.Workflow) *hubapi.Graph {
	if len(workflow.Nodes) == 0 {
		return nil
	}
	out := &hubapi.Graph{Entry: workflow.Entry, Nodes: make([]hubapi.GraphNode, 0, len(workflow.Nodes)), Edges: make([]hubapi.GraphEdge, 0, len(workflow.Edges))}
	parallel := map[string]bool{}
	for _, node := range workflow.Nodes {
		item := hubapi.GraphNode{ID: node.ID, Parallel: node.Dispatch == "all"}
		for _, candidate := range node.Execution.Candidates {
			item.Executors = append(item.Executors, candidate.Executor)
		}
		if node.Join != nil {
			item.Join = append([]string(nil), node.Join.From...)
		}
		parallel[node.ID] = item.Parallel
		out.Nodes = append(out.Nodes, item)
	}
	for _, edge := range workflow.Edges {
		out.Edges = append(out.Edges, hubapi.GraphEdge{Source: edge.From, Target: edge.To, Condition: clip(Redact(edge.When), maxSummaryRunes),
			MaxIterations: edge.MaxIterations, Parallel: parallel[edge.From]})
	}
	return out
}
