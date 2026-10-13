package trigger

import (
	"encoding/json"
	"fmt"
	"strings"
)

func ResolveInputs(definition Definition, event Event) (map[string]string, error) {
	inputs := map[string]string{}
	for key, value := range definition.Inputs {
		inputs[key] = value
	}
	var payload any
	if len(event.Payload) > 0 {
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return nil, fmt.Errorf("event payload is invalid JSON: %w", err)
		}
	}
	for name, binding := range definition.Bindings {
		value, ok := resolveBinding(binding, payload, event.Headers, event.Attributes)
		if !ok {
			return nil, fmt.Errorf("binding %s did not resolve", name)
		}
		if !isScalar(value) {
			return nil, fmt.Errorf("binding %s must resolve to a scalar value", name)
		}
		inputs[name] = fmt.Sprint(value)
	}
	return inputs, nil
}

func resolveBinding(binding Binding, payload any, headers, attributes map[string]string) (any, bool) {
	switch strings.ToLower(binding.From) {
	case "json", "payload":
		return lookupJSONPath(payload, binding.Path)
	case "header", "headers":
		value, ok := headers[binding.Name]
		return value, ok
	case "attribute", "attributes":
		value, ok := attributes[binding.Name]
		return value, ok
	default:
		return nil, false
	}
}

func lookupJSONPath(value any, path string) (any, bool) {
	path = strings.TrimPrefix(strings.TrimSpace(path), "$")
	path = strings.TrimPrefix(path, ".")
	if path == "" {
		return value, true
	}
	for _, key := range strings.Split(path, ".") {
		switch current := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = current[key]
			if !ok {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return value, true
}

func isScalar(value any) bool {
	switch value.(type) {
	case nil, map[string]any, []any:
		return false
	default:
		return true
	}
}
