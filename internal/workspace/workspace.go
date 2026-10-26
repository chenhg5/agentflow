// Package workspace owns the recommended directory layout and scaffolding.
// Its conventions do not add business semantics to the graph engine.
package workspace

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
)

const LayoutVersion = "1"

var ErrConflict = errors.New("workspace file already exists")

//go:embed templates/*
var templates embed.FS

type Registry struct {
	Executors map[string]driver.Executor `json:"executors"`
}

type Change struct {
	Root          string   `json:"root"`
	LayoutVersion string   `json:"layout_version"`
	Created       []string `json:"created"`
	Registered    string   `json:"registered,omitempty"`
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

const Ignore = "/runs/\n/dispatch/\n/trigger-deliveries/\n/scheduler.lock\n/trigger-dispatch.lock\n/resource-state/\n/resources.lock\n/queue.json\n/queue.lock\n/worker.lock\n/worker.json\n/output/\n__pycache__/\n"

func Contract() map[string]any {
	return map[string]any{
		"layout_version": LayoutVersion,
		"root":           "Invoke commands from the standalone workflow workspace root.",
		"files":          map[string]string{"workflow.json": "Recommended single graph location; omit at root for multiple graphs.", "executors.json": "Shared named executor registrations.", "triggers.json": "Optional cron/webhook triggers with explicit graph targets; reload service after edits when no work is executing.", "DESIGN.md": "Authoring notes for contracts, roles/context/tools, transitions, human participation and verification; not an engine schema."},
		"directories":    map[string]string{"executors/agents/<name>": "Owned agent workspaces or symlinks; external paths also work.", "executors/programs/<name>": "Program executor source and working directory.", "workflows": "Recommended home of all <name>.json graphs in a multi graph workspace; keep no separate root graph or compatibility copy. Engine accepts explicit paths; directory nesting does not connect graphs.", "runs/<run-id>": "Generated workflow snapshot, state, events and node artifacts.", "output": "Optional user program outputs; not the engine result store."},
		"node_artifacts": "runs/<run-id>/artifacts/<node-id>.<visit>.candidate-<n>.attempt-<n>.{input,config,result}.json; per-attempt logs live under artifacts/logs/",
		"environment":    map[string]string{"AGENTFLOW_ROOT": "Workflow workspace root.", "AGENTFLOW_WORKSPACE": "Executor working directory.", "AGENTFLOW_INPUT_FILE": "Absolute JSON input path.", "AGENTFLOW_CONFIG_FILE": "Absolute executor config path.", "AGENTFLOW_RESULT_FILE": "Absolute JSON result path."},
		"commands":       []string{"workspace init", "executor init", "executor add", "workspace validate"},
		"session_policy": "Bind a directory with executor.workspace; each node.execution.candidates item owns its session policy.",
	}
}

func Init(root string, dryRun bool) (Change, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Change{}, err
	}
	files, entry, err := executorFiles("step", "command", "")
	if err != nil {
		return Change{}, err
	}
	registry, _ := json.MarshalIndent(Registry{Executors: map[string]driver.Executor{"step": entry}}, "", "  ")
	flow := model.Workflow{Version: "3", Name: filepath.Base(root), Entry: "step", Nodes: []model.Node{{ID: "step", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "step"}}}, InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "object"}}}, Edges: []model.Transition{{From: "step", To: "$end"}}}
	workflow, _ := json.MarshalIndent(flow, "", "  ")
	files["executors.json"], files["workflow.json"], files[".gitignore"] = string(registry)+"\n", string(workflow)+"\n", Ignore
	design, err := templates.ReadFile("templates/DESIGN.md")
	if err != nil {
		return Change{}, err
	}
	files["DESIGN.md"] = string(design)
	files["README.md"] = "# Workflow workspace\n\nLayout version 1. Run commands from this directory. Single graph: root workflow.json. Multiple graphs: move every graph into workflows/<name>.json, remove the root graph, and update trigger/operator targets. Definitions share the root executor registry and run store. See DESIGN.md.\n\n```sh\nagentflow workspace validate\nagentflow run start workflow.json\n```\n\nThe starter program passes incoming JSON through. Adapt the graph and executor to your task. Add an owned agent with `agentflow executor init <name> --kind codex`, or bind an existing directory with `agentflow executor add <name> --kind codex --workspace /existing/path`. Each ordered execution candidate owns its config and session policy. Generated per-attempt evidence lives in runs/; inspect it with run get/logs/metrics.\n"
	created, err := createFiles(root, files, dryRun)
	return Change{Root: root, LayoutVersion: LayoutVersion, Created: created}, err
}

func InitExecutor(root, name, kind, modelName string, dryRun bool) (Change, error) {
	files, entry, err := executorFiles(name, kind, modelName)
	if err != nil {
		return Change{}, err
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(entry.Workspace))); err == nil {
		return Change{}, fmt.Errorf("%w: %s; use executor add to reuse this directory", ErrConflict, entry.Workspace)
	} else if !os.IsNotExist(err) {
		return Change{}, err
	}
	registry, err := readRegistry(root)
	if err != nil {
		return Change{}, err
	}
	if _, exists := registry.Executors[name]; exists {
		return Change{}, fmt.Errorf("%w: executor %s", ErrConflict, name)
	}
	created, err := createFiles(root, files, dryRun)
	if err != nil {
		return Change{}, err
	}
	if !dryRun {
		registry.Executors[name] = entry
		if err := saveRegistry(root, registry); err != nil {
			for _, path := range created {
				_ = os.Remove(filepath.Join(root, filepath.FromSlash(path)))
			}
			return Change{}, err
		}
	}
	return Change{Root: root, LayoutVersion: LayoutVersion, Created: created, Registered: name}, nil
}

func AddExecutor(root, name, kind, directory, modelName string, command []string, dryRun bool) (Change, error) {
	if !namePattern.MatchString(name) {
		return Change{}, fmt.Errorf("executor name must be 1-64 letters, digits, dots, hyphens or underscores, starting with a letter or digit")
	}
	if kind != "codex" && kind != "claude-code" && kind != "command" && kind != "external" {
		return Change{}, fmt.Errorf("unsupported executor kind %q", kind)
	}
	if kind != "external" {
		if directory == "" {
			return Change{}, fmt.Errorf("--workspace must identify an existing directory")
		}
		path := directory
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		info, err := os.Stat(path)
		if err != nil {
			return Change{}, fmt.Errorf("executor workspace: %w", err)
		}
		if !info.IsDir() {
			return Change{}, fmt.Errorf("executor workspace is not a directory: %s", directory)
		}
	}
	if kind == "command" && len(command) == 0 {
		return Change{}, fmt.Errorf("command executor needs --command as a JSON argument array")
	}
	registry, err := readRegistry(root)
	if err != nil {
		return Change{}, err
	}
	if _, exists := registry.Executors[name]; exists {
		return Change{}, fmt.Errorf("%w: executor %s", ErrConflict, name)
	}
	if !dryRun {
		registry.Executors[name] = driver.Executor{Kind: kind, Workspace: directory, Model: modelName, Command: command}
		if err := saveRegistry(root, registry); err != nil {
			return Change{}, err
		}
	}
	return Change{Root: root, LayoutVersion: LayoutVersion, Created: []string{}, Registered: name}, nil
}

func executorFiles(name, kind, modelName string) (map[string]string, driver.Executor, error) {
	if !namePattern.MatchString(name) {
		return nil, driver.Executor{}, fmt.Errorf("invalid executor directory name %q", name)
	}
	files := map[string]string{}
	read := func(file string) string {
		data, _ := templates.ReadFile("templates/" + file)
		return strings.ReplaceAll(string(data), "{{name}}", name)
	}
	if kind == "command" {
		directory := "executors/programs/" + name
		files[directory+"/main.py"] = read("program.py")
		return files, driver.Executor{Kind: kind, Workspace: directory, Command: []string{"python3", "main.py"}}, nil
	}
	if kind != "codex" && kind != "claude-code" {
		return nil, driver.Executor{}, fmt.Errorf("executor init scaffolds codex, claude-code or command; use executor add to bind another existing executor")
	}
	directory := "executors/agents/" + name
	if kind == "claude-code" {
		for target, source := range map[string]string{"CLAUDE.md": "CLAUDE.md", ".claude/skills/workflow-contract/SKILL.md": "SKILL.md", ".mcp.json": "claude-mcp.json", "tools/manifest.json": "manifest.json", "tools/read_input.py": "read_input.py", "tools/mcp_server.py": "mcp_server.py"} {
			files[directory+"/"+target] = read(source)
		}
		return files, driver.Executor{Kind: kind, Workspace: directory, Model: modelName}, nil
	}
	for target, source := range map[string]string{"AGENTS.md": "AGENTS.md", ".agents/skills/workflow-contract/SKILL.md": "SKILL.md", ".codex/config.toml": "codex.toml", "tools/manifest.json": "manifest.json", "tools/read_input.py": "read_input.py", "tools/mcp_server.py": "mcp_server.py"} {
		files[directory+"/"+target] = read(source)
	}
	return files, driver.Executor{Kind: kind, Workspace: directory, Model: modelName, Sandbox: "workspace-write", SkipGitRepoCheck: true, ExtraArgs: []string{"-c", `sandbox_workspace_write.writable_roots=["../../../runs"]`}}, nil
}

func createFiles(root string, files map[string]string, dryRun bool) ([]string, error) {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		for directory := filepath.Dir(path); directory != "."; directory = filepath.Dir(directory) {
			if info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(directory))); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("%w: refusing to create files through symlink %s; use executor add", ErrConflict, directory)
			}
		}
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(path))); err == nil {
			return nil, fmt.Errorf("%w: %s", ErrConflict, path)
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	if dryRun {
		return paths, nil
	}
	created := []string{}
	for _, path := range paths {
		absolute := filepath.Join(root, filepath.FromSlash(path))
		err := os.MkdirAll(filepath.Dir(absolute), 0o755)
		if err == nil {
			var file *os.File
			file, err = os.OpenFile(absolute, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
			if err == nil {
				created = append(created, path)
				_, err = file.WriteString(files[path])
				err = errors.Join(err, file.Close())
			}
		}
		if err != nil {
			for _, item := range created {
				_ = os.Remove(filepath.Join(root, filepath.FromSlash(item)))
			}
			return nil, err
		}
	}
	return created, nil
}

func readRegistry(root string) (Registry, error) {
	data, err := os.ReadFile(filepath.Join(root, "executors.json"))
	if os.IsNotExist(err) {
		return Registry{Executors: map[string]driver.Executor{}}, nil
	}
	if err != nil {
		return Registry{}, err
	}
	var registry Registry
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return registry, err
	}
	if registry.Executors == nil {
		return registry, fmt.Errorf("executors.json must contain an executors object")
	}
	return registry, nil
}

func saveRegistry(root string, registry Registry) error {
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(root, "executors-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(append(data, '\n'))
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(root, "executors.json"))
}
