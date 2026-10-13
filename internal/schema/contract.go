package schema

import (
	"fmt"
	"math"
	"reflect"
	"strings"
)

var contractTypes = map[string]bool{"object": true, "array": true, "string": true, "number": true, "integer": true, "boolean": true, "null": true}

// ValidateDefinition checks the shape of a contract before a workflow is
// executed. Runtime values are checked separately by ValidateValue.
func ValidateDefinition(name string, definition map[string]any) []string {
	if len(definition) == 0 {
		return nil
	}
	errors := []string{}
	if expected, ok := definition["type"].(string); ok && expected != "" && !contractTypes[expected] {
		errors = append(errors, fmt.Sprintf("%s has unsupported contract type %q", name, expected))
	}
	if properties, ok := definition["properties"].(map[string]any); ok {
		for key, child := range properties {
			childDefinition, ok := child.(map[string]any)
			if !ok {
				errors = append(errors, fmt.Sprintf("%s.properties.%s must be an object", name, key))
				continue
			}
			errors = append(errors, ValidateDefinition(name+".properties."+key, childDefinition)...)
		}
	}
	if items, ok := definition["items"].(map[string]any); ok {
		errors = append(errors, ValidateDefinition(name+".items", items)...)
	}
	return errors
}

// ValidateValue validates a runtime value against the intentionally small
// JSON-Schema subset supported by workflow node contracts. Keeping this local
// avoids a dependency on a full schema engine while making contracts useful at
// execution time.
func ValidateValue(name string, value any, definition map[string]any) []string {
	if len(definition) == 0 {
		return nil
	}
	errors := validateValue(name, value, definition)
	return errors
}

func validateValue(path string, value any, definition map[string]any) []string {
	errors := []string{}
	if enum, ok := definition["enum"].([]any); ok {
		matched := false
		for _, candidate := range enum {
			if reflect.DeepEqual(candidate, value) || fmt.Sprint(candidate) == fmt.Sprint(value) {
				matched = true
				break
			}
		}
		if !matched {
			errors = append(errors, fmt.Sprintf("%s must be one of the declared enum values", path))
		}
	}
	if value == nil {
		if definition["type"] != "null" && definition["nullable"] != true {
			errors = append(errors, fmt.Sprintf("%s is required", path))
		}
		return errors
	}
	if expected, ok := definition["type"].(string); ok && expected != "" && !matchesType(value, expected) {
		errors = append(errors, fmt.Sprintf("%s must be %s", path, expected))
		return errors
	}

	switch object := value.(type) {
	case map[string]any:
		if required, ok := definition["required"].([]any); ok {
			for _, item := range required {
				key, ok := item.(string)
				if !ok {
					continue
				}
				if _, exists := object[key]; !exists {
					errors = append(errors, fmt.Sprintf("%s.%s is required", path, key))
				}
			}
		}
		properties, _ := definition["properties"].(map[string]any)
		for key, child := range properties {
			childDefinition, ok := child.(map[string]any)
			if !ok {
				continue
			}
			if childValue, exists := object[key]; exists {
				errors = append(errors, validateValue(path+"."+key, childValue, childDefinition)...)
			}
		}
		if additional, ok := definition["additionalProperties"].(bool); ok && !additional {
			for key := range object {
				if _, known := properties[key]; !known {
					errors = append(errors, fmt.Sprintf("%s.%s is not allowed", path, key))
				}
			}
		}
	case []any:
		if items, ok := definition["items"].(map[string]any); ok {
			for index, child := range object {
				errors = append(errors, validateValue(fmt.Sprintf("%s[%d]", path, index), child, items)...)
			}
		}
	}
	if minimum, ok := number(definition["minimum"]); ok {
		if actual, ok := number(value); ok && actual < minimum {
			errors = append(errors, fmt.Sprintf("%s must be at least %v", path, minimum))
		}
	}
	if minLength, ok := number(definition["minLength"]); ok {
		if text, ok := value.(string); ok && float64(len([]rune(text))) < minLength {
			errors = append(errors, fmt.Sprintf("%s must contain at least %v characters", path, minLength))
		}
	}
	return errors
}

func matchesType(value any, expected string) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := number(value)
		return ok
	case "integer":
		actual, ok := number(value)
		return ok && math.Trunc(actual) == actual
	case "null":
		return value == nil
	default:
		return true
	}
}

func number(value any) (float64, bool) {
	switch current := value.(type) {
	case float64:
		return current, true
	case float32:
		return float64(current), true
	case int:
		return float64(current), true
	case int64:
		return float64(current), true
	case int32:
		return float64(current), true
	case jsonNumber:
		actual, err := current.Float64()
		return actual, err == nil
	default:
		return 0, false
	}
}

// jsonNumber is kept as a tiny interface so this package does not need to
// expose encoding/json's concrete number type in the public API.
type jsonNumber interface {
	Float64() (float64, error)
}

func FormatContractErrors(prefix string, errors []string) string {
	if len(errors) == 0 {
		return ""
	}
	if prefix == "" {
		return strings.Join(errors, "; ")
	}
	return prefix + ": " + strings.Join(errors, "; ")
}
