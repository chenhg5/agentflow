package trigger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/robfig/cron/v3"
)

func LoadConfig(root string) (Config, error) {
	data, err := os.ReadFile(filepath.Join(root, "triggers.json"))
	if err != nil {
		return Config{}, err
	}
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, fmt.Errorf("invalid triggers.json: %w", err)
	}
	seen := map[string]bool{}
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
		if definition.Source.Kind == "cron" && definition.Source.Expression == "" {
			return Config{}, fmt.Errorf("cron trigger %s needs source.expression", definition.ID)
		}
		if definition.Source.Kind == "cron" {
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
	}
	return config, nil
}
