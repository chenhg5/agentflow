package ui

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/hub/app"
	"github.com/chenhg5/agentflow/internal/hub/domain"
)

func TestDuration(t *testing.T) {
	for ms, want := range map[int64]string{0: "—", 850: "850ms", 12300: "12.3s", 5000: "5s", 245000: "4m 05s", 4320000: "1h 12m", 183600000: "2d 3h"} {
		if got := duration(ms); got != want {
			t.Errorf("duration(%d) = %q, want %q", ms, got, want)
		}
	}
}

func TestRelative(t *testing.T) {
	now := time.Date(2026, 11, 6, 15, 0, 0, 0, time.Local)
	for offset, want := range map[time.Duration]string{
		10 * time.Second: "刚刚", 5 * time.Minute: "5 分钟前", 3 * time.Hour: "3 小时前",
		20 * time.Hour: "昨天 19:00", 72 * time.Hour: "11-03 15:00", 400 * 24 * time.Hour: "2025-10-02",
	} {
		if got := relative(now.Add(-offset), now); got != want {
			t.Errorf("relative(-%v) = %q, want %q", offset, got, want)
		}
	}
}

func TestTimeline(t *testing.T) {
	start := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	run := domain.Run{Status: domain.StatusSucceeded, StartedAt: start, FinishedAt: start.Add(100 * time.Second), Steps: []domain.Step{
		{ID: "a", StartedAt: start, FinishedAt: start.Add(25 * time.Second)},
		{ID: "b", StartedAt: start.Add(25 * time.Second), FinishedAt: start.Add(100 * time.Second)},
		{ID: "c"},
	}}
	rows := timeline(run, start.Add(time.Hour))
	if !rows[0].Shown || rows[0].Left != 0 || rows[0].Width != 25 || rows[1].Left != 25 || rows[1].Width != 75 || rows[2].Shown {
		t.Fatalf("rows = %+v", rows)
	}
	running := domain.Run{Status: domain.StatusRunning, StartedAt: start, Steps: []domain.Step{{ID: "a", StartedAt: start.Add(50 * time.Second)}}}
	rows = timeline(running, start.Add(100*time.Second))
	if rows[0].Left != 50 || rows[0].Width != 50 {
		t.Fatalf("a running step should reach now: %+v", rows[0])
	}
}

func TestRunsChart(t *testing.T) {
	if runsChart(nil) != "" {
		t.Fatal("no days, no chart")
	}
	day := time.Date(2026, 11, 6, 0, 0, 0, 0, time.Local)
	svg := string(runsChart([]app.Day{{Date: day, Counts: app.Counts{Total: 3, Succeeded: 2, Failed: 1}}}))
	if !strings.Contains(svg, "11-06：成功 2 · 失败 1 · 其他 0") || !strings.Contains(svg, `class="good"`) || !strings.Contains(svg, `class="bad"`) {
		t.Fatalf("chart = %s", svg)
	}
	for peak, want := range map[int]int{0: 4, 3: 4, 7: 8, 9: 12, 37: 40, 130: 200} {
		if got := niceCeiling(peak); got != want {
			t.Errorf("niceCeiling(%d) = %d, want %d", peak, got, want)
		}
	}
}

func TestRunsHrefKeepsEveryFilter(t *testing.T) {
	current := url.Values{"project": {"demo"}, "since": {"2026-11-01"}, "status": {"failed"}, "offset": {"50"}}
	if got := runsHref(current, "offset", "100"); got != "/runs?offset=100&project=demo&since=2026-11-01&status=failed" {
		t.Errorf("next = %s", got)
	}
	if got := runsHref(current, "status", "", "offset", ""); got != "/runs?project=demo&since=2026-11-01" {
		t.Errorf("tab = %s", got)
	}
	if current.Get("offset") != "50" {
		t.Error("runsHref must not change the current query")
	}
}
