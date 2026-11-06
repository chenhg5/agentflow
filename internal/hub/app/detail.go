package app

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/domain"
)

// RunDetail is everything the Hub knows about one run: what its page shows,
// in one value, so the page and the API cannot drift apart.
type RunDetail struct {
	Run      domain.Run
	Machine  domain.Machine
	Commands []domain.Command
	Inputs   []Input
}

// Input is a step of an unfinished run that waits for a person.
type Input struct {
	NodeID   string
	Executor string
	Since    time.Time
	// Accepts says whether the run's machine takes input from the Hub.
	Accepts bool
	// OutputSchema is what the step's output must satisfy.
	OutputSchema map[string]any
}

// Detail returns one run with its graph, machine, commands and the steps
// waiting for input.
func (q Query) Detail(ctx context.Context, key domain.RunKey) (RunDetail, error) {
	run, err := q.Run(ctx, key)
	if err != nil {
		return RunDetail{}, err
	}
	detail := RunDetail{Run: run}
	if detail.Machine, err = q.Machine(ctx, key.Machine); err != nil {
		return RunDetail{}, err
	}
	if detail.Commands, err = q.Store.RunCommands(ctx, key); err != nil {
		return RunDetail{}, err
	}
	detail.Inputs = inputsOf(run, detail.Machine)
	return detail, nil
}

func inputsOf(run domain.Run, machine domain.Machine) []Input {
	if run.Status.Finished() {
		return nil
	}
	schemas := map[string]map[string]any{}
	if run.Graph != nil {
		for _, node := range run.Graph.Nodes {
			schemas[node.ID] = node.OutputSchema
		}
	}
	var inputs []Input
	for _, step := range run.Steps {
		if step.Status.NeedsInput() {
			inputs = append(inputs, Input{NodeID: step.ID, Executor: step.Type, Since: step.StartedAt,
				Accepts: machine.Accepts(domain.CapabilitySubmit), OutputSchema: schemas[step.ID]})
		}
	}
	return inputs
}

// StepStat summarises one step of one workflow over many runs: where runs
// fail, slow down, retry and wait. It is what an agent reads to decide which
// step of a workflow to fix first.
type StepStat struct {
	Workflow string
	Step     string
	// Type and Provider are from the most recent visit.
	Type     string
	Provider string
	// Runs counts runs that reached the step; Counts splits them by the
	// step's last status in each run.
	Runs      int
	Counts    StepCounts
	Retried   int // runs where the step took more than one attempt
	P50Ms     int64
	P95Ms     int64
	WaitP50Ms int64 // time spent waiting for input or resources
	Errors    []ErrorGroup
	LastAt    time.Time
}

// StepCounts splits step visits by outcome.
type StepCounts struct {
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
	Cancelled int `json:"cancelled"`
	Waiting   int `json:"waiting"`
	Active    int `json:"active"`
}

// FailureRate is failed / (succeeded + failed), or -1 when neither happened.
func (c StepCounts) FailureRate() float64 {
	finished := c.Succeeded + c.Failed
	if finished == 0 {
		return -1
	}
	return float64(c.Failed) / float64(finished)
}

// ErrorGroup is one error message and the runs that hit it.
type ErrorGroup struct {
	Message string
	Count   int
	// Last is the most recent run with this error; Stderr is the last line
	// of that run's stderr, often more telling than an exit code.
	Last   domain.RunKey
	LastAt time.Time
	Stderr string
}

const (
	maxErrorGroups = 5
	maxErrorText   = 300
)

// Steps summarises every step of the runs of the last days days, for one
// workflow or (empty workflow) all of them. Steps are ordered by workflow,
// then failures first, then by time spent.
func (q Query) Steps(ctx context.Context, workflow string, days int) ([]StepStat, error) {
	runs, _, err := q.Store.List(ctx, RunFilter{Workflow: workflow, Since: startOfDay(q.now()).AddDate(0, 0, -(days - 1)), WithSteps: true})
	if err != nil {
		return nil, err
	}
	type key struct{ workflow, step string }
	stats := map[key]*StepStat{}
	durations, waits := map[key][]int64{}, map[key][]int64{}
	groups := map[key]map[string]*ErrorGroup{}
	for _, run := range runs { // newest first
		for _, step := range run.Steps {
			k := key{run.Workflow, step.ID}
			stat := stats[k]
			if stat == nil {
				stat = &StepStat{Workflow: run.Workflow, Step: step.ID, Type: step.Type, Provider: step.Provider, LastAt: run.When()}
				stats[k], groups[k] = stat, map[string]*ErrorGroup{}
			}
			stat.Runs++
			stat.Counts.add(step.Status)
			if step.Attempts > 1 {
				stat.Retried++
			}
			if step.Status.Finished() && step.DurationMs > 0 {
				durations[k] = append(durations[k], step.DurationMs-step.WaitingMs)
			}
			if step.WaitingMs > 0 {
				waits[k] = append(waits[k], step.WaitingMs)
			}
			if step.Status == domain.StatusFailed {
				message := errorText(step)
				group := groups[k][message]
				if group == nil {
					group = &ErrorGroup{Message: message, Last: run.Key, LastAt: run.When(), Stderr: lastLine(step.StderrTail)}
					groups[k][message] = group
				}
				group.Count++
			}
		}
	}
	out := make([]StepStat, 0, len(stats))
	for k, stat := range stats {
		stat.P50Ms, stat.P95Ms = percentile(durations[k], 50), percentile(durations[k], 95)
		stat.WaitP50Ms = percentile(waits[k], 50)
		for _, group := range groups[k] {
			stat.Errors = append(stat.Errors, *group)
		}
		sort.Slice(stat.Errors, func(i, j int) bool {
			if stat.Errors[i].Count != stat.Errors[j].Count {
				return stat.Errors[i].Count > stat.Errors[j].Count
			}
			return stat.Errors[i].LastAt.After(stat.Errors[j].LastAt)
		})
		if len(stat.Errors) > maxErrorGroups {
			stat.Errors = stat.Errors[:maxErrorGroups]
		}
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Workflow != b.Workflow {
			return a.Workflow < b.Workflow
		}
		if a.Counts.Failed != b.Counts.Failed {
			return a.Counts.Failed > b.Counts.Failed
		}
		if a.P95Ms != b.P95Ms {
			return a.P95Ms > b.P95Ms
		}
		return a.Step < b.Step
	})
	return out, nil
}

func (c *StepCounts) add(status domain.Status) {
	switch status {
	case domain.StatusSucceeded:
		c.Succeeded++
	case domain.StatusFailed:
		c.Failed++
	case domain.StatusSkipped:
		c.Skipped++
	case domain.StatusCancelled:
		c.Cancelled++
	case domain.StatusWaiting, domain.StatusWaitingInput, domain.StatusWaitingResource:
		c.Waiting++
	default:
		c.Active++
	}
}

// errorText is the first line of a step's error, or of the end of its
// stderr when the step did not say why it failed.
func errorText(step domain.Step) string {
	text := strings.TrimSpace(step.Error)
	if text == "" {
		text = lastLine(step.StderrTail)
	}
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	text = clip(text)
	if text == "" {
		return "(no error message)"
	}
	return text
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return clip(strings.TrimSpace(lines[len(lines)-1]))
}

func clip(text string) string {
	if runes := []rune(text); len(runes) > maxErrorText {
		text = string(runes[:maxErrorText]) + "…"
	}
	return text
}
