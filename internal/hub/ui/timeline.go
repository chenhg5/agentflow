package ui

import (
	"strconv"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/domain"
)

// timelineRow places one step on the run's time axis, in percent.
type timelineRow struct {
	Step  domain.Step
	Left  float64
	Width float64
	Shown bool
}

// timeline lays steps out on one axis from the run's start to its end (or now,
// while it is still going), like the run page of the Loops console.
func timeline(run domain.Run, now time.Time) []timelineRow {
	start, end := run.StartedAt, run.FinishedAt
	for _, step := range run.Steps {
		if !step.StartedAt.IsZero() && (start.IsZero() || step.StartedAt.Before(start)) {
			start = step.StartedAt
		}
		if step.FinishedAt.After(end) {
			end = step.FinishedAt
		}
	}
	if !run.Status.Finished() && now.After(end) {
		end = now
	}
	span := end.Sub(start)
	rows := make([]timelineRow, len(run.Steps))
	for index, step := range run.Steps {
		rows[index] = timelineRow{Step: step}
		if step.StartedAt.IsZero() || start.IsZero() || span <= 0 {
			continue
		}
		stepEnd := step.FinishedAt
		if stepEnd.IsZero() {
			stepEnd = end
		}
		left := float64(step.StartedAt.Sub(start)) / float64(span) * 100
		width := float64(stepEnd.Sub(step.StartedAt)) / float64(span) * 100
		rows[index].Left = clamp(left, 0, 99.4)
		rows[index].Width = clamp(width, 0.6, 100-rows[index].Left)
		rows[index].Shown = true
	}
	return rows
}

func clamp(value, low, high float64) float64 { return min(max(value, low), high) }

func itoa(value int) string { return strconv.Itoa(value) }
