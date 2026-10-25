package workspace

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/template"
)

// Resource describes a discovered file, never its potentially private contents.
// Discovery does not establish that a provider loaded or followed that file.
type Resource struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type AgentResources struct {
	Workflow                 string            `json:"workflow"`
	Node                     string            `json:"node"`
	Candidate                int               `json:"candidate"`
	Executor                 string            `json:"executor"`
	Kind                     string            `json:"kind"`
	Workspace                string            `json:"workspace"`
	ResolvedWorkspace        string            `json:"resolved_workspace,omitempty"`
	WorkspaceStatus          string            `json:"workspace_status"`
	UnresolvedReferences     []string          `json:"unresolved_references"`
	Session                  model.SessionSpec `json:"session"`
	InlineInstructionPresent bool              `json:"inline_instruction_present"`
	Instructions             []Resource        `json:"local_instructions"`
	Skills                   []Resource        `json:"local_skills"`
	ToolConfigs              []Resource        `json:"local_tool_configs"`
	Issues                   []string          `json:"issues"`
	EffectiveContextVerified bool              `json:"effective_context_verified"`
	Scope                    string            `json:"scope"`
}

// staticReferenceKnown distinguishes a known missing representative value from
// a value that only exists after an upstream node executes. Runtime rendering
// deliberately preserves the former distinction: an exact missing placeholder
// becomes nil through template.RenderValue, while a future value must remain
// unresolved during static validation.
func staticReferenceKnown(context map[string]any, ref string, entry bool, inputs map[string]model.InputDefinition) bool {
	if resolved, ok := template.Lookup(context, ref); ok {
		if text, ok := resolved.(string); ok && strings.Contains(text, "{{") {
			return false
		}
		return true
	}
	if ref == "config" || strings.HasPrefix(ref, "config.") {
		return true
	}
	parts := strings.Split(ref, ".")
	// Runtime always installs the supplied run-input map as the entry node's
	// input, whether or not a key was declared in workflow.inputs. A missing
	// entry input path is therefore known absent and RenderValue/Render turn it
	// into nil/empty text. Downstream input remains future runtime state.
	if entry && len(parts) > 1 && parts[0] == "input" {
		return true
	}
	if len(parts) == 2 && parts[0] == "inputs" {
		_, declared := inputs[parts[1]]
		return declared
	}
	return false
}

// renderKnown uses the runtime renderer for values known from representative
// inputs, including known-absent optional values, while preserving genuinely
// unknown downstream references.
func renderKnown(value any, context map[string]any, entry bool, inputs map[string]model.InputDefinition) any {
	switch value := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, item := range value {
			result[key] = renderKnown(item, context, entry, inputs)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			result[i] = renderKnown(item, context, entry, inputs)
		}
		return result
	case string:
		for _, ref := range template.References(value) {
			if !staticReferenceKnown(context, ref, entry, inputs) {
				return value
			}
		}
		return template.RenderValue(value, context)
	default:
		return value
	}
}

// staticCandidateValues renders candidate config from the same values known to
// the runtime before driver.Prepare. Downstream input remains intentionally
// empty here: references to it are classified separately as unresolved.
func staticCandidateValues(flow model.Workflow, node model.Node, candidate model.ExecutorCandidate, inputs map[string]string) (map[string]any, map[string]any, map[string]any) {
	input := map[string]any{}
	configContext := map[string]any{"inputs": inputs}
	if node.ID == flow.Entry {
		for key, value := range inputs {
			input[key] = value
		}
		configContext["input"] = input
	}
	config, _ := renderKnown(candidate.Config, configContext, node.ID == flow.Entry, flow.Inputs).(map[string]any)
	if config == nil {
		config = map[string]any{}
	}
	return input, config, configContext
}

func candidateDirectory(flow model.Workflow, node model.Node, candidate model.ExecutorCandidate, entry driver.Executor, inputs map[string]string) (string, []string) {
	input, config, _ := staticCandidateValues(flow, node, candidate, inputs)
	context := map[string]any{"input": input, "config": config}
	directory := entry.Workspace
	if directory == "" && (entry.Kind == "codex" || entry.Kind == "claude-code") {
		directory, _ = config["workspace"].(string)
	}
	if directory == "" {
		directory = "."
	}
	missing := []string{}
	for _, ref := range template.References(directory) {
		if !staticReferenceKnown(context, ref, node.ID == flow.Entry, flow.Inputs) {
			missing = append(missing, ref)
		}
	}
	if len(missing) > 0 {
		return directory, missing
	}
	directory = template.Render(directory, context)
	if directory == "" {
		directory = "."
	}
	return directory, missing
}

func inspectAgent(root, path string, flow model.Workflow, node model.Node, index int, candidate model.ExecutorCandidate, entry driver.Executor, inputs map[string]string) AgentResources {
	directory, missing := candidateDirectory(flow, node, candidate, entry, inputs)
	instruction, _ := candidate.Config["instruction"].(string)
	result := AgentResources{Workflow: path, Node: node.ID, Candidate: index, Executor: candidate.Executor, Kind: entry.Kind, Workspace: directory, WorkspaceStatus: "unresolved", UnresolvedReferences: missing, Session: candidate.Session, InlineInstructionPresent: strings.TrimSpace(instruction) != "", Instructions: []Resource{}, Skills: []Resource{}, ToolConfigs: []Resource{}, Issues: []string{}, Scope: "Workspace-local discovery only. Parent/user/provider configuration and runtime loading are not verified."}
	if result.Session.Mode == "" {
		result.Session.Mode = "new"
	}
	if len(missing) > 0 {
		result.Issues = append(result.Issues, "workspace unresolved; supply representative --input values or inspect the actual run binding")
		return result
	}
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(root, directory)
	}
	result.Workspace, _ = filepath.Abs(directory)
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		result.WorkspaceStatus = "unavailable"
		result.Issues = append(result.Issues, err.Error())
		return result
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		result.WorkspaceStatus = "unavailable"
		result.Issues = append(result.Issues, "workspace is not an accessible directory")
		return result
	}
	result.ResolvedWorkspace, result.WorkspaceStatus = resolved, "inspected"
	add := func(target *[]Resource, relative string) {
		file := filepath.Join(resolved, filepath.FromSlash(relative))
		data, err := os.ReadFile(file)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			result.Issues = append(result.Issues, fmt.Sprintf("cannot inspect %s: %v", relative, err))
			return
		}
		*target = append(*target, Resource{Path: file, SHA256: fmt.Sprintf("%x", sha256.Sum256(data))})
	}
	instructionFiles, skillDirs, toolFiles := []string{"AGENTS.md"}, []string{".agents/skills", ".codex/skills"}, []string{".codex/config.toml", "tools/manifest.json"}
	if entry.Kind == "claude-code" {
		instructionFiles, skillDirs, toolFiles = []string{"CLAUDE.md", ".claude/CLAUDE.md"}, []string{".claude/skills"}, []string{".mcp.json", ".claude/settings.json", ".claude/settings.local.json", "tools/manifest.json"}
	}
	for _, name := range instructionFiles {
		add(&result.Instructions, name)
	}
	for _, dir := range skillDirs {
		entries, err := os.ReadDir(filepath.Join(resolved, dir))
		if err != nil && !os.IsNotExist(err) {
			result.Issues = append(result.Issues, fmt.Sprintf("cannot inspect %s: %v", dir, err))
		}
		for _, file := range entries {
			if !file.IsDir() && file.Type()&os.ModeSymlink == 0 {
				continue
			}
			add(&result.Skills, filepath.Join(dir, file.Name(), "SKILL.md"))
		}
	}
	for _, name := range toolFiles {
		add(&result.ToolConfigs, name)
	}
	if len(result.Instructions) == 0 {
		result.Issues = append(result.Issues, "no workspace-local persistent instructions discovered; document inline or external role/context sources")
	}
	if len(result.Skills) == 0 {
		result.Issues = append(result.Issues, "no workspace-local skills discovered; document external skills or why none are needed")
	}
	if len(result.ToolConfigs) == 0 {
		result.Issues = append(result.Issues, "no workspace-local tool configuration discovered; document provider tools or external tool sources")
	}
	sort.Slice(result.Skills, func(i, j int) bool { return result.Skills[i].Path < result.Skills[j].Path })
	return result
}
