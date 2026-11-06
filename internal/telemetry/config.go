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
//
// The project file comes with the repository, so it is not trusted with your
// token: when it is the one that sets the URL, only a token from the same file
// is sent there. A cloned repository cannot point your token at its own host.
func LoadConfig(root string) (Config, error) {
	var user Config
	if home, err := os.UserHomeDir(); err == nil {
		layer, err := readConfig(filepath.Join(home, ".agentflow", ConfigFile))
		if err != nil {
			return Config{}, err
		}
		user = layer
	}
	project, err := readConfig(filepath.Join(root, ConfigFile))
	if err != nil {
		return Config{}, err
	}
	env := Config{URL: os.Getenv("AGENTFLOW_HUB_URL"), Token: os.Getenv("AGENTFLOW_HUB_TOKEN"), Machine: os.Getenv("AGENTFLOW_MACHINE")}

	config := merge(merge(user, project), env)
	if env.URL == "" && project.URL != "" {
		config.Token = project.Token
	}
	config.URL = strings.TrimRight(strings.TrimSpace(config.URL), "/")
	return config, nil
}

func readConfig(file string) (Config, error) {
	var config Config
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return Config{}, errors.New(file + ": " + err.Error())
	}
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
