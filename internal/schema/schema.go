package schema

import (
	"fmt"
	"github.com/chenhg5/agentflow/internal/model"
)

var allowedTypes = map[string]bool{"shell": true, "agent": true, "human": true}

func Validate(workflow model.Workflow) []string {
	errors := []string{}
	if workflow.Version != "1" {
		errors = append(errors, `version must be "1"`)
	}
	if workflow.Name == "" {
		errors = append(errors, "name is required")
	}
	if len(workflow.Nodes) == 0 {
		return append(errors, "nodes must be a non-empty array")
	}
	ids := map[string]bool{}
	for _, node := range workflow.Nodes {
		if node.ID == "" {
			errors = append(errors, "every node needs an id")
		} else if ids[node.ID] {
			errors = append(errors, fmt.Sprintf("duplicate node id: %s", node.ID))
		} else {
			ids[node.ID] = true
		}
		if !allowedTypes[node.Type] {
			errors = append(errors, fmt.Sprintf("node %s has unsupported type: %s", node.ID, node.Type))
		}
		if node.Type == "shell" && len(node.Command) == 0 {
			errors = append(errors, fmt.Sprintf("shell node %s needs a command", node.ID))
		}
		if node.Type == "agent" && node.Driver == "" {
			errors = append(errors, fmt.Sprintf("agent node %s needs a driver", node.ID))
		}
		if (node.Type == "agent" || node.Type == "human") && node.Prompt == "" {
			errors = append(errors, fmt.Sprintf("%s node %s needs a prompt", node.Type, node.ID))
		}
	}
	for _, node := range workflow.Nodes {
		for _, dependency := range node.Needs {
			if !ids[dependency] {
				errors = append(errors, fmt.Sprintf("node %s depends on missing node: %s", node.ID, dependency))
			}
		}
	}
	if len(errors) == 0 && hasCycle(workflow.Nodes) {
		errors = append(errors, "workflow contains a dependency cycle")
	}
	return errors
}

func hasCycle(nodes []model.Node) bool {
	byID := map[string]model.Node{}
	for _, node := range nodes {
		byID[node.ID] = node
	}
	visiting := map[string]bool{}
	visited := map[string]bool{}
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
	for _, node := range nodes {
		if visit(node.ID) {
			return true
		}
	}
	return false
}
