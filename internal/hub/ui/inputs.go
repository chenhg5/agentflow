package ui

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/chenhg5/agentflow/internal/hub/domain"
)

// inputField is one form control built from a property of a node's
// outputSchema.
type inputField struct {
	Name        string
	Label       string
	Description string
	Kind        string // choice, bool, number, integer, text, textarea
	Options     []string
	Required    bool
	Default     string
}

// inputForm is the "submit input" card for one waiting step.
type inputForm struct {
	NodeID   string
	Executor string
	Waiting  string
	Fields   []inputField
	RawJSON  string // set when the schema is not a flat object: the form is a JSON box
	Accepts  bool
	CSRF     string
}

// formFor builds the form for a waiting step from its outputSchema. A flat
// object with simple properties becomes fields; anything else a JSON box.
func formFor(schema map[string]any) (fields []inputField, raw string) {
	properties, _ := schema["properties"].(map[string]any)
	if schema == nil || schema["type"] != "object" || len(properties) == 0 {
		return nil, "{}"
	}
	required := map[string]bool{}
	if list, ok := schema["required"].([]any); ok {
		for _, item := range list {
			if name, ok := item.(string); ok {
				required[name] = true
			}
		}
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if required[names[i]] != required[names[j]] {
			return required[names[i]]
		}
		return names[i] < names[j]
	})
	skeleton := map[string]any{}
	for _, name := range names {
		property, _ := properties[name].(map[string]any)
		field := inputField{Name: name, Label: name, Required: required[name]}
		if title, ok := property["title"].(string); ok && title != "" {
			field.Label = title
		}
		field.Description, _ = property["description"].(string)
		if value, ok := property["default"]; ok {
			field.Default = fmt.Sprint(value)
		}
		kind, _ := property["type"].(string)
		switch enum, _ := property["enum"].([]any); {
		case len(enum) > 0:
			field.Kind = "choice"
			for _, option := range enum {
				field.Options = append(field.Options, fmt.Sprint(option))
			}
		case kind == "boolean":
			field.Kind = "bool"
		case kind == "integer", kind == "number":
			field.Kind = kind
		case kind == "string":
			field.Kind = "text"
			if limit, ok := property["maxLength"].(float64); !ok || limit > 200 {
				field.Kind = "textarea"
			}
		default:
			// arrays, nested objects, unions: the whole form becomes JSON.
			for _, other := range names {
				skeleton[other] = nil
			}
			data, _ := json.MarshalIndent(skeleton, "", "  ")
			return nil, string(data)
		}
		fields = append(fields, field)
	}
	return fields, ""
}

// outputFromForm turns a posted form back into the node's output object,
// typed by the same schema the form was built from.
func outputFromForm(values url.Values, schema map[string]any) (map[string]any, error) {
	if raw := strings.TrimSpace(values.Get("json")); raw != "" {
		var output map[string]any
		if err := json.Unmarshal([]byte(raw), &output); err != nil || output == nil {
			return nil, errors.New("输入要是一个 JSON 对象")
		}
		return output, nil
	}
	fields, _ := formFor(schema)
	output := map[string]any{}
	for _, field := range fields {
		key := "f." + field.Name
		value := values.Get(key)
		switch field.Kind {
		case "bool":
			output[field.Name] = value == "true"
			continue
		case "integer", "number":
			if value == "" {
				break
			}
			number, err := strconv.ParseFloat(value, 64)
			if err != nil || (field.Kind == "integer" && number != float64(int64(number))) {
				return nil, fmt.Errorf("%s 要填%s", field.Label, map[string]string{"integer": "整数", "number": "数字"}[field.Kind])
			}
			output[field.Name] = number
			continue
		}
		if value == "" {
			if field.Required {
				return nil, fmt.Errorf("%s 是必填的", field.Label)
			}
			continue
		}
		if field.Kind == "choice" && !contains(field.Options, value) {
			return nil, fmt.Errorf("%s 只能选 %s", field.Label, strings.Join(field.Options, " / "))
		}
		output[field.Name] = value
	}
	return output, nil
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// csrfToken binds a form to one run with a key that lives as long as the Hub
// process, so another site cannot make a person's browser submit input.
func (p *Pages) csrfToken(key domain.RunKey) string {
	mac := hmac.New(sha256.New, p.csrfKey)
	mac.Write([]byte(key.String()))
	return hex.EncodeToString(mac.Sum(nil))
}

func (p *Pages) validCSRF(key domain.RunKey, token string) bool {
	return hmac.Equal([]byte(token), []byte(p.csrfToken(key)))
}
