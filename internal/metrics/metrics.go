package metrics

import (
	"sort"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
)

// RunSnapshot is the observable execution summary for one workflow run.
type RunSnapshot struct {
	OK             bool               `json:"ok"`
	RunID          string             `json:"run_id"`
	Workflow       string             `json:"workflow"`
	WorkflowDigest string             `json:"workflow_digest"`
	Status         string             `json:"status"`
	Trigger        *model.TriggerInfo `json:"trigger,omitempty"`
	StartedAt      string             `json:"started_at,omitempty"`
	FinishedAt     string             `json:"finished_at,omitempty"`
	AsOf           string             `json:"as_of"`
	DurationMs     int64              `json:"duration_ms"`
	ActiveMs       int64              `json:"active_ms"`
	WaitingMs      int64              `json:"waiting_ms"`
	Totals         ByteTotals         `json:"totals"`
	Nodes          []NodeSnapshot     `json:"nodes"`
}

type NodeSnapshot struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Status      string `json:"status"`
	Workspace   string `json:"workspace,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
	Attempts    int    `json:"attempts"`
	DurationMs  int64  `json:"duration_ms"`
	WaitingMs   int64  `json:"waiting_ms,omitempty"`
	InputBytes  int64  `json:"input_bytes"`
	OutputBytes int64  `json:"output_bytes"`
	StdoutBytes int64  `json:"stdout_bytes"`
	StderrBytes int64  `json:"stderr_bytes"`
}

type ByteTotals struct {
	Attempts    int   `json:"attempts"`
	InputBytes  int64 `json:"input_bytes"`
	OutputBytes int64 `json:"output_bytes"`
	StdoutBytes int64 `json:"stdout_bytes"`
	StderrBytes int64 `json:"stderr_bytes"`
}

type Distribution struct {
	Count int   `json:"count"`
	Total int64 `json:"total_ms"`
	Avg   int64 `json:"avg_ms"`
	Min   int64 `json:"min_ms"`
	Max   int64 `json:"max_ms"`
	P50   int64 `json:"p50_ms"`
	P95   int64 `json:"p95_ms"`
}

type RunCounts struct {
	Total        int `json:"total"`
	Succeeded    int `json:"succeeded"`
	Failed       int `json:"failed"`
	WaitingHuman int `json:"waiting_human"`
	Running      int `json:"running"`
	Cancelled    int `json:"cancelled"`
}

type NodeAggregate struct {
	ID         string       `json:"id"`
	Type       string       `json:"type"`
	Executions int          `json:"executions"`
	Succeeded  int          `json:"succeeded"`
	Failed     int          `json:"failed"`
	Skipped    int          `json:"skipped"`
	Duration   Distribution `json:"duration_ms"`
	Waiting    Distribution `json:"waiting_ms"`
	Totals     ByteTotals   `json:"totals"`
}

// AggregateSnapshot summarizes all matching runs. It is intentionally local and
// deterministic so it can be exported or sent to another metrics system later.
//
// Run-level distributions only include finished runs, so a run still waiting
// for a human does not inflate them. Duration is wall-clock time, Waiting is
// time spent in human nodes, and Active is the difference.
type AggregateSnapshot struct {
	OK             bool            `json:"ok"`
	Workflow       string          `json:"workflow,omitempty"`
	WorkflowDigest string          `json:"workflow_digest,omitempty"`
	AsOf           string          `json:"as_of"`
	FirstRunAt     string          `json:"first_run_at,omitempty"`
	LastRunAt      string          `json:"last_run_at,omitempty"`
	Runs           RunCounts       `json:"runs"`
	Duration       Distribution    `json:"duration_ms"`
	Active         Distribution    `json:"active_ms"`
	Waiting        Distribution    `json:"waiting_ms"`
	Totals         ByteTotals      `json:"totals"`
	NodeGroups     []NodeAggregate `json:"nodes"`
}

// Filter selects runs for aggregation. Empty fields match everything.
type Filter struct {
	Workflow string
	Digest   string
}

func (f Filter) matches(run *model.Run) bool {
	if run == nil {
		return false
	}
	if f.Workflow != "" && run.State.WorkflowName != f.Workflow {
		return false
	}
	return f.Digest == "" || run.WorkflowDigest() == f.Digest
}

func Snapshot(run *model.Run, asOf time.Time) RunSnapshot {
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	startedAt := run.State.StartedAt
	if startedAt == "" {
		startedAt = run.State.CreatedAt
	}
	finishedAt := run.State.FinishedAt
	duration := run.State.DurationMs
	if duration == 0 {
		duration = DurationMillis(startedAt, finishedAt, asOf)
	}
	result := RunSnapshot{
		OK:             run.State.Status != "failed",
		RunID:          run.RunID,
		Workflow:       run.State.WorkflowName,
		WorkflowDigest: run.WorkflowDigest(),
		Status:         run.State.Status,
		Trigger:        run.State.Trigger,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
		AsOf:           asOf.UTC().Format(time.RFC3339Nano),
		DurationMs:     duration,
		Nodes:          make([]NodeSnapshot, 0, len(run.Workflow.Nodes)),
	}
	for _, definition := range run.Workflow.Nodes {
		state := run.State.Nodes[definition.ID]
		if state == nil {
			continue
		}
		nodeDuration := state.DurationMs
		if nodeDuration == 0 {
			nodeDuration = DurationMillis(state.StartedAt, state.FinishedAt, asOf)
		}
		waiting := state.WaitDurationMs
		if waiting == 0 && state.Status == "waiting_human" {
			waiting = DurationMillis(state.StartedAt, "", asOf)
		}
		node := NodeSnapshot{
			ID:          definition.ID,
			Type:        definition.Type,
			Status:      state.Status,
			Workspace:   state.Workspace,
			SessionID:   state.SessionID,
			Attempts:    state.Attempts,
			DurationMs:  nodeDuration,
			WaitingMs:   waiting,
			InputBytes:  state.InputBytes,
			OutputBytes: state.OutputBytes,
			StdoutBytes: state.StdoutBytes,
			StderrBytes: state.StderrBytes,
		}
		result.Nodes = append(result.Nodes, node)
		result.WaitingMs += node.WaitingMs
		result.Totals.Attempts += node.Attempts
		result.Totals.InputBytes += node.InputBytes
		result.Totals.OutputBytes += node.OutputBytes
		result.Totals.StdoutBytes += node.StdoutBytes
		result.Totals.StderrBytes += node.StderrBytes
	}
	if result.WaitingMs > result.DurationMs {
		result.WaitingMs = result.DurationMs
	}
	result.ActiveMs = result.DurationMs - result.WaitingMs
	return result
}

// AggregateByDigest returns one aggregate per workflow version, ordered by the
// first run of each version, so a workflow change can be compared with the
// version before it.
func AggregateByDigest(runs []*model.Run, workflow string, asOf time.Time) []AggregateSnapshot {
	digests := []string{}
	seen := map[string]bool{}
	ordered := append([]*model.Run(nil), runs...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].State.CreatedAt < ordered[j].State.CreatedAt })
	for _, run := range ordered {
		if !(Filter{Workflow: workflow}).matches(run) {
			continue
		}
		digest := run.WorkflowDigest()
		if !seen[digest] {
			seen[digest] = true
			digests = append(digests, digest)
		}
	}
	result := make([]AggregateSnapshot, 0, len(digests))
	for _, digest := range digests {
		result = append(result, Aggregate(runs, Filter{Workflow: workflow, Digest: digest}, asOf))
	}
	return result
}

func Aggregate(runs []*model.Run, filter Filter, asOf time.Time) AggregateSnapshot {
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	result := AggregateSnapshot{OK: true, Workflow: filter.Workflow, WorkflowDigest: filter.Digest, AsOf: asOf.UTC().Format(time.RFC3339Nano), NodeGroups: []NodeAggregate{}}
	var durations, active, waiting []int64
	var waitingDurations = map[string][]int64{}
	var nodeDurations = map[string][]int64{}
	groups := map[string]*NodeAggregate{}
	for _, run := range runs {
		if !filter.matches(run) {
			continue
		}
		snapshot := Snapshot(run, asOf)
		if result.FirstRunAt == "" || run.State.CreatedAt < result.FirstRunAt {
			result.FirstRunAt = run.State.CreatedAt
		}
		if run.State.CreatedAt > result.LastRunAt {
			result.LastRunAt = run.State.CreatedAt
		}
		result.Runs.Total++
		switch run.State.Status {
		case "succeeded":
			result.Runs.Succeeded++
		case "failed":
			result.Runs.Failed++
		case "waiting_human":
			result.Runs.WaitingHuman++
		case "running":
			result.Runs.Running++
		case "cancelled":
			result.Runs.Cancelled++
		}
		if finished(run.State.Status) {
			durations = append(durations, snapshot.DurationMs)
			active = append(active, snapshot.ActiveMs)
			waiting = append(waiting, snapshot.WaitingMs)
		}
		result.Totals.Attempts += snapshot.Totals.Attempts
		result.Totals.InputBytes += snapshot.Totals.InputBytes
		result.Totals.OutputBytes += snapshot.Totals.OutputBytes
		result.Totals.StdoutBytes += snapshot.Totals.StdoutBytes
		result.Totals.StderrBytes += snapshot.Totals.StderrBytes
		for _, node := range snapshot.Nodes {
			if node.Status == "pending" {
				continue
			}
			group, ok := groups[node.ID]
			if !ok {
				group = &NodeAggregate{ID: node.ID, Type: node.Type}
				groups[node.ID] = group
			}
			if node.Status != "skipped" {
				group.Executions++
			}
			switch node.Status {
			case "succeeded":
				group.Succeeded++
			case "failed":
				group.Failed++
			case "skipped":
				group.Skipped++
			}
			group.Totals.Attempts += node.Attempts
			group.Totals.InputBytes += node.InputBytes
			group.Totals.OutputBytes += node.OutputBytes
			group.Totals.StdoutBytes += node.StdoutBytes
			group.Totals.StderrBytes += node.StderrBytes
			nodeDurations[node.ID] = append(nodeDurations[node.ID], node.DurationMs)
			if node.WaitingMs > 0 {
				waitingDurations[node.ID] = append(waitingDurations[node.ID], node.WaitingMs)
			}
		}
	}
	result.Duration = summarize(durations)
	result.Active = summarize(active)
	result.Waiting = summarize(waiting)
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		group := groups[id]
		group.Duration = summarize(nodeDurations[id])
		group.Waiting = summarize(waitingDurations[id])
		result.NodeGroups = append(result.NodeGroups, *group)
	}
	return result
}

func finished(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}

func MarkRunStarted(state *model.RunState, at time.Time) {
	if state.StartedAt == "" {
		state.StartedAt = at.UTC().Format(time.RFC3339Nano)
	}
}

func MarkRunFinished(state *model.RunState, at time.Time) {
	state.FinishedAt = at.UTC().Format(time.RFC3339Nano)
	state.DurationMs = DurationMillis(state.StartedAt, state.FinishedAt, at)
}

func RefreshNode(state *model.NodeState, at time.Time) {
	state.DurationMs = DurationMillis(state.StartedAt, state.FinishedAt, at)
	if state.Status == "waiting_human" {
		state.WaitDurationMs = DurationMillis(state.StartedAt, "", at)
	}
}

func DurationMillis(start, finish string, asOf time.Time) int64 {
	if start == "" {
		return 0
	}
	started, err := time.Parse(time.RFC3339Nano, start)
	if err != nil {
		return 0
	}
	ended := asOf
	if finish != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, finish); err == nil {
			ended = parsed
		}
	}
	if ended.Before(started) {
		return 0
	}
	return ended.Sub(started).Milliseconds()
}

func summarize(values []int64) Distribution {
	if len(values) == 0 {
		return Distribution{}
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var total int64
	for _, value := range sorted {
		total += value
	}
	return Distribution{
		Count: len(sorted),
		Total: total,
		Avg:   total / int64(len(sorted)),
		Min:   sorted[0],
		Max:   sorted[len(sorted)-1],
		P50:   percentile(sorted, 0.50),
		P95:   percentile(sorted, 0.95),
	}
}

func percentile(values []int64, quantile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * quantile)
	return values[index]
}
