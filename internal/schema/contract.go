package schema

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chenhg5/agentflow/internal/model"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

var fieldTypes = map[string]bool{"object": true, "array": true, "string": true, "number": true, "integer": true, "boolean": true, "null": true}

// SchemaForFields turns Multigent-style field declarations into a strict
// object contract. Field declarations remain useful metadata for agents and
// UI while the generated schema enforces the same requirements at runtime.
func SchemaForFields(fields []model.FieldSpec) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	properties := map[string]any{}
	required := []any{}
	for _, field := range fields {
		definition := map[string]any{"type": field.Type}
		if field.Type == "" {
			definition["type"] = "string"
		}
		if field.Description != "" {
			definition["description"] = field.Description
		}
		properties[field.Name] = definition
		if field.Required {
			required = append(required, field.Name)
		}
	}
	result := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}

func EffectiveInputSchema(node model.Node) map[string]any {
	if len(node.InputSchema) > 0 {
		return node.InputSchema
	}
	return SchemaForFields(node.InputFields)
}

func EffectiveOutputSchema(node model.Node) map[string]any {
	if len(node.OutputSchema) > 0 {
		return node.OutputSchema
	}
	return SchemaForFields(node.OutputFields)
}

// ValidateDefinition compiles the complete JSON Schema contract. Workflow
// validation and runtime validation therefore use the same implementation.
func ValidateDefinition(name string, definition map[string]any) []string {
	if len(definition) == 0 {
		return nil
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	location := "mem://agentflow/" + sanitizeLocation(name) + ".json"
	if err := compiler.AddResource(location, definition); err != nil {
		return []string{fmt.Sprintf("%s is not a valid JSON Schema: %v", name, err)}
	}
	if _, err := compiler.Compile(location); err != nil {
		return []string{fmt.Sprintf("%s is not a valid JSON Schema: %v", name, err)}
	}
	return nil
}

// ValidateValue validates a runtime value using JSON Schema draft 2020-12.
func ValidateValue(name string, value any, definition map[string]any) []string {
	if len(definition) == 0 {
		return nil
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	location := "mem://agentflow/runtime/" + sanitizeLocation(name) + ".json"
	if err := compiler.AddResource(location, definition); err != nil {
		return []string{fmt.Sprintf("%s schema is invalid: %v", name, err)}
	}
	compiled, err := compiler.Compile(location)
	if err != nil {
		return []string{fmt.Sprintf("%s schema is invalid: %v", name, err)}
	}
	if err := compiled.Validate(value); err != nil {
		if validation, ok := err.(*jsonschema.ValidationError); ok {
			var messages []string
			collectValidationErrors(validation, &messages)
			if len(messages) > 0 {
				return messages
			}
		}
		return []string{fmt.Sprintf("%s: %v", name, err)}
	}
	return nil
}

func collectValidationErrors(err *jsonschema.ValidationError, messages *[]string) {
	if len(err.Causes) == 0 {
		path := "/"
		if len(err.InstanceLocation) > 0 {
			path = "/" + strings.Join(err.InstanceLocation, "/")
		}
		*messages = append(*messages, fmt.Sprintf("%s: %s", path, err.Error()))
		return
	}
	for _, cause := range err.Causes {
		collectValidationErrors(cause, messages)
	}
}

func sanitizeLocation(value string) string {
	value = strings.NewReplacer("/", "_", " ", "_", ".", "_").Replace(value)
	if value == "" {
		return "contract"
	}
	return value
}

func FormatContractErrors(prefix string, errors []string) string {
	if len(errors) == 0 {
		return ""
	}
	ordered := append([]string(nil), errors...)
	sort.Strings(ordered)
	if prefix == "" {
		return strings.Join(ordered, "; ")
	}
	return prefix + ": " + strings.Join(ordered, "; ")
}
