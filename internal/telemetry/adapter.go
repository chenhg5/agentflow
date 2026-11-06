package telemetry

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/chenhg5/agentflow/internal/hubapi"
	"github.com/chenhg5/agentflow/internal/metrics"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/version"
)

const (
	maxErrorRunes       = 2000
	maxSummaryRunes     = 500
	maxDescriptionRunes = 2000
	maxInputRunes       = 300
	maxInputs           = 30
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
	source := hubapi.Source{Machine: machine, Hostname: hostname, Project: filepath.Base(project), Path: project, Version: version.Version}
	if config.AcceptCommands {
		source.Capabilities = []string{hubapi.CapabilitySubmit}
	}
	return source
}

// FromRun adapts a run of the JSON engine to the reporting contract.
func FromRun(run *model.Run, source hubapi.Source, asOf time.Time) hubapi.RunReport {
	snapshot := metrics.Snapshot(run, asOf)
	report := hubapi.RunReport{
		Protocol:       hubapi.ProtocolVersion,
		Source:         source,
		Orchestration:  orchestrationOf(run),
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
	report.WorkflowTitle = clip(Redact(run.Workflow.Title), maxSummaryRunes)
	report.WorkflowDescription = clip(Redact(run.Workflow.Description), maxDescriptionRunes)
	report.Group = clip(Redact(run.Workflow.Group), maxSummaryRunes)
	for _, tag := range run.Workflow.Tags {
		report.Tags = append(report.Tags, clip(Redact(tag), maxSummaryRunes))
	}
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
		step.Artifacts = artifactsOf(run.RunDir, state)
		if report.Error == "" && state.Status == "failed" && step.Error != "" {
			report.Error = definition.ID + ": " + step.Error
		}
		report.Steps = append(report.Steps, step)
	}
	attachTails(run, report.Steps)
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
		item := hubapi.GraphNode{ID: node.ID, Parallel: node.Dispatch == "all", OutputSchema: node.OutputSchema}
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

const (
	tailBytes       = 2048      // per log stream
	resultBytes     = 2048      // per result preview
	reportTailQuota = 32 * 1024 // all tails and previews in one report
)

// latestArtifacts are the files of the step's latest attempt.
func latestArtifacts(state *model.NodeState) map[string]string {
	if n := len(state.AttemptSequence); n > 0 && len(state.AttemptSequence[n-1].Artifacts) > 0 {
		return state.AttemptSequence[n-1].Artifacts
	}
	return state.Artifacts
}

// artifactsOf lists the latest attempt's files by name and size.
func artifactsOf(runDir string, state *model.NodeState) []hubapi.Artifact {
	files := latestArtifacts(state)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []hubapi.Artifact
	for _, name := range names {
		info, err := os.Stat(filepath.Join(runDir, filepath.FromSlash(files[name])))
		if err != nil || info.IsDir() {
			continue
		}
		out = append(out, hubapi.Artifact{Name: name, Bytes: info.Size()})
	}
	return out
}

// attachTails adds log tails and result previews within the report's quota:
// failed steps first, then running and waiting ones, then the most recently
// finished. Steps left out keep the tails the Hub already has.
func attachTails(run *model.Run, steps []hubapi.StepReport) {
	order := make([]int, len(steps))
	for index := range order {
		order[index] = index
	}
	rank := func(status string) int {
		switch status {
		case "failed":
			return 0
		case "running", "waiting_input", "waiting_human":
			return 1
		case "succeeded":
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := steps[order[i]], steps[order[j]]
		if rank(a.Status) != rank(b.Status) {
			return rank(a.Status) < rank(b.Status)
		}
		return a.FinishedAt > b.FinishedAt
	})
	left := reportTailQuota
	for _, index := range order {
		step := &steps[index]
		if rank(step.Status) == 3 || left <= 0 {
			continue
		}
		files := latestArtifacts(run.State.Nodes[step.ID])
		for _, part := range []struct {
			key   string
			into  *string
			limit int
			head  bool
		}{{"stderrLog", &step.StderrTail, tailBytes, false}, {"stdoutLog", &step.StdoutTail, tailBytes, false}, {"result", &step.Result, resultBytes, true}} {
			path, ok := files[part.key]
			if !ok || left <= 0 {
				continue
			}
			text := readEnd(filepath.Join(run.RunDir, filepath.FromSlash(path)), min(part.limit, left), part.head)
			*part.into = Redact(text)
			left -= len(*part.into)
		}
	}
}

// readEnd reads up to limit bytes from the end of a file (or its start, for a
// result preview), trimmed to whole UTF-8 characters.
func readEnd(path string, limit int, head bool) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		return ""
	}
	size := info.Size()
	offset := int64(0)
	if !head && size > int64(limit) {
		offset = size - int64(limit)
	}
	buf := make([]byte, min(int64(limit), size))
	n, _ := file.ReadAt(buf, offset)
	buf = buf[:n]
	if offset > 0 {
		// The cut can fall inside a secret whose prefix would have matched a
		// mask pattern: drop the partial first line rather than send it.
		if newline := bytes.IndexByte(buf, '\n'); newline >= 0 {
			buf = buf[newline+1:]
		} else {
			buf = nil
		}
	}
	for len(buf) > 0 && !utf8.Valid(buf) {
		buf = buf[:len(buf)-1]
	}
	text := string(buf)
	if offset > 0 {
		text = "…" + text
	} else if head && size > int64(len(buf)) {
		text += "…"
	}
	return text
}

// orchestrationOf tells the Hub whether the graph engine or a script drove
// the run (see internal/coderun).
func orchestrationOf(run *model.Run) string {
	if run.Workflow.Version == "code" {
		return hubapi.OrchestrationCode
	}
	return hubapi.OrchestrationJSON
}
