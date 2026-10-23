package schema

import (
	"fmt"
	"github.com/chenhg5/agentflow/internal/template"
	"strings"

	"github.com/chenhg5/agentflow/internal/graph"
	"github.com/chenhg5/agentflow/internal/model"
)

func Validate(workflow model.Workflow) []string {
	errors := []string{}
	if workflow.Version != "2" {
		errors = append(errors, `version must be "2"`)
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
		if node.Executor == "" {
			errors = append(errors, fmt.Sprintf("node %s needs an executor", node.ID))
		}
		errors = append(errors, ValidateDefinition(fmt.Sprintf("node %s inputSchema", node.ID), node.InputSchema)...)
		errors = append(errors, ValidateDefinition(fmt.Sprintf("node %s outputSchema", node.ID), node.OutputSchema)...)
	}
	for _, node := range workflow.Nodes {
		for _, reference := range template.References(node.Input) {
			parts := strings.Split(reference, ".")
			valid := reference == "inputs" || reference == "nodes"
			if len(parts) >= 2 && parts[0] == "inputs" {
				_, valid = workflow.Inputs[parts[1]]
			}
			if len(parts) == 2 && parts[0] == "nodes" && ids[parts[1]] {
				valid = true
			}
			if len(parts) >= 3 && parts[0] == "nodes" && ids[parts[1]] {
				valid = parts[2] == "result" || parts[2] == "input" || parts[2] == "status" || parts[2] == "artifacts"
			}
			if !valid {
				errors = append(errors, fmt.Sprintf("node %s has unknown input reference %q; use inputs.<declared-name> or nodes.<node-id>.result/input/status/artifacts", node.ID, reference))
			}
		}
	}
	errors = append(errors, graph.Validate(workflow)...)
	return errors
}
