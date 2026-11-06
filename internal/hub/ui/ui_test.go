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

func TestFlowLayout(t *testing.T) {
	graph := domain.Graph{Entry: "a",
		Nodes: []domain.GraphNode{{ID: "a", Executors: []string{"process"}}, {ID: "b", Executors: []string{"codex", "claude-code"}, Parallel: true}, {ID: "c"}, {ID: "d"}, {ID: "orphan"}},
		Edges: []domain.GraphEdge{
			{Source: "a", Target: "b", MaxIterations: 3}, {Source: "b", Target: "c", Condition: "output.ok"},
			{Source: "b", Target: "d", Parallel: true}, {Source: "c", Target: "a", MaxIterations: 2}, {Source: "d", Target: domain.EndNode},
		}}
	run := domain.Run{Status: domain.StatusRunning, Graph: &graph, Steps: []domain.Step{
		{ID: "a", Type: "process", Status: domain.StatusSucceeded, DurationMs: 1500, Attempts: 1},
		{ID: "b", Type: "claude-code", Status: domain.StatusRunning, Attempts: 2},
	}}
	flow := layoutFlow(run)
	column := map[string]int{}
	byID := map[string]flowNode{}
	for _, node := range flow.Nodes {
		column[node.ID] = node.X
		byID[node.ID] = node
	}
	if !(column["a"] < column["b"] && column["b"] < column["c"] && column["c"] == column["d"]) {
		t.Fatalf("a chain whose edges all carry loops must still read left to right: %v", column)
	}
	if column[domain.EndNode] <= column["orphan"] || column["orphan"] <= column["d"] {
		t.Fatalf("unreachable nodes go past the last column and the end goes last: %v", column)
	}
	if byID["a"].Tone != "good" || byID["a"].Duration != "1.5s" || byID["b"].Executor != "claude-code" || byID["b"].Attempts != 2 || byID["c"].Executor != "" || byID["c"].Label != "待开始" {
		t.Fatalf("node states: %+v", byID)
	}
	if len(byID["b"].Badges) != 1 || !byID[domain.EndNode].End {
		t.Fatalf("badges / end: %+v %+v", byID["b"], byID[domain.EndNode])
	}
	kinds := map[string]string{}
	for _, edge := range flow.Edges {
		kinds[edge.Source+">"+edge.Target] = edge.Kind + ":" + edge.Label
		if edge.Target == "b" && !edge.Animated {
			t.Error("edges into a running node are animated")
		}
	}
	if kinds["a>b"] != "loop:循环 ≤ 3" || kinds["b>c"] != "condition:output.ok" || kinds["b>d"] != "parallel:⇉" {
		t.Fatalf("edge kinds: %v", kinds)
	}
	if flowJSON(domain.Run{}) != "" {
		t.Fatal("no graph, no canvas")
	}
}

func TestInputForm(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"decision"}, "properties": map[string]any{
		"decision": map[string]any{"type": "string", "enum": []any{"approve", "hold"}, "title": "决定"},
		"note":     map[string]any{"type": "string"},
		"retries":  map[string]any{"type": "integer"},
		"urgent":   map[string]any{"type": "boolean", "default": true},
	}}
	fields, raw := formFor(schema)
	if raw != "" || len(fields) != 4 || fields[0].Name != "decision" || fields[0].Kind != "choice" || fields[0].Label != "决定" || !fields[0].Required {
		t.Fatalf("fields = %+v raw %q", fields, raw)
	}
	kinds := map[string]string{}
	for _, field := range fields {
		kinds[field.Name] = field.Kind
	}
	if kinds["note"] != "textarea" || kinds["retries"] != "integer" || kinds["urgent"] != "bool" {
		t.Fatalf("kinds = %v", kinds)
	}
	output, err := outputFromForm(url.Values{"f.decision": {"hold"}, "f.retries": {"2"}, "f.urgent": {"true"}}, schema)
	if err != nil || output["decision"] != "hold" || output["retries"] != 2.0 || output["urgent"] != true || output["note"] != nil {
		t.Fatalf("output = %v, %v", output, err)
	}
	if out, _ := outputFromForm(url.Values{"f.decision": {"approve"}}, schema); out["urgent"] != false {
		t.Fatal("an unticked box is false")
	}
	for name, values := range map[string]url.Values{
		"missing required": {},
		"not an option":    {"f.decision": {"delete everything"}},
		"not an integer":   {"f.decision": {"hold"}, "f.retries": {"1.5"}},
		"bad json":         {"json": {"[1,2]"}},
	} {
		if _, err := outputFromForm(values, schema); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	nested := map[string]any{"type": "object", "properties": map[string]any{"items": map[string]any{"type": "array"}}}
	if fields, raw := formFor(nested); fields != nil || !strings.Contains(raw, `"items"`) {
		t.Fatalf("nested schema should be a JSON box: %v %q", fields, raw)
	}
	if _, raw := formFor(nil); raw != "{}" {
		t.Fatal("no schema: an empty JSON object to fill")
	}
	if out, err := outputFromForm(url.Values{"json": {`{"a":1}`}}, nil); err != nil || out["a"] != 1.0 {
		t.Fatalf("json box: %v %v", out, err)
	}
}
