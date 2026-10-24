package schema

import (
	"fmt"
	"strings"

	"github.com/chenhg5/agentflow/internal/graph"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/template"
)

func Validate(workflow model.Workflow) []string {
	errors := []string{}
	if workflow.Version != "3" {
		errors = append(errors, `version must be "3"`)
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
		errors = append(errors, validateExecution(node)...)
	}
	for _, node := range workflow.Nodes {
		if from := node.Execution.Session.From; from != "" && !ids[from] {
			errors = append(errors, fmt.Sprintf("node %s execution.session.from %q is not a node", node.ID, from))
		}
		for _, reference := range template.References(node.Config) {
			if !validReference(workflow, ids, reference, false) {
				errors = append(errors, fmt.Sprintf("node %s has unknown config reference %q; use input.<field>, inputs.<declared-name>, or nodes.<node-id>.result/input/status/sessionId/artifacts", node.ID, reference))
			}
		}
		for _, reference := range template.References(node.Execution.Session.ID) {
			parts := strings.Split(reference, ".")
			if len(parts) != 2 || parts[0] != "inputs" {
				errors = append(errors, fmt.Sprintf("node %s fixed session id may only reference a declared workflow input", node.ID))
				continue
			}
			if _, ok := workflow.Inputs[parts[1]]; !ok {
				errors = append(errors, fmt.Sprintf("node %s fixed session id references undeclared input %q", node.ID, parts[1]))
			}
		}
	}
	for _, edge := range workflow.Edges {
		if edge.Map == nil {
			continue
		}
		for _, reference := range template.References(*edge.Map) {
			if !validReference(workflow, ids, reference, true) {
				errors = append(errors, fmt.Sprintf("edge %s -> %s has unknown map reference %q; use input.<field>, output.<field>, inputs.<declared-name>, or nodes.<node-id>.result/input/status/sessionId/artifacts", edge.From, edge.To, reference))
			}
		}
	}
	errors = append(errors, graph.Validate(workflow)...)
	return errors
}

func validReference(workflow model.Workflow, ids map[string]bool, reference string, allowOutput bool) bool {
	parts := strings.Split(reference, ".")
	if reference == "input" || reference == "inputs" || reference == "nodes" {
		return true
	}
	if allowOutput && (reference == "output" || len(parts) >= 2 && parts[0] == "output") {
		return true
	}
	if len(parts) >= 2 && parts[0] == "input" {
		return true
	}
	if len(parts) >= 2 && parts[0] == "inputs" {
		_, ok := workflow.Inputs[parts[1]]
		return ok
	}
	if len(parts) == 2 && parts[0] == "nodes" && ids[parts[1]] {
		return true
	}
	if len(parts) >= 3 && parts[0] == "nodes" && ids[parts[1]] {
		return parts[2] == "result" || parts[2] == "input" || parts[2] == "status" || parts[2] == "sessionId" || parts[2] == "artifacts"
	}
	return false
}

func validateExecution(node model.Node) []string {
	spec := node.Execution.Session
	mode := spec.Mode
	if mode == "" {
		mode = "new"
	}
	valid := map[string]bool{"new": true, "fixed": true, "inherit": true, "fork": true}
	if !valid[mode] {
		return []string{fmt.Sprintf("node %s has unsupported execution.session.mode %q", node.ID, mode)}
	}
	if mode == "new" && (spec.ID != "" || spec.From != "") {
		return []string{fmt.Sprintf("node %s new session cannot define execution.session.id or from", node.ID)}
	}
	if (mode == "inherit" || mode == "fork") && spec.ID != "" {
		return []string{fmt.Sprintf("node %s session mode %s cannot define execution.session.id", node.ID, mode)}
	}
	return nil
}
