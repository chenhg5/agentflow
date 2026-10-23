package graph

import (
	"fmt"
	"strings"

	"github.com/chenhg5/agentflow/internal/condition"
	"github.com/chenhg5/agentflow/internal/model"
)

const End = "$end"

// TransitionKey is stable across restarts and unique after validation.
func TransitionKey(edge model.Transition) string {
	return edge.From + "\x00" + edge.When + "\x00" + edge.To
}

func validateTransitions(workflow model.Workflow) []string {
	errors := []string{}
	byID := map[string]model.Node{}
	for _, node := range workflow.Nodes {
		byID[node.ID] = node
	}
	if _, ok := byID[workflow.Entry]; !ok {
		errors = append(errors, "entry must name a node")
	}
	if len(workflow.Edges) == 0 {
		return append(errors, "version 2 requires edges")
	}
	outgoing := map[string][]model.Transition{}
	seen := map[string]bool{}
	for _, edge := range workflow.Edges {
		if _, ok := byID[edge.From]; !ok {
			errors = append(errors, fmt.Sprintf("edge source %q is not a node", edge.From))
			continue
		}
		if edge.To != End {
			if _, ok := byID[edge.To]; !ok {
				errors = append(errors, fmt.Sprintf("edge target %q is not a node or $end", edge.To))
				continue
			}
		}
		if edge.MaxIterations < 0 {
			errors = append(errors, fmt.Sprintf("edge %s -> %s maxIterations must be positive", edge.From, edge.To))
		}
		if strings.TrimSpace(edge.When) != edge.When {
			errors = append(errors, fmt.Sprintf("edge %s -> %s when must not have surrounding spaces", edge.From, edge.To))
		}
		if err := condition.Validate(edge.When); err != nil {
			errors = append(errors, fmt.Sprintf("edge %s -> %s has invalid condition: %v", edge.From, edge.To, err))
		}
		key := edge.From + "\x00" + edge.When
		if seen[key] {
			errors = append(errors, fmt.Sprintf("node %s has duplicate edge condition %q", edge.From, edge.When))
		}
		seen[key] = true
		outgoing[edge.From] = append(outgoing[edge.From], edge)
	}
	for _, node := range workflow.Nodes {
		edges := outgoing[node.ID]
		if len(edges) == 0 {
			errors = append(errors, fmt.Sprintf("node %s needs an outgoing edge, possibly to $end", node.ID))
		}
		for index, edge := range edges {
			if edge.When == "" && index != len(edges)-1 {
				errors = append(errors, fmt.Sprintf("node %s default edge must be last", node.ID))
			}
		}
	}
	// A cycle must contain at least one bounded edge. Removing bounded edges
	// must leave a DAG, so a run cannot loop forever without consuming a limit.
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
		for _, edge := range outgoing[id] {
			if edge.To != End && edge.MaxIterations == 0 && visit(edge.To) {
				return true
			}
		}
		delete(visiting, id)
		visited[id] = true
		return false
	}
	for _, node := range workflow.Nodes {
		if visit(node.ID) {
			errors = append(errors, "every cycle needs a maxIterations edge")
			break
		}
	}
	reachable := map[string]bool{}
	var walk func(string)
	walk = func(id string) {
		if reachable[id] {
			return
		}
		reachable[id] = true
		for _, edge := range outgoing[id] {
			if edge.To != End {
				walk(edge.To)
			}
		}
	}
	if _, ok := byID[workflow.Entry]; ok {
		walk(workflow.Entry)
	}
	for _, node := range workflow.Nodes {
		if !reachable[node.ID] {
			errors = append(errors, fmt.Sprintf("node %s is unreachable from entry", node.ID))
		}
	}
	return errors
}
