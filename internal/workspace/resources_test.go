package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
)

func resourceGraph(t *testing.T, root string, entry driver.Executor, candidate model.ExecutorCandidate) {
	t.Helper()
	registry, _ := json.Marshal(Registry{Executors: map[string]driver.Executor{"reviewer": entry}})
	flow, _ := json.Marshal(model.Workflow{Version: "3", Name: "resources", Entry: "review", Inputs: map[string]model.InputDefinition{}, Nodes: []model.Node{{ID: "review", Execution: model.Execution{Candidates: []model.ExecutorCandidate{candidate}}, OutputSchema: map[string]any{"type": "object"}}}, Edges: []model.Transition{{From: "review", To: "$end"}}})
	for name, data := range map[string][]byte{"executors.json": registry, "workflow.json": flow} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeScaffoldResourcesAndNonOverwriting(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, false); err != nil {
		t.Fatal(err)
	}
	if _, err := InitExecutor(root, "reviewer", "claude-code", "", false); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "executors/agents/reviewer")
	for _, name := range []string{"CLAUDE.md", ".claude/skills/workflow-contract/SKILL.md", ".mcp.json", "tools/manifest.json", "tools/mcp_server.py"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	var mcp struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".mcp.json"))
	if err := json.Unmarshal(data, &mcp); err != nil || mcp.Servers["workflow_tools"].Command != "python3" {
		t.Fatalf("invalid native MCP configuration: %s %v", data, err)
	}
	role := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(role, []byte("Existing specialist instructions"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InitExecutor(root, "reviewer", "claude-code", "", false); err == nil {
		t.Fatal("reinitializing an existing agent must fail")
	}
	if data, _ := os.ReadFile(role); string(data) != "Existing specialist instructions" {
		t.Fatal("existing role was overwritten")
	}
	flow := model.Workflow{Entry: "review"}
	r := inspectAgent(root, "workflow.json", flow, model.Node{ID: "review"}, 0, model.ExecutorCandidate{Executor: "reviewer"}, driver.Executor{Kind: "claude-code", Workspace: dir}, nil)
	if r.WorkspaceStatus != "inspected" || len(r.Instructions) != 1 || len(r.Skills) != 1 || len(r.ToolConfigs) != 2 || r.EffectiveContextVerified {
		t.Fatalf("incorrect resource inventory: %+v", r)
	}
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), "Existing specialist instructions") {
		t.Fatal("diagnostics must expose identities/digests, not file contents")
	}
}

func TestResourceResolutionPreservesUnknownContext(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, false); err != nil {
		t.Fatal(err)
	}
	if _, err := InitExecutor(root, "owned", "claude-code", "", false); err != nil {
		t.Fatal(err)
	}
	flow := model.Workflow{Entry: "review"}
	node := model.Node{ID: "review"}
	candidate := model.ExecutorCandidate{Executor: "reviewer", Config: map[string]any{"location": "{{ inputs.agent_dir }}", "instruction": "Read {{ nodes.prepare.result.manifest }}"}}
	entry := driver.Executor{Kind: "claude-code", Workspace: "{{ config.location }}"}
	unknown := inspectAgent(root, "workflow.json", flow, node, 0, candidate, entry, nil)
	if unknown.WorkspaceStatus != "unresolved" || unknown.ResolvedWorkspace != "" {
		t.Fatalf("missing input silently resolved: %+v", unknown)
	}
	known := inspectAgent(root, "workflow.json", flow, node, 0, candidate, entry, map[string]string{"agent_dir": "executors/agents/owned"})
	if known.WorkspaceStatus != "inspected" || len(known.Instructions) != 1 {
		t.Fatalf("known path blocked by unrelated runtime instruction: %+v", known)
	}
	candidate.Config["location"] = "{{ input.agent_dir }}"
	node.ID = "later"
	unknown = inspectAgent(root, "workflow.json", flow, node, 0, candidate, entry, map[string]string{"agent_dir": "executors/agents/owned"})
	if unknown.WorkspaceStatus != "unresolved" {
		t.Fatal("upstream node input must not be fabricated from run input")
	}
}

func TestBareClaudeAndExternalResourceDiscovery(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	flow, node := model.Workflow{Entry: "review"}, model.Node{ID: "review"}
	candidate := model.ExecutorCandidate{Executor: "reviewer", Config: map[string]any{"instruction": "Task-specific request"}}
	entry := driver.Executor{Kind: "claude-code", Workspace: outside}
	bare := inspectAgent(root, "workflow.json", flow, node, 0, candidate, entry, nil)
	if bare.WorkspaceStatus != "inspected" || len(bare.Issues) != 3 || !bare.InlineInstructionPresent {
		t.Fatalf("missing resources should be visible without disallowing inline/external roles: %+v", bare)
	}
	if err := os.WriteFile(filepath.Join(outside, "CLAUDE.md"), []byte("External role"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked-agent")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	entry.Workspace = link
	linked := inspectAgent(root, "workflow.json", flow, node, 0, candidate, entry, nil)
	resolvedOutside, _ := filepath.EvalSymlinks(outside)
	if linked.ResolvedWorkspace != resolvedOutside || len(linked.Instructions) != 1 || linked.Session.Mode != "new" {
		t.Fatalf("external identity not retained: %+v", linked)
	}
	if data, _ := os.ReadFile(filepath.Join(outside, "CLAUDE.md")); string(data) != "External role" {
		t.Fatal("resource inspection modified external role")
	}
}

func TestValidationExposesAgentResourcesWithoutRequiringAgents(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, false); err != nil {
		t.Fatal(err)
	}
	programOnly := Validate(root, "workflow.json")
	if !programOnly.Valid || len(programOnly.AgentResources) != 0 {
		t.Fatalf("program-only workflows must remain valid: %+v", programOnly)
	}
	// Missing provider binary may make structural validation unavailable on CI;
	// resource discovery must still describe the configured candidate.
	resourceGraph(t, root, driver.Executor{Kind: "claude-code", Workspace: "{{ config.location }}"}, model.ExecutorCandidate{Executor: "reviewer", Config: map[string]any{"location": "{{ inputs.directory }}"}})
	unknown := Validate(root, "workflow.json")
	known := ValidateWithInputs(root, "workflow.json", map[string]string{"directory": root})
	if len(unknown.AgentResources) != 1 || unknown.AgentResources[0].WorkspaceStatus != "unresolved" || len(known.AgentResources) != 1 || known.AgentResources[0].WorkspaceStatus != "inspected" || known.ExecutionVerified {
		t.Fatalf("resource diagnostics did not resolve representative inputs: %+v %+v", unknown, known)
	}
}
