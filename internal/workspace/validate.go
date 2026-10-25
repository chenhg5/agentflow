package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/schema"
	"github.com/chenhg5/agentflow/internal/trigger"
)

type Issue struct {
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Message  string `json:"message"`
}

type Validation struct {
	Root              string           `json:"root"`
	LayoutVersion     string           `json:"layout_version"`
	Valid             bool             `json:"valid"`
	ExecutionVerified bool             `json:"execution_verified"`
	Issues            []Issue          `json:"issues"`
	Bindings          []map[string]any `json:"bindings"`
	RunStore          string           `json:"run_store"`
}

func Validate(root, workflowPath string) Validation {
	result := Validation{Root: root, LayoutVersion: LayoutVersion, Valid: true, Issues: []Issue{}, Bindings: []map[string]any{}, RunStore: filepath.Join(root, "runs")}
	issue := func(severity, path, message string) {
		result.Issues = append(result.Issues, Issue{severity, path, message})
		if severity == "error" {
			result.Valid = false
		}
	}
	executors, err := driver.LoadExecutors(root)
	if err != nil {
		issue("error", "executors.json", err.Error())
		return result
	}
	paths := []string{}
	if workflowPath != "" {
		paths = append(paths, workflowPath)
	} else if _, err := os.Stat(filepath.Join(root, "workflow.json")); !os.IsNotExist(err) {
		paths = append(paths, "workflow.json")
	}
	if _, err := os.Stat(filepath.Join(root, "triggers.json")); err == nil {
		config, err := trigger.LoadConfig(root)
		if err != nil {
			issue("error", "triggers.json", err.Error())
		} else {
			for _, definition := range config.Triggers {
				if definition.Enabled {
					paths = append(paths, definition.Target.Workflow)
				}
			}
		}
	}
	if len(paths) == 0 {
		issue("error", "workflow.json", "no default graph or enabled trigger target; select a graph with --workflow")
	}
	seen := map[string]bool{}
	for _, path := range paths {
		absolute := path
		if !filepath.IsAbs(path) {
			absolute = filepath.Join(root, path)
		}
		if seen[absolute] {
			continue
		}
		seen[absolute] = true
		data, err := os.ReadFile(absolute)
		if err != nil {
			issue("error", path, err.Error())
			continue
		}
		var flow model.Workflow
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&flow); err != nil {
			issue("error", path, err.Error())
			continue
		}
		for _, problem := range schema.Validate(flow) {
			issue("error", path, problem)
		}
		for _, node := range flow.Nodes {
			label := path + ":" + node.ID
			entry, exists := executors[node.Executor]
			if !exists {
				issue("error", label, fmt.Sprintf("unregistered executor %q", node.Executor))
				continue
			}
			if entry.Kind == "external" {
				continue
			}
			directory := entry.Workspace
			if directory == "" && (entry.Kind == "codex" || entry.Kind == "claude-code") {
				directory, _ = node.Config["workspace"].(string)
			}
			if directory == "" {
				directory = "."
			}
			if strings.Contains(directory, "{{") {
				issue("warning", label, "workspace contains runtime templates; validate it with representative run inputs")
				continue
			}
			if !filepath.IsAbs(directory) {
				directory = filepath.Join(root, filepath.FromSlash(directory))
			}
			info, err := os.Stat(directory)
			if err != nil {
				issue("error", label, "executor workspace: "+err.Error())
				continue
			}
			if !info.IsDir() {
				issue("error", label, "executor workspace is not a directory")
				continue
			}
			resolved, err := filepath.EvalSymlinks(directory)
			if err != nil {
				issue("error", label, err.Error())
				continue
			}
			result.Bindings = append(result.Bindings, map[string]any{"workflow": path, "node": node.ID, "executor": node.Executor, "workspace": directory, "resolved_workspace": resolved})
			entry.Workspace = directory
			if len(entry.Command) == 0 && entry.Kind == "command" {
				if values, ok := node.Config["command"].([]any); ok {
					for _, value := range values {
						if text, ok := value.(string); ok {
							entry.Command = append(entry.Command, text)
						}
					}
				}
			}
			if entry.Kind == "command" && len(entry.Command) == 0 && len(entry.ResumeCommand) == 0 && len(entry.ForkCommand) == 0 {
				issue("error", label, "program executor has no command")
				continue
			}
			if status := driver.CheckExecutor(node.Executor, entry); !status.Available {
				issue("error", label, status.Error)
			}
			if entry.Kind == "codex" {
				if _, err := os.Stat(filepath.Join(directory, "AGENTS.md")); err != nil {
					issue("warning", label, "no AGENTS.md in this Codex workspace; confirm where its persistent role instructions come from")
				}
			}
		}
	}
	return result
}
