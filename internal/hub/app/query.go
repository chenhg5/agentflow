package app

import (
	"context"
	"sort"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/domain"
)

// Query answers the questions the pages and the API ask. Aggregates are
// computed here from plain run listings, so a repository only has to filter.
type Query struct {
	Store Store
	Now   func() time.Time
}

// Counts splits a set of runs by outcome.
type Counts struct {
	Total     int `json:"total"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
	Waiting   int `json:"waiting"`
	Active    int `json:"active"`
}

// SuccessRate is succeeded / finished, or -1 when nothing finished.
func (c Counts) SuccessRate() float64 {
	finished := c.Succeeded + c.Failed
	if finished == 0 {
		return -1
	}
	return float64(c.Succeeded) / float64(finished)
}

func (c *Counts) add(status domain.Status) {
	c.Total++
	switch status {
	case domain.StatusSucceeded:
		c.Succeeded++
	case domain.StatusFailed:
		c.Failed++
	case domain.StatusCancelled:
		c.Cancelled++
	case domain.StatusWaiting:
		c.Waiting++
	default:
		c.Active++
	}
}

// Day is one bar of the daily runs chart.
type Day struct {
	Date   time.Time `json:"date"`
	Counts Counts    `json:"counts"`
}

// WorkflowStat summarises one workflow.
type WorkflowStat struct {
	Name       string          `json:"name"`
	Counts     Counts          `json:"counts"`
	P50Ms      int64           `json:"p50_ms"`
	P95Ms      int64           `json:"p95_ms"`
	Last       *domain.Run     `json:"-"`
	LastStatus domain.Status   `json:"last_status"`
	LastAt     time.Time       `json:"last_at"`
	Machines   []string        `json:"machines"`
	Recent     []domain.Status `json:"recent"` // oldest → newest, for a sparkline of outcomes
}

// Overview is the landing page.
type Overview struct {
	Window    time.Duration  `json:"-"`
	Days      int            `json:"days"`
	Counts    Counts         `json:"counts"`
	AvgMs     int64          `json:"avg_ms"`
	Daily     []Day          `json:"daily"`
	Failures  []domain.Run   `json:"-"`
	Waiting   []domain.Run   `json:"-"`
	Running   []domain.Run   `json:"-"`
	Workflows []WorkflowStat `json:"workflows"`
	Machines  int            `json:"machines"`
}

const recentOutcomes = 12

// Overview summarises the last days days.
func (q Query) Overview(ctx context.Context, days int) (Overview, error) {
	now := q.now()
	since := startOfDay(now).AddDate(0, 0, -(days - 1))
	runs, _, err := q.Store.List(ctx, RunFilter{Since: since})
	if err != nil {
		return Overview{}, err
	}
	overview := Overview{Days: days, Window: now.Sub(since)}
	byDay := map[string]*Counts{}
	var finishedTotal, finishedCount int64
	for _, run := range runs {
		overview.Counts.add(run.Status)
		day := startOfDay(run.When()).Format(time.DateOnly)
		if byDay[day] == nil {
			byDay[day] = &Counts{}
		}
		byDay[day].add(run.Status)
		if run.Status.Finished() && run.DurationMs > 0 {
			finishedTotal += run.DurationMs - run.WaitingMs
			finishedCount++
		}
		switch {
		case run.Status == domain.StatusFailed && len(overview.Failures) < 8:
			overview.Failures = append(overview.Failures, run)
		case run.Status == domain.StatusWaiting:
			overview.Waiting = append(overview.Waiting, run)
		case run.Status.Active() && len(overview.Running) < 8:
			overview.Running = append(overview.Running, run)
		}
	}
	if finishedCount > 0 {
		overview.AvgMs = finishedTotal / finishedCount
	}
	for day := since; !day.After(now); day = day.AddDate(0, 0, 1) {
		entry := Day{Date: day}
		if counts := byDay[day.Format(time.DateOnly)]; counts != nil {
			entry.Counts = *counts
		}
		overview.Daily = append(overview.Daily, entry)
	}
	overview.Workflows = workflowStats(runs)
	machines, err := q.Store.Machines(ctx)
	if err != nil {
		return Overview{}, err
	}
	overview.Machines = len(machines)
	return overview, nil
}

// Workflows summarises every workflow with runs in the last days days.
func (q Query) Workflows(ctx context.Context, days int) ([]WorkflowStat, error) {
	runs, _, err := q.Store.List(ctx, RunFilter{Since: startOfDay(q.now()).AddDate(0, 0, -(days - 1))})
	if err != nil {
		return nil, err
	}
	return workflowStats(runs), nil
}

// Runs lists runs.
func (q Query) Runs(ctx context.Context, filter RunFilter) ([]domain.Run, int, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	return q.Store.List(ctx, filter)
}

// Run returns one run with its steps.
func (q Query) Run(ctx context.Context, key domain.RunKey) (domain.Run, error) {
	return q.Store.Get(ctx, key)
}

// Machines lists machines, most recently seen first.
func (q Query) Machines(ctx context.Context) ([]domain.Machine, error) {
	machines, err := q.Store.Machines(ctx)
	sort.Slice(machines, func(i, j int) bool { return machines[i].LastSeen.After(machines[j].LastSeen) })
	return machines, err
}

// workflowStats expects runs newest first.
func workflowStats(runs []domain.Run) []WorkflowStat {
	stats := map[string]*WorkflowStat{}
	durations := map[string][]int64{}
	machines := map[string]map[string]bool{}
	order := []string{}
	for index := range runs {
		run := runs[index]
		stat := stats[run.Workflow]
		if stat == nil {
			stat = &WorkflowStat{Name: run.Workflow, Last: &runs[index], LastStatus: run.Status, LastAt: run.When()}
			stats[run.Workflow] = stat
			machines[run.Workflow] = map[string]bool{}
			order = append(order, run.Workflow)
		}
		stat.Counts.add(run.Status)
		machines[run.Workflow][run.Key.Machine] = true
		if len(stat.Recent) < recentOutcomes && run.Status.Finished() {
			stat.Recent = append([]domain.Status{run.Status}, stat.Recent...)
		}
		if run.Status.Finished() && run.DurationMs > 0 {
			durations[run.Workflow] = append(durations[run.Workflow], run.DurationMs-run.WaitingMs)
		}
	}
	out := make([]WorkflowStat, 0, len(order))
	for _, name := range order {
		stat := stats[name]
		stat.P50Ms, stat.P95Ms = percentile(durations[name], 50), percentile(durations[name], 95)
		for machine := range machines[name] {
			stat.Machines = append(stat.Machines, machine)
		}
		sort.Strings(stat.Machines)
		out = append(out, *stat)
	}
	return out
}

func percentile(values []int64, p int) int64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int64(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := (len(sorted)*p + 99) / 100
	if index < 1 {
		index = 1
	}
	return sorted[index-1]
}

func startOfDay(t time.Time) time.Time {
	t = t.Local()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func (q Query) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}
