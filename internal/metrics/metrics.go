package metrics

import (
	"fmt"
	"math"
	"sort"
	"strings"
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
	Evaluation     *model.Evaluation  `json:"evaluation,omitempty"`
}

type NodeSnapshot struct {
	ID          string `json:"id"`
	Executor    string `json:"executor"`
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
	WaitingInput int `json:"waiting_input"`
	Running      int `json:"running"`
	Cancelled    int `json:"cancelled"`
}

type NodeAggregate struct {
	ID         string       `json:"id"`
	Executor   string       `json:"executor"`
	Executions int          `json:"executions"`
	Succeeded  int          `json:"succeeded"`
	Failed     int          `json:"failed"`
	Skipped    int          `json:"skipped"`
	Duration   Distribution `json:"duration_ms"`
	Waiting    Distribution `json:"waiting_ms"`
	Totals     ByteTotals   `json:"totals"`
}

type NumericDistribution struct {
	Count int     `json:"count"`
	Total float64 `json:"total"`
	Avg   float64 `json:"avg"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	P50   float64 `json:"p50"`
	P95   float64 `json:"p95"`
}

type MeasurementAggregate struct {
	Name      string              `json:"name"`
	Unit      string              `json:"unit,omitempty"`
	Direction string              `json:"direction,omitempty"`
	Values    NumericDistribution `json:"values"`
}

// AggregateSnapshot summarizes all matching runs. It is intentionally local and
// deterministic so it can be exported or sent to another metrics system later.
//
// Run-level distributions only include finished runs, so a run still waiting
// for a human does not inflate them. Duration is wall-clock time, Waiting is
// time spent in human nodes, and Active is the difference.
type AggregateSnapshot struct {
	OK             bool                   `json:"ok"`
	Workflow       string                 `json:"workflow,omitempty"`
	WorkflowDigest string                 `json:"workflow_digest,omitempty"`
	AsOf           string                 `json:"as_of"`
	FirstRunAt     string                 `json:"first_run_at,omitempty"`
	LastRunAt      string                 `json:"last_run_at,omitempty"`
	Runs           RunCounts              `json:"runs"`
	Duration       Distribution           `json:"duration_ms"`
	Active         Distribution           `json:"active_ms"`
	Waiting        Distribution           `json:"waiting_ms"`
	Totals         ByteTotals             `json:"totals"`
	NodeGroups     []NodeAggregate        `json:"nodes"`
	Measurements   []MeasurementAggregate `json:"measurements,omitempty"`
}

type MetricComparison struct {
	Baseline      float64  `json:"baseline"`
	Current       float64  `json:"current"`
	Delta         float64  `json:"delta"`
	ChangePercent *float64 `json:"change_percent,omitempty"`
	Direction     string   `json:"direction"`
	Unit          string   `json:"unit,omitempty"`
	Improved      *bool    `json:"improved,omitempty"`
}

// Comparison explains the change from a previous run to a current run. The
// runtime measurements use lower-is-better semantics for elapsed time,
// waiting time, and attempts. User evaluation measurements carry their own
// direction and remain separate from these derived runtime facts.
type Comparison struct {
	OK           bool                        `json:"ok"`
	Current      RunSnapshot                 `json:"current"`
	Baseline     RunSnapshot                 `json:"baseline"`
	SameWorkflow bool                        `json:"same_workflow"`
	SameVersion  bool                        `json:"same_version"`
	Runtime      map[string]MetricComparison `json:"runtime"`
	Measurements map[string]MetricComparison `json:"measurements,omitempty"`
}

// Filter selects runs for aggregation. Empty fields match everything.
type Filter struct {
	Workflow string
	Digest   string
}

func ValidateEvaluation(evaluation model.Evaluation) error {
	if evaluation.EvaluatedAt == "" {
		return fmt.Errorf("evaluatedAt is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, evaluation.EvaluatedAt); err != nil {
		return fmt.Errorf("evaluatedAt must be an RFC3339 timestamp")
	}
	for name, measurement := range evaluation.Measurements {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("measurement name cannot be empty")
		}
		if math.IsNaN(measurement.Value) || math.IsInf(measurement.Value, 0) {
			return fmt.Errorf("measurement %q value must be finite", name)
		}
		if normalizeDirection(measurement.Direction) == "" {
			return fmt.Errorf("measurement %q direction must be higher, lower, or neutral", name)
		}
	}
	return nil
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
		Evaluation:     run.State.Evaluation,
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
		if waiting == 0 && state.Status == "waiting_input" {
			waiting = DurationMillis(state.StartedAt, "", asOf)
		}
		node := NodeSnapshot{
			ID:          definition.ID,
			Executor:    definition.Executor,
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
		case "waiting_input":
			result.Runs.WaitingInput++
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
				group = &NodeAggregate{ID: node.ID, Executor: node.Executor}
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
	result.Measurements = aggregateMeasurements(runs, filter)
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

func aggregateMeasurements(runs []*model.Run, filter Filter) []MeasurementAggregate {
	type values struct {
		name      string
		unit      string
		direction string
		items     []float64
	}
	groups := map[string]*values{}
	for _, run := range runs {
		if !filter.matches(run) || !finished(run.State.Status) || run.State.Evaluation == nil {
			continue
		}
		for name, measurement := range run.State.Evaluation.Measurements {
			direction := normalizeDirection(measurement.Direction)
			key := name + "\x00" + measurement.Unit + "\x00" + direction
			group, ok := groups[key]
			if !ok {
				group = &values{name: name, unit: measurement.Unit, direction: direction}
				groups[key] = group
			}
			group.items = append(group.items, measurement.Value)
		}
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]MeasurementAggregate, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		result = append(result, MeasurementAggregate{Name: group.name, Unit: group.unit, Direction: group.direction, Values: summarizeFloat(group.items)})
	}
	return result
}

func Compare(current, baseline *model.Run, asOf time.Time) Comparison {
	if asOf.IsZero() {
		asOf = time.Now().UTC()
	}
	currentSnapshot := Snapshot(current, asOf)
	baselineSnapshot := Snapshot(baseline, asOf)
	return Comparison{
		OK:           true,
		Current:      currentSnapshot,
		Baseline:     baselineSnapshot,
		SameWorkflow: current.State.WorkflowName == baseline.State.WorkflowName,
		SameVersion:  current.WorkflowDigest() == baseline.WorkflowDigest(),
		Runtime: map[string]MetricComparison{
			"duration_ms": compareMetric(float64(currentSnapshot.DurationMs), float64(baselineSnapshot.DurationMs), "lower", "ms"),
			"active_ms":   compareMetric(float64(currentSnapshot.ActiveMs), float64(baselineSnapshot.ActiveMs), "lower", "ms"),
			"waiting_ms":  compareMetric(float64(currentSnapshot.WaitingMs), float64(baselineSnapshot.WaitingMs), "lower", "ms"),
			"attempts":    compareMetric(float64(currentSnapshot.Totals.Attempts), float64(baselineSnapshot.Totals.Attempts), "lower", "count"),
		},
		Measurements: compareMeasurements(current.State.Evaluation, baseline.State.Evaluation),
	}
}

func compareMeasurements(current, baseline *model.Evaluation) map[string]MetricComparison {
	result := map[string]MetricComparison{}
	if current == nil || baseline == nil {
		return result
	}
	for name, measurement := range current.Measurements {
		previous, ok := baseline.Measurements[name]
		if !ok {
			continue
		}
		if measurement.Unit != previous.Unit {
			continue
		}
		direction := normalizeDirection(measurement.Direction)
		previousDirection := normalizeDirection(previous.Direction)
		if direction != previousDirection {
			continue
		}
		result[name] = compareMetric(measurement.Value, previous.Value, direction, measurement.Unit)
	}
	return result
}

func compareMetric(current, baseline float64, direction, unit string) MetricComparison {
	comparison := MetricComparison{Baseline: baseline, Current: current, Delta: current - baseline, Direction: direction, Unit: unit}
	if baseline != 0 {
		change := (current - baseline) / math.Abs(baseline) * 100
		comparison.ChangePercent = &change
	}
	if direction == "higher" || direction == "lower" {
		improved := (direction == "higher" && current > baseline) || (direction == "lower" && current < baseline)
		comparison.Improved = &improved
	}
	return comparison
}

func normalizeDirection(direction string) string {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "higher", "up", "maximize", "max":
		return "higher"
	case "lower", "down", "minimize", "min":
		return "lower"
	case "neutral", "none", "":
		if strings.TrimSpace(direction) == "" {
			return "neutral"
		}
		return "neutral"
	default:
		return ""
	}
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
	if state.Status == "waiting_input" {
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

func summarizeFloat(values []float64) NumericDistribution {
	if len(values) == 0 {
		return NumericDistribution{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	var total float64
	for _, value := range sorted {
		total += value
	}
	return NumericDistribution{
		Count: len(sorted),
		Total: total,
		Avg:   total / float64(len(sorted)),
		Min:   sorted[0],
		Max:   sorted[len(sorted)-1],
		P50:   percentileFloat(sorted, 0.50),
		P95:   percentileFloat(sorted, 0.95),
	}
}

func percentile(values []int64, quantile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * quantile)
	return values[index]
}

func percentileFloat(values []float64, quantile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	index := int(float64(len(values)-1) * quantile)
	return values[index]
}
