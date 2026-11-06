package ui

import (
	"encoding/json"
	"strings"

	"github.com/chenhg5/agentflow/internal/hub/domain"
)

// flowNode and flowEdge are what static/flow.js hands to React Flow. The
// layout is computed here, the same way `agentflow web` lays out a run:
// columns by distance from the entry (loop edges ignored), rows in order.
type flowNode struct {
	ID       string   `json:"id"`
	Executor string   `json:"executor"`
	Status   string   `json:"status"`
	Label    string   `json:"label"`
	Tone     string   `json:"tone"`
	Duration string   `json:"duration,omitempty"`
	Attempts int      `json:"attempts,omitempty"`
	Badges   []string `json:"badges,omitempty"`
	X        int      `json:"x"`
	Y        int      `json:"y"`
	End      bool     `json:"end,omitempty"`
}

type flowEdge struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Label    string `json:"label,omitempty"`
	Kind     string `json:"kind"` // plain, condition, parallel, loop
	Animated bool   `json:"animated,omitempty"`
}

type flowData struct {
	Nodes []flowNode `json:"nodes"`
	Edges []flowEdge `json:"edges"`
}

// flowJSON lays out run's graph, or returns "" when the run has none.
func flowJSON(run domain.Run) string {
	if run.Graph == nil || len(run.Graph.Nodes) == 0 {
		return ""
	}
	data, _ := json.Marshal(layoutFlow(run))
	return string(data)
}

func layoutFlow(run domain.Run) flowData {
	graph := *run.Graph
	steps := map[string]domain.Step{}
	for _, step := range run.Steps {
		steps[step.ID] = step
	}
	nodes := append([]domain.GraphNode(nil), graph.Nodes...)
	for _, edge := range graph.Edges {
		if edge.Target == domain.EndNode {
			nodes = append(nodes, domain.GraphNode{ID: domain.EndNode})
			break
		}
	}
	levels := levelsFrom(graph, nodes)
	rows := map[int]int{}
	out := flowData{}
	for _, node := range nodes {
		level := levels[node.ID]
		row := rows[level]
		rows[level] = row + 1
		item := flowNode{ID: node.ID, X: level*250 + 40, Y: row*120 + 40}
		if node.ID == domain.EndNode {
			item.End, item.Label, item.Executor = true, "结束", "end"
			item.Status = "pending"
			if run.Status == domain.StatusSucceeded {
				item.Status = string(domain.StatusSucceeded)
			}
		} else {
			step, ran := steps[node.ID]
			item.Status = string(domain.StatusPending)
			item.Executor = strings.Join(node.Executors, " → ")
			if ran {
				item.Status = string(step.Status)
				if step.Type != "" && step.Status != domain.StatusPending {
					item.Executor = step.Type
				}
				if step.DurationMs > 0 {
					item.Duration = duration(step.DurationMs)
				}
				item.Attempts = step.Attempts
			}
			if node.Parallel {
				item.Badges = append(item.Badges, "并行发散")
			}
			if len(node.Join) > 0 {
				item.Badges = append(item.Badges, "汇合")
			}
		}
		look := lookOf(domain.Status(item.Status))
		item.Label, item.Tone = look.Label, look.Tone
		if item.End {
			item.Label = "结束"
		}
		out.Nodes = append(out.Nodes, item)
	}
	for index, edge := range graph.Edges {
		item := flowEdge{ID: "e" + itoa(index), Source: edge.Source, Target: edge.Target, Kind: "plain"}
		switch {
		case edge.Condition != "":
			item.Kind, item.Label = "condition", edge.Condition
		case edge.Parallel:
			item.Kind, item.Label = "parallel", "⇉"
		}
		if edge.MaxIterations > 0 {
			if item.Kind == "plain" {
				item.Kind = "loop"
			}
			loop := "循环 ≤ " + itoa(edge.MaxIterations)
			if item.Label == "" {
				item.Label = loop
			} else {
				item.Label += " · " + loop
			}
		}
		if target, ok := steps[edge.Target]; ok && target.Status == domain.StatusRunning {
			item.Animated = true
		}
		out.Edges = append(out.Edges, item)
	}
	return out
}

// levelsFrom places each node in the column of its breadth-first distance from
// the entry, following every edge. Loop edges then point back to an earlier
// column instead of dragging a chain into one column, which matters because
// v0.4 workflows often put maxIterations on most edges of a cycle. Nodes the
// entry cannot reach go one column past the last.
func levelsFrom(graph domain.Graph, nodes []domain.GraphNode) map[string]int {
	next := map[string][]string{}
	for _, edge := range graph.Edges {
		next[edge.Source] = append(next[edge.Source], edge.Target)
	}
	entry := graph.Entry
	if entry == "" && len(nodes) > 0 {
		entry = nodes[0].ID
	}
	levels := map[string]int{entry: 0}
	queue := []string{entry}
	deepest := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, target := range next[id] {
			if _, seen := levels[target]; !seen {
				levels[target] = levels[id] + 1
				deepest = max(deepest, levels[target])
				queue = append(queue, target)
			}
		}
	}
	for _, node := range nodes {
		if _, seen := levels[node.ID]; !seen {
			levels[node.ID] = deepest + 1
		}
	}
	if _, ok := levels[domain.EndNode]; ok {
		levels[domain.EndNode] = deepest + 1 // the end always sits at the far right
		for _, node := range nodes {
			if node.ID != domain.EndNode && levels[node.ID] > deepest {
				levels[domain.EndNode] = levels[node.ID] + 1
			}
		}
	}
	return levels
}
