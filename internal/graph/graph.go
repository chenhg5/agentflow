// Package graph contains the workflow graph view used by validation and the
// visualization layer. The execution engine still uses the workflow's
// dependency list as its source of truth; this package makes the implicit
// control and data flow explicit for inspection and diagnostics.
package graph

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chenhg5/agentflow/internal/model"
)

const (
	ChannelControl   = "control"
	ChannelData      = "data"
	ChannelCondition = "condition"
)

// Edge is a normalized visual edge. A pair of nodes can carry more than one
// channel, for example control ordering and structured data handoff.
type Edge struct {
	ID        string            `json:"id"`
	Source    string            `json:"source"`
	Target    string            `json:"target"`
	Channels  []string          `json:"channels"`
	Condition string            `json:"condition,omitempty"`
	Mapping   map[string]string `json:"mapping,omitempty"`
}

type Document struct {
	Edges []Edge `json:"edges"`
}

// Describe converts the compact workflow representation into a graph view.
// It deliberately keeps the existing workflow JSON format stable.
func Describe(workflow model.Workflow) Document {
	edges := map[string]*Edge{}
	order := []string{}
	ensure := func(source, target string) *Edge {
		key := source + "\x00" + target
		if edge, ok := edges[key]; ok {
			return edge
		}
		edge := &Edge{ID: source + "->" + target, Source: source, Target: target, Channels: []string{}}
		edges[key] = edge
		order = append(order, key)
		return edge
	}
	addChannel := func(edge *Edge, channel string) {
		for _, existing := range edge.Channels {
			if existing == channel {
				return
			}
		}
		edge.Channels = append(edge.Channels, channel)
		sort.Strings(edge.Channels)
	}

	for _, node := range workflow.Nodes {
		for _, dependency := range node.Needs {
			edge := ensure(dependency, node.ID)
			addChannel(edge, ChannelControl)
			if strings.TrimSpace(node.When) != "" {
				addChannel(edge, ChannelCondition)
				edge.Condition = node.When
			}
		}
		targets := make([]string, 0, len(node.InputMapping))
		for target := range node.InputMapping {
			targets = append(targets, target)
		}
		sort.Strings(targets)
		for _, target := range targets {
			expression := node.InputMapping[target]
			if source, _, ok := ParseNodeOutput(expression); ok {
				edge := ensure(source, node.ID)
				addChannel(edge, ChannelData)
				if edge.Mapping == nil {
					edge.Mapping = map[string]string{}
				}
				edge.Mapping[target] = expression
			}
		}
	}
	result := make([]Edge, 0, len(order))
	for _, key := range order {
		result = append(result, *edges[key])
	}
	return Document{Edges: result}
}

// ParseNodeOutput parses AgentFlow's explicit node output reference syntax.
func ParseNodeOutput(expression string) (nodeID, field string, ok bool) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(expression), "$node."), ".")
	if !strings.HasPrefix(strings.TrimSpace(expression), "$node.") || len(parts) < 3 || parts[1] != "output" {
		return "", "", false
	}
	return parts[0], strings.Join(parts[2:], "."), parts[0] != "" && strings.Join(parts[2:], ".") != ""
}

// Validate checks graph-specific invariants that are easy to miss when a
// workflow is authored by hand. It returns errors rather than warnings because
// an undeclared data dependency would otherwise race the deterministic runner.
func Validate(workflow model.Workflow) []string {
	errors := []string{}
	ids := map[string]bool{}
	for _, node := range workflow.Nodes {
		ids[node.ID] = true
	}
	for _, node := range workflow.Nodes {
		seen := map[string]bool{}
		for _, dependency := range node.Needs {
			if dependency == node.ID {
				errors = append(errors, fmt.Sprintf("node %s cannot depend on itself", node.ID))
			}
			if seen[dependency] {
				errors = append(errors, fmt.Sprintf("node %s contains duplicate dependency: %s", node.ID, dependency))
			}
			seen[dependency] = true
		}
		for target, expression := range node.InputMapping {
			if strings.TrimSpace(target) == "" || strings.TrimSpace(expression) == "" {
				continue
			}
			if source, _, ok := ParseNodeOutput(expression); ok {
				if !ids[source] {
					errors = append(errors, fmt.Sprintf("node %s inputMapping.%s references missing node: %s", node.ID, target, source))
					continue
				}
				if !seen[source] {
					errors = append(errors, fmt.Sprintf("node %s inputMapping.%s references %s without declaring it in needs", node.ID, target, source))
				}
			}
		}
	}
	if hasCycle(workflow) {
		errors = append(errors, "workflow contains a dependency cycle")
	}
	return errors
}

func hasCycle(workflow model.Workflow) bool {
	byID := map[string]model.Node{}
	for _, node := range workflow.Nodes {
		byID[node.ID] = node
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return true
		}
		if visited[id] {
			return false
		}
		visiting[id] = true
		for _, dependency := range byID[id].Needs {
			if visit(dependency) {
				return true
			}
		}
		delete(visiting, id)
		visited[id] = true
		return false
	}
	for _, node := range workflow.Nodes {
		if visit(node.ID) {
			return true
		}
	}
	return false
}
