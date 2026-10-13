package template

import (
	"fmt"
	"regexp"
	"strings"
)

var placeholder = regexp.MustCompile(`\{\{\s*([^}]+?)\s*\}\}`)

func Render(value string, context map[string]any) string {
	return placeholder.ReplaceAllStringFunc(value, func(match string) string {
		expression := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}"))
		value, ok := Lookup(context, expression)
		if !ok || value == nil {
			return ""
		}
		return fmt.Sprint(value)
	})
}

func RenderArray(values []string, context map[string]any) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = Render(value, context)
	}
	return result
}

// RenderValue renders a JSON-compatible value while preserving the type of an
// exact placeholder. For example, "{{ nodes.build.result.count }}" resolves to
// a number, while "count={{ nodes.build.result.count }}" remains a string.
func RenderValue(value any, context map[string]any) any {
	switch current := value.(type) {
	case string:
		match := placeholder.FindStringSubmatch(current)
		if match != nil && match[0] == current {
			if resolved, ok := Lookup(context, strings.TrimSpace(match[1])); ok {
				return resolved
			}
			return nil
		}
		return Render(current, context)
	case map[string]any:
		result := make(map[string]any, len(current))
		for key, item := range current {
			result[key] = RenderValue(item, context)
		}
		return result
	case []any:
		result := make([]any, len(current))
		for index, item := range current {
			result[index] = RenderValue(item, context)
		}
		return result
	default:
		return value
	}
}

func Lookup(context map[string]any, expression string) (any, bool) {
	var current any = context
	for _, key := range strings.Split(expression, ".") {
		switch object := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = object[key]
			if !ok {
				return nil, false
			}
		case map[string]string:
			var ok bool
			current, ok = object[key]
			if !ok {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return current, true
}

func EvaluateWhen(expression string, context map[string]any) bool {
	if strings.TrimSpace(expression) == "" {
		return true
	}
	for _, operator := range []string{"==", "!=", "contains"} {
		parts := strings.SplitN(expression, operator, 2)
		if len(parts) != 2 {
			continue
		}
		left, _ := Lookup(context, strings.TrimSpace(parts[0]))
		right := strings.TrimSpace(parts[1])
		if len(right) >= 2 && ((right[0] == '"' && right[len(right)-1] == '"') || (right[0] == '\'' && right[len(right)-1] == '\'')) {
			right = right[1 : len(right)-1]
		}
		if operator == "==" {
			return fmt.Sprint(left) == right
		}
		if operator == "!=" {
			return fmt.Sprint(left) != right
		}
		return strings.Contains(fmt.Sprint(left), right)
	}
	value, ok := Lookup(context, strings.TrimSpace(expression))
	return ok && fmt.Sprint(value) != "" && fmt.Sprint(value) != "false"
}
