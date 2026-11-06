package schema

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chenhg5/agentflow/internal/model"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func EffectiveInputSchema(node model.Node) map[string]any  { return node.InputSchema }
func EffectiveOutputSchema(node model.Node) map[string]any { return node.OutputSchema }

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
