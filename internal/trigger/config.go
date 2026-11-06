package trigger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

func LoadConfig(root string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(root, "triggers.json"))
	if err != nil {
		return Config{}, err
	}
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("invalid triggers.json: %w", err)
	}
	seen := map[string]bool{}
	webhookPaths := map[string]string{}
	for _, definition := range config.Triggers {
		if definition.ID == "" {
			return Config{}, fmt.Errorf("trigger id is required")
		}
		if seen[definition.ID] {
			return Config{}, fmt.Errorf("duplicate trigger id: %s", definition.ID)
		}
		seen[definition.ID] = true
		if definition.Source.Kind != "cron" && definition.Source.Kind != "webhook" {
			return Config{}, fmt.Errorf("trigger %s has unsupported source kind: %s", definition.ID, definition.Source.Kind)
		}
		if definition.Target.Workflow == "" {
			return Config{}, fmt.Errorf("trigger %s needs target.workflow", definition.ID)
		}
		if definition.Policy.Concurrency != "" && definition.Policy.Concurrency != "forbid" && definition.Policy.Concurrency != "allow" {
			return Config{}, fmt.Errorf("trigger %s has unsupported concurrency policy: %s", definition.ID, definition.Policy.Concurrency)
		}
		if definition.Policy.Misfire != "" && definition.Policy.Misfire != "skip" && definition.Policy.Misfire != "fire-once" {
			return Config{}, fmt.Errorf("trigger %s has unsupported misfire policy: %s", definition.ID, definition.Policy.Misfire)
		}
		if definition.Policy.Misfire == "fire-once" && definition.Source.Kind != "cron" {
			return Config{}, fmt.Errorf("trigger %s: fire-once requires cron", definition.ID)
		}
		for name, binding := range definition.Bindings {
			switch binding.From {
			case "json", "payload", "header", "headers", "attribute", "attributes":
			case "event":
				switch binding.Path {
				case "scheduled_at", "received_at", "trigger_id", "delivery_id", "schedule_timezone", "catch_up":
				default:
					return Config{}, fmt.Errorf("trigger %s binding %s has unknown event fact %s", definition.ID, name, binding.Path)
				}
				if binding.Name != "" {
					return Config{}, fmt.Errorf("trigger %s binding %s: event facts use path, not name", definition.ID, name)
				}
			default:
				return Config{}, fmt.Errorf("trigger %s binding %s has unknown source %s", definition.ID, name, binding.From)
			}
		}
		if definition.Source.Kind == "cron" && definition.Source.Expression == "" {
			return Config{}, fmt.Errorf("cron trigger %s needs source.expression", definition.ID)
		}
		if definition.Source.Kind == "cron" {
			if strings.HasPrefix(definition.Source.Expression, "TZ=") || strings.HasPrefix(definition.Source.Expression, "CRON_TZ=") {
				return Config{}, fmt.Errorf("cron trigger %s: declare timezone in source.timezone", definition.ID)
			}
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			if _, err := parser.Parse(definition.Source.Expression); err != nil {
				return Config{}, fmt.Errorf("cron trigger %s has invalid expression: %w", definition.ID, err)
			}
			if definition.Source.Timezone != "" {
				if _, err := time.LoadLocation(definition.Source.Timezone); err != nil {
					return Config{}, fmt.Errorf("cron trigger %s timezone: %w", definition.ID, err)
				}
			}
		}
		if definition.Source.Kind == "webhook" && definition.Source.Path == "" {
			return Config{}, fmt.Errorf("webhook trigger %s needs source.path", definition.ID)
		}
		if definition.Source.Kind == "webhook" && definition.Source.Path[0] != '/' {
			return Config{}, fmt.Errorf("webhook trigger %s source.path must start with /", definition.ID)
		}
		if definition.Enabled && definition.Source.Kind == "webhook" {
			path := strings.TrimSuffix(definition.Source.Path, "/")
			if previous, exists := webhookPaths[path]; exists {
				return Config{}, fmt.Errorf("webhook triggers %s and %s share source.path %s", previous, definition.ID, definition.Source.Path)
			}
			webhookPaths[path] = definition.ID
		}
	}
	return config, nil
}
