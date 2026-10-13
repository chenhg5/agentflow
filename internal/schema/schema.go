package schema

import (
	"fmt"
	"strings"

	"github.com/chenhg5/agentflow/internal/graph"
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
		if node.Type == "agent" && node.Provider == "" {
			errors = append(errors, fmt.Sprintf("agent node %s needs a provider", node.ID))
		}
		if node.Session != "" && node.Type != "agent" {
			errors = append(errors, fmt.Sprintf("session is only supported on agent node %s", node.ID))
		}
		if (node.Type == "agent" || node.Type == "human") && node.Prompt == "" {
			errors = append(errors, fmt.Sprintf("%s node %s needs a prompt", node.Type, node.ID))
		}
		errors = append(errors, validateFields(node.ID, "inputFields", node.InputFields)...)
		errors = append(errors, validateFields(node.ID, "outputFields", node.OutputFields)...)
		errors = append(errors, ValidateDefinition(fmt.Sprintf("node %s inputSchema", node.ID), EffectiveInputSchema(node))...)
		errors = append(errors, ValidateDefinition(fmt.Sprintf("node %s outputSchema", node.ID), EffectiveOutputSchema(node))...)
		errors = append(errors, validateFieldSchemaConsistency(node.ID, "inputFields", node.InputFields, node.InputSchema)...)
		errors = append(errors, validateFieldSchemaConsistency(node.ID, "outputFields", node.OutputFields, node.OutputSchema)...)
		if node.InputMapping != nil {
			for target, source := range node.InputMapping {
				if strings.TrimSpace(target) == "" {
					errors = append(errors, fmt.Sprintf("node %s inputMapping has an empty target field", node.ID))
				}
				if strings.TrimSpace(source) == "" {
					errors = append(errors, fmt.Sprintf("node %s inputMapping.%s has an empty source expression", node.ID, target))
				}
			}
		}
	}
	for _, node := range workflow.Nodes {
		for _, dependency := range node.Needs {
			if !ids[dependency] {
				errors = append(errors, fmt.Sprintf("node %s depends on missing node: %s", node.ID, dependency))
			}
		}
	}
	errors = append(errors, graph.Validate(workflow)...)
	return errors
}

func validateFields(nodeID, kind string, fields []model.FieldSpec) []string {
	errors := []string{}
	seen := map[string]bool{}
	for _, field := range fields {
		if field.Name == "" {
			errors = append(errors, fmt.Sprintf("node %s %s contains a field without a name", nodeID, kind))
			continue
		}
		if seen[field.Name] {
			errors = append(errors, fmt.Sprintf("node %s %s contains duplicate field: %s", nodeID, kind, field.Name))
		}
		seen[field.Name] = true
		if field.Type != "" && !fieldTypes[field.Type] {
			errors = append(errors, fmt.Sprintf("node %s %s.%s has unsupported type %q", nodeID, kind, field.Name, field.Type))
		}
	}
	return errors
}

func validateFieldSchemaConsistency(nodeID, kind string, fields []model.FieldSpec, definition map[string]any) []string {
	if len(fields) == 0 || len(definition) == 0 {
		return nil
	}
	properties, _ := definition["properties"].(map[string]any)
	if properties == nil {
		return []string{fmt.Sprintf("node %s %s require a schema with properties", nodeID, kind)}
	}
	required := map[string]bool{}
	if values, ok := definition["required"].([]any); ok {
		for _, value := range values {
			if name, ok := value.(string); ok {
				required[name] = true
			}
		}
	}
	errors := []string{}
	for _, field := range fields {
		if _, ok := properties[field.Name]; !ok {
			errors = append(errors, fmt.Sprintf("node %s %s field %s is missing from schema properties", nodeID, kind, field.Name))
		}
		if field.Required && !required[field.Name] {
			errors = append(errors, fmt.Sprintf("node %s %s field %s must be required in schema", nodeID, kind, field.Name))
		}
	}
	return errors
}
