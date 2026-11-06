// Package telemetry reports runs to a Hub. It is the runner side of the hubapi
// contract: an adapter turns engine state into a RunReport, an outbox keeps the
// latest report of every run on disk, and a sender delivers pending reports.
// Reporting never blocks or fails a run: when the Hub is unreachable the
// reports stay in the outbox and are delivered on the next flush.
package telemetry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Config tells a runner where to report.
type Config struct {
	URL     string `json:"url"`
	Token   string `json:"token,omitempty"`
	Machine string `json:"machine,omitempty"`
}

// Enabled reports whether a Hub URL is configured.
func (c Config) Enabled() bool { return strings.TrimSpace(c.URL) != "" }

// ConfigFile is the project-level configuration file name under .agentflow.
const ConfigFile = "hub.json"

// LoadConfig resolves the Hub configuration. Later sources override earlier
// ones field by field: ~/.agentflow/hub.json, <root>/hub.json, then the
// AGENTFLOW_HUB_URL, AGENTFLOW_HUB_TOKEN and AGENTFLOW_MACHINE variables.
func LoadConfig(root string) (Config, error) {
	var config Config
	files := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		files = append(files, filepath.Join(home, ".agentflow", ConfigFile))
	}
	files = append(files, filepath.Join(root, ConfigFile))
	for _, file := range files {
		data, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Config{}, err
		}
		var layer Config
		if err := json.Unmarshal(data, &layer); err != nil {
			return Config{}, errors.New(file + ": " + err.Error())
		}
		config = merge(config, layer)
	}
	config = merge(config, Config{URL: os.Getenv("AGENTFLOW_HUB_URL"), Token: os.Getenv("AGENTFLOW_HUB_TOKEN"), Machine: os.Getenv("AGENTFLOW_MACHINE")})
	config.URL = strings.TrimRight(strings.TrimSpace(config.URL), "/")
	return config, nil
}

func merge(base, layer Config) Config {
	if layer.URL != "" {
		base.URL = layer.URL
	}
	if layer.Token != "" {
		base.Token = layer.Token
	}
	if layer.Machine != "" {
		base.Machine = layer.Machine
	}
	return base
}
