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

// References lists substitutions in nested JSON input, without evaluating them.
func References(value any) []string {
	var refs []string
	switch current := value.(type) {
	case string:
		for _, match := range placeholder.FindAllStringSubmatch(current, -1) {
			refs = append(refs, strings.TrimSpace(match[1]))
		}
	case map[string]any:
		for _, item := range current {
			refs = append(refs, References(item)...)
		}
	case []any:
		for _, item := range current {
			refs = append(refs, References(item)...)
		}
	case []string:
		for _, item := range current {
			refs = append(refs, References(item)...)
		}
	}
	return refs
}
