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
	ID                string                  `json:"id"`
	Visit             int                     `json:"visit,omitempty"`
	Executor          string                  `json:"executor,omitempty"`
	SelectedCandidate *int                    `json:"selected_candidate,omitempty"`
	Status            string                  `json:"status"`
	Workspace         string                  `json:"workspace,omitempty"`
	SessionID         string                  `json:"session_id,omitempty"`
	SessionProtocol   string                  `json:"session_protocol,omitempty"`
	WaitingStartedAt  string                  `json:"waiting_started_at,omitempty"`
	Attempts          int                     `json:"attempts"`
	AttemptSequence   []model.AttemptEvidence `json:"attempt_sequence,omitempty"`
	Failure           *model.ExecutionFailure `json:"failure,omitempty"`
	FallbackReason    string                  `json:"fallback_reason,omitempty"`
	Artifacts         map[string]string       `json:"artifacts,omitempty"`
	DurationMs        int64                   `json:"duration_ms"`
	WaitingMs         int64                   `json:"waiting_ms,omitempty"`
	InputBytes        int64                   `json:"input_bytes"`
	OutputBytes       int64                   `json:"output_bytes"`
	StdoutBytes       int64                   `json:"stdout_bytes"`
	StderrBytes       int64                   `json:"stderr_bytes"`
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
	runStart, hasRunStart := parseTimestamp(startedAt)
	// A finished run freezes every derived interval at its durable end so a
	// cancelled wait cannot keep growing after completion.
	runEnd := asOf
	terminal := false
	if parsed, ok := parseTimestamp(finishedAt); ok {
		runEnd, terminal = parsed, true
	} else if duration > 0 && hasRunStart {
		runEnd = runStart.Add(time.Duration(duration) * time.Millisecond)
	}
	var waitIntervals []waitInterval
	var unplacedWait int64
	for _, definition := range run.Workflow.Nodes {
		for _, visit := range nodeVisits(run, definition.ID) {
			state := &visit.State
			nodeDuration := state.DurationMs
			if nodeDuration == 0 {
				// For a terminal run the durable finish boundary also bounds an
				// unfinished node snapshot, so a cancelled retry/fallback
				// checkpoint that never restarted cannot keep growing.
				nodeDuration = DurationMillis(state.StartedAt, state.FinishedAt, runEnd)
			}
			waiting := state.WaitDurationMs
			if start, ok := parseTimestamp(state.WaitingStartedAt); ok {
				end, ended := parseTimestamp(state.FinishedAt)
				if !ended {
					switch {
					case state.Status == "waiting_input":
						end = runEnd
					case state.WaitDurationMs > 0:
						end = start.Add(time.Duration(state.WaitDurationMs) * time.Millisecond)
					default:
						end = runEnd
					}
				}
				if hasRunStart && start.Before(runStart) {
					start = runStart
				}
				if end.After(runEnd) {
					end = runEnd
				}
				if end.After(start) {
					waiting = end.Sub(start).Milliseconds()
					waitIntervals = append(waitIntervals, waitInterval{start: start, end: end})
				} else {
					waiting = 0
				}
			} else if waiting > 0 {
				// Legacy/archived evidence without a start timestamp cannot be
				// placed on the timeline; keep its recorded duration.
				unplacedWait += waiting
			}
			attempts := state.AttemptSequence
			if terminal {
				// A sibling branch can fail the run while another attempt is
				// still waiting_input. Freeze the snapshot's copy at the run
				// boundary instead of exposing an ever-growing in-flight
				// attempt in terminal metrics.
				attempts = make([]model.AttemptEvidence, len(state.AttemptSequence))
				for index, attempt := range state.AttemptSequence {
					attempts[index] = attempt
					if attempt.StartedAt != "" && attempt.FinishedAt == "" {
						attempts[index].FinishedAt = runEnd.Format(time.RFC3339Nano)
						attempts[index].DurationMs = DurationMillis(attempt.StartedAt, attempts[index].FinishedAt, runEnd)
					}
				}
			}
			node := NodeSnapshot{
				ID:                definition.ID,
				Visit:             visit.Visit,
				Executor:          state.SelectedExecutor,
				SelectedCandidate: state.SelectedCandidate,
				Status:            state.Status,
				Workspace:         state.Workspace,
				SessionID:         state.SessionID,
				SessionProtocol:   state.SessionProtocol,
				WaitingStartedAt:  state.WaitingStartedAt,
				Attempts:          state.Attempts,
				AttemptSequence:   attempts,
				Failure:           state.Failure,
				FallbackReason:    state.FallbackReason,
				Artifacts:         state.Artifacts,
				DurationMs:        nodeDuration,
				WaitingMs:         waiting,
				InputBytes:        state.InputBytes,
				OutputBytes:       state.OutputBytes,
				StdoutBytes:       state.StdoutBytes,
				StderrBytes:       state.StderrBytes,
			}
			result.Nodes = append(result.Nodes, node)
			result.WaitingMs += node.WaitingMs
			result.Totals.Attempts += node.Attempts
			result.Totals.InputBytes += node.InputBytes
			result.Totals.OutputBytes += node.OutputBytes
			result.Totals.StdoutBytes += node.StdoutBytes
			result.Totals.StderrBytes += node.StderrBytes
		}
	}
	// Parallel external waits overlap: the run waits for the union of those
	// intervals, not their arithmetic sum.
	result.WaitingMs = unionMillis(waitIntervals) + unplacedWait
	if result.WaitingMs > result.DurationMs {
		result.WaitingMs = result.DurationMs
	}
	if result.WaitingMs < 0 {
		result.WaitingMs = 0
	}
	result.ActiveMs = result.DurationMs - result.WaitingMs
	return result
}

type waitInterval struct {
	start time.Time
	end   time.Time
}

func parseTimestamp(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func unionMillis(intervals []waitInterval) int64 {
	if len(intervals) == 0 {
		return 0
	}
	sorted := append([]waitInterval(nil), intervals...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].start.Equal(sorted[j].start) {
			return sorted[i].end.Before(sorted[j].end)
		}
		return sorted[i].start.Before(sorted[j].start)
	})
	var total time.Duration
	current := sorted[0]
	for _, next := range sorted[1:] {
		if next.start.After(current.end) {
			total += current.end.Sub(current.start)
			current = next
			continue
		}
		if next.end.After(current.end) {
			current.end = next.end
		}
	}
	total += current.end.Sub(current.start)
	return total.Milliseconds()
}

type visitState struct {
	Visit int
	State model.NodeState
}

// nodeVisits returns each persisted visit exactly once. Completed current
// visits also appear in History; the current node state wins that duplicate so
// late submission or evaluation updates remain visible without double counts.
func nodeVisits(run *model.Run, nodeID string) []visitState {
	byVisit := map[int]model.NodeState{}
	for _, visit := range run.State.History {
		if visit.NodeID == nodeID {
			byVisit[visit.Visit] = visit.State
		}
	}
	if current := run.State.Nodes[nodeID]; current != nil {
		byVisit[run.State.Visits[nodeID]] = *current
	}
	visits := make([]int, 0, len(byVisit))
	for visit := range byVisit {
		visits = append(visits, visit)
	}
	sort.Ints(visits)
	result := make([]visitState, 0, len(visits))
	for _, visit := range visits {
		result = append(result, visitState{Visit: visit, State: byVisit[visit]})
	}
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
			key := node.ID + "\x00" + node.Executor
			group, ok := groups[key]
			if !ok {
				group = &NodeAggregate{ID: node.ID, Executor: node.Executor}
				groups[key] = group
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
			nodeDurations[key] = append(nodeDurations[key], node.DurationMs)
			if node.WaitingMs > 0 {
				waitingDurations[key] = append(waitingDurations[key], node.WaitingMs)
			}
		}
	}
	result.Duration = summarize(durations)
	result.Active = summarize(active)
	result.Waiting = summarize(waiting)
	result.Measurements = aggregateMeasurements(runs, filter)
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := groups[key]
		group.Duration = summarize(nodeDurations[key])
		group.Waiting = summarize(waitingDurations[key])
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
		state.WaitDurationMs = DurationMillis(state.WaitingStartedAt, "", at)
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
