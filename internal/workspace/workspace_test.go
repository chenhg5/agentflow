package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/agentflow/internal/driver"
)

func TestInitDryRunConflictAndExecutorScaffolding(t *testing.T) {
	root := filepath.Join(t.TempDir(), "flow")
	if _, err := Init(root, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("dry run created workspace: %v", err)
	}
	if _, err := Init(root, false); err != nil {
		t.Fatal(err)
	}
	if report := Validate(root, ""); !report.Valid {
		t.Fatalf("starter invalid: %+v", report)
	}
	if _, err := Init(root, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("existing workspace overwritten: %v", err)
	}
	if _, err := InitExecutor(root, "writer", "codex", "", false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"AGENTS.md", ".agents/skills/workflow-contract/SKILL.md", ".codex/config.toml", "tools/mcp_server.py", "tools/manifest.json", "tools/read_input.py"} {
		if info, err := os.Stat(filepath.Join(root, "executors/agents/writer", name)); err != nil || info.Size() == 0 {
			t.Fatalf("missing usable agent file %s: %v", name, err)
		}
	}
	executors, err := driver.LoadExecutors(root)
	if err != nil {
		t.Fatal(err)
	}
	if executors["writer"].Workspace != "executors/agents/writer" || executors["writer"].Kind != "codex" {
		t.Fatalf("wrong registration: %+v", executors["writer"])
	}
}

func TestExistingAgentIsBoundWithoutCreatingOrOverwritingFiles(t *testing.T) {
	root, external := t.TempDir(), t.TempDir()
	marker := filepath.Join(external, "AGENTS.md")
	if err := os.WriteFile(marker, []byte("existing role"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AddExecutor(root, "existing", "codex", external, "", nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "executors")); !os.IsNotExist(err) {
		t.Fatalf("unnecessary local agent directory created: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "existing role" {
		t.Fatalf("external role changed: %v", err)
	}
	if _, err := InitExecutor(root, "existing", "codex", "", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate entry accepted: %v", err)
	}
}

func TestInitRefusesExistingSymlinkAndValidationFindsBrokenBinding(t *testing.T) {
	root, external := t.TempDir(), t.TempDir()
	if _, err := Init(root, false); err != nil {
		t.Fatal(err)
	}
	agents := filepath.Join(root, "executors", "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(agents, "linked")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	if _, err := InitExecutor(root, "linked", "codex", "", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("symlink overwritten: %v", err)
	}
	entries, err := os.ReadDir(external)
	if err != nil || len(entries) != 0 {
		t.Fatalf("external directory modified: %v", err)
	}
	if _, err := AddExecutor(root, "linked", "codex", "executors/agents/linked", "", nil, false); err != nil {
		t.Fatal(err)
	}
	flow := `{"version":"3","name":"reuse","entry":"linked","nodes":[{"id":"linked","execution":{"candidates":[{"executor":"linked","session":{"mode":"fixed","id":"saved-session"}}]}}],"edges":[{"from":"linked","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if report := Validate(root, ""); report.Valid {
		t.Fatalf("broken binding validated: %+v", report)
	}
}

func TestConflictPreflightCreatesNoPartialWorkspace(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "workflow.json")
	if err := os.WriteFile(file, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(root, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict not detected: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("partially initialized workspace: %v %+v", err, entries)
	}
}

func TestValidationUsesTriggerTargetsWithoutRequiringDefaultGraph(t *testing.T) {
	root := t.TempDir()
	if _, err := Init(root, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "workflow.json"), filepath.Join(root, "morning.json")); err != nil {
		t.Fatal(err)
	}
	config := `{"triggers":[{"id":"morning","enabled":true,"source":{"kind":"cron","expression":"0 9 * * *"},"target":{"workflow":"morning.json"}}]}`
	if err := os.WriteFile(filepath.Join(root, "triggers.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if report := Validate(root, ""); !report.Valid || len(report.Bindings) != 1 {
		t.Fatalf("trigger target not validated: %+v", report)
	}
	if report := Validate(root, "missing.json"); report.Valid {
		t.Fatalf("explicit missing graph passed validation: %+v", report)
	}
}

func TestValidationAllowsUnavailablePrimaryWithUsableFallback(t *testing.T) {
	root := t.TempDir()
	flow := `{"version":"3","name":"fallback","entry":"step","nodes":[{"id":"step","execution":{"candidates":[{"executor":"codex","config":{"workspace":"missing-primary"}},{"executor":"process","config":{"command":["sh","-c","printf '{}' > \"$AGENTFLOW_RESULT_FILE\""]}}]}}],"edges":[{"from":"step","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	result := Validate(root, "")
	if !result.Valid {
		t.Fatalf("usable fallback should keep workspace valid: %+v", result.Issues)
	}
	foundWarning := false
	for _, issue := range result.Issues {
		if issue.Severity == "warning" && strings.Contains(issue.Path, "candidate-1") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("unavailable primary warning missing: %+v", result.Issues)
	}
}

func TestValidationRejectsUnreachableFallbackPolicy(t *testing.T) {
	root := t.TempDir()
	flow := `{"version":"3","name":"fallback","entry":"step","nodes":[{"id":"step","execution":{"candidates":[{"executor":"worker"},{"executor":"process","config":{"command":["sh","-c","printf '{}' > \"$AGENTFLOW_RESULT_FILE\""]}}],"fallback":{"when":"failure.kind == 'exit_code'"}}}],"edges":[{"from":"step","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := `{"executors":{"worker":{"kind":"command","command":["` + filepath.Join(root, "not-installed") + `"]}}}`
	if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	result := Validate(root, "")
	if result.Valid {
		t.Fatalf("unreachable fallback policy passed validation: %+v", result.Issues)
	}
	foundUnreachable := false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "unreachable") {
			foundUnreachable = true
		}
	}
	if !foundUnreachable {
		t.Fatalf("unreachable fallback was not explained: %+v", result.Issues)
	}
}

func TestValidationModelsRuntimeFailureFieldsForReachability(t *testing.T) {
	setup := func(when, retry string) Validation {
		root := t.TempDir()
		retryField := ""
		if retry != "" {
			retryField = `"retry":{"maxAttempts":` + retry + `},`
		}
		flow := `{"version":"3","name":"fallback","entry":"step","nodes":[{"id":"step",` + retryField + `"execution":{"candidates":[{"executor":"worker"},{"executor":"process","config":{"command":["sh","-c","printf '{}' > \"$AGENTFLOW_RESULT_FILE\""]}}],"fallback":{"when":"` + when + `"}}}],"edges":[{"from":"step","to":"$end"}]}`
		if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
			t.Fatal(err)
		}
		registry := `{"executors":{"worker":{"kind":"command","command":["` + filepath.Join(root, "not-installed") + `"]}}}`
		if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		return Validate(root, "")
	}
	// Runtime fallback observes a 1-based attempt sequence.
	if result := setup("failure.kind == 'start_failed' && failure.attempt == 1", "1"); !result.Valid {
		t.Fatalf("runtime-equivalent attempt field was rejected: %+v", result.Issues)
	}
	// With two attempts the policy sees attempt 2, so attempt 1 is unreachable.
	if result := setup("failure.kind == 'start_failed' && failure.attempt == 1", "2"); result.Valid {
		t.Fatalf("retry position was ignored in static reachability: %+v", result.Issues)
	}
	// Message text is not reproducible statically; do not invent unreachability.
	if result := setup("failure.kind == 'start_failed' && failure.message == 'invented'", ""); !result.Valid {
		t.Fatalf("message-dependent policy was treated as definitely unreachable: %+v", result.Issues)
	}
}

func TestValidationCountsAttemptsFromStaticallyAvailableCandidates(t *testing.T) {
	setup := func(when string) Validation {
		root := t.TempDir()
		flow := `{"version":"3","name":"fallback","entry":"step","nodes":[{"id":"step","retry":{"maxAttempts":2},"execution":{"candidates":[{"executor":"first"},{"executor":"worker"},{"executor":"third"}],"fallback":{"when":"` + when + `"}}}],"edges":[{"from":"step","to":"$end"}]}`
		if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
			t.Fatal(err)
		}
		command := `["sh","-c","printf '{}' > \"$AGENTFLOW_RESULT_FILE\""]`
		registry := `{"executors":{"first":{"kind":"command","command":` + command + `},"worker":{"kind":"command","command":["` + filepath.Join(root, "not-installed") + `"]},"third":{"kind":"command","command":` + command + `}}}`
		if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		return Validate(root, "")
	}
	// The available first candidate consumes attempts 1-2 before the missing
	// candidate is reached, so its final runtime attempt is 4.
	result := setup("failure.kind == 'start_failed' && failure.attempt == 4")
	if !result.Valid {
		t.Fatalf("reachable candidate was rejected: %+v", result.Issues)
	}
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "unreachable") {
			t.Fatalf("runtime attempt position was ignored after an available candidate: %+v", result.Issues)
		}
	}
	// The same missing candidate is not attempt 2; a policy matching only that
	// position does not make the later candidate reachable.
	result = setup("failure.kind == 'start_failed' && failure.attempt == 2")
	found := false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "unreachable") {
			found = true
		}
	}
	if !found {
		t.Fatalf("wrongly reachable policy did not warn: %+v", result.Issues)
	}
}

func TestValidationRequiresTheCommandForTheSelectedSessionMode(t *testing.T) {
	setup := func(candidate, when string) Validation {
		root := t.TempDir()
		fallback := ""
		if when != "" {
			fallback = `"fallback":{"when":"` + when + `"},`
		}
		flow := `{"version":"3","name":"session","entry":"step","nodes":[{"id":"step","execution":{` + fallback + `"candidates":[` + candidate + `]}}],"edges":[{"from":"step","to":"$end"}]}`
		if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
			t.Fatal(err)
		}
		registry := `{"executors":{"worker":{"kind":"command","command":["sh","-c","exit 0"]}}}`
		if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		return Validate(root, "")
	}
	// A fixed session id always needs resumeCommand; the unused base command is
	// not the command validation should accept.
	result := setup(`{"executor":"worker","session":{"mode":"fixed","id":"known-session"}}`, "")
	if result.Valid {
		t.Fatalf("missing resumeCommand was reported usable: %+v", result.Bindings)
	}
	found := false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "does not define resumeCommand") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing resumeCommand was not reported: %+v", result.Issues)
	}
	// An explicit policy can select the configuration failure, matching runtime.
	result = setup(`{"executor":"worker","session":{"mode":"fixed","id":"known-session"}},{"executor":"process","config":{"command":["sh","-c","printf '{}' > \"$AGENTFLOW_RESULT_FILE\""]}}`, "failure.kind == 'configuration_failed'")
	if !result.Valid {
		t.Fatalf("configuration fallback was rejected: %+v", result.Issues)
	}
}

func TestValidationRendersFixedSessionIDFromRepresentativeInputs(t *testing.T) {
	root := t.TempDir()
	flow := `{"version":"3","name":"session-input","inputs":{"sid":{"required":false}},"entry":"step","nodes":[{"id":"step","execution":{"candidates":[{"executor":"worker","session":{"mode":"fixed","id":"{{ inputs.sid }}"}},{"executor":"external"}]}}],"edges":[{"from":"step","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := `{"executors":{"worker":{"kind":"command","resumeCommand":["sh","-c","exit 0"]}}}`
	if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	result := ValidateWithInputs(root, "", nil)
	if result.Valid {
		t.Fatalf("an omitted optional fixed session id validated: %+v", result.Issues)
	}
	foundEmpty := false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "fixed session id resolved to an empty value") {
			foundEmpty = true
		}
	}
	if !foundEmpty {
		t.Fatalf("the rendered empty session id was not reported: %+v", result.Issues)
	}
	result = ValidateWithInputs(root, "", map[string]string{"sid": "existing-session"})
	if !result.Valid {
		t.Fatalf("a supplied fixed session id was rejected: %+v", result.Issues)
	}
}

func TestValidationResolvesExternalFixedIDBeforeAdapterPolicy(t *testing.T) {
	root := t.TempDir()
	flow := `{"version":"3","name":"external-session-input","inputs":{"sid":{"required":false}},"entry":"step","nodes":[{"id":"step","execution":{"fallback":{"when":"failure.kind == 'configuration_failed'"},"candidates":[{"executor":"external","session":{"mode":"fixed","id":"{{ inputs.sid }}"}},{"executor":"process","config":{"command":["sh","-c","exit 0"]}}]}}],"edges":[{"from":"step","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	result := ValidateWithInputs(root, "", nil)
	if result.Valid {
		t.Fatalf("external adapter failure masked a known-empty fixed id: %+v", result.Issues)
	}
	foundSession, foundUnreachable := false, false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "fixed session id resolved to an empty value") {
			foundSession = true
		}
		if strings.Contains(issue.Message, "does not select a session_incompatible failure") {
			foundUnreachable = true
		}
	}
	if !foundSession || !foundUnreachable {
		t.Fatalf("runtime failure precedence was not preserved: %+v", result.Issues)
	}
	result = ValidateWithInputs(root, "", map[string]string{"sid": "existing-session"})
	if !result.Valid {
		t.Fatalf("configuration fallback after a resolved external session was rejected: %+v", result.Issues)
	}
}

func TestValidationKeepsDownstreamCommandTemplatesInconclusive(t *testing.T) {
	root := t.TempDir()
	flow := `{"version":"3","name":"runtime-command","entry":"producer","nodes":[` +
		`{"id":"producer","execution":{"candidates":[{"executor":"process","config":{"command":["sh","-c","printf '{\"cmd\":\"sh\"}' > \"$AGENTFLOW_RESULT_FILE\""]}}]}},` +
		`{"id":"consumer","execution":{"candidates":[{"executor":"process","config":{"command":["{{ input.cmd }}","-c","exit 0"]}}]}}],` +
		`"edges":[{"from":"producer","to":"consumer"},{"from":"consumer","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	result := Validate(root, "")
	if !result.Valid {
		t.Fatalf("a future input-templated command was treated as deterministically empty: %+v", result.Issues)
	}
	found := false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "candidate config contains runtime templates") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the unresolved selected command was not reported as inconclusive: %+v", result.Issues)
	}
}

func TestValidationRendersMissingOptionalCommandLikeRuntime(t *testing.T) {
	for _, reference := range []string{"{{ inputs.cmd }}", "{{ input.cmd }}"} {
		t.Run(reference, func(t *testing.T) {
			root := t.TempDir()
			flow := `{"version":"3","name":"missing-command","inputs":{"cmd":{"required":false}},"entry":"step","nodes":[{"id":"step","execution":{"candidates":[` +
				`{"executor":"process","config":{"command":["` + reference + `","-c","exit 0"]}},` +
				`{"executor":"external"}]}}],"edges":[{"from":"step","to":"$end"}]}`
			if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
				t.Fatal(err)
			}
			result := ValidateWithInputs(root, "", nil)
			if result.Valid {
				t.Fatalf("known-missing command used the start-failure fallback: %+v", result.Issues)
			}
			foundConfiguration, foundUnreachable := false, false
			for _, issue := range result.Issues {
				if strings.Contains(issue.Message, "config.command") {
					foundConfiguration = true
				}
				if strings.Contains(issue.Message, "later candidates are unreachable") {
					foundUnreachable = true
				}
			}
			if !foundConfiguration || !foundUnreachable {
				t.Fatalf("typed missing command did not preserve runtime failure semantics: %+v", result.Issues)
			}
			if supplied := ValidateWithInputs(root, "", map[string]string{"cmd": "sh"}); !supplied.Valid {
				t.Fatalf("supplied optional command was rejected: %+v", supplied.Issues)
			}
		})
	}
}

func TestValidationRendersKnownMissingExecutorWorkspaceLikeRuntime(t *testing.T) {
	root := t.TempDir()
	flow := `{"version":"3","name":"missing-workspace-value","entry":"step","nodes":[{"id":"step","execution":{"fallback":{"when":"failure.kind == 'exit_code'"},"candidates":[{"executor":"worker"},{"executor":"external"}]}}],"edges":[{"from":"step","to":"$end"}]}`
	registry := `{"executors":{"worker":{"kind":"command","workspace":"missing/{{ config.dir }}","command":["sh","-c","exit 0"]}}}`
	for name, data := range map[string]string{"workflow.json": flow, "executors.json": registry} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result := Validate(root, "")
	if result.Valid {
		t.Fatalf("known-missing workspace value was treated as inconclusive: %+v", result.Issues)
	}
	foundWorkspace, foundUnreachable := false, false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "workspace unavailable") {
			foundWorkspace = true
		}
		if strings.Contains(issue.Message, "later candidates are unreachable") {
			foundUnreachable = true
		}
	}
	if !foundWorkspace || !foundUnreachable {
		t.Fatalf("rendered workspace failure did not follow runtime fallback semantics: %+v", result.Issues)
	}
}

func TestValidationRendersKnownMissingEntryInputWorkspaceLikeRuntime(t *testing.T) {
	for _, declared := range []bool{false, true} {
		name := "undeclared"
		inputs := ""
		if declared {
			name = "declared"
			inputs = `,"inputs":{"dir":{"type":"string"}}`
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			flow := `{"version":"3","name":"missing-entry-input-workspace"` + inputs + `,"entry":"step","nodes":[{"id":"step","execution":{"fallback":{"when":"failure.kind == 'exit_code'"},"candidates":[{"executor":"worker"},{"executor":"external"}]}}],"edges":[{"from":"step","to":"$end"}]}`
			registry := `{"executors":{"worker":{"kind":"command","workspace":"missing/{{ input.dir }}","command":["sh","-c","exit 0"]}}}`
			for filename, data := range map[string]string{"workflow.json": flow, "executors.json": registry} {
				if err := os.WriteFile(filepath.Join(root, filename), []byte(data), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			result := Validate(root, "")
			if result.Valid {
				t.Fatalf("known-missing entry input was treated as inconclusive: %+v", result.Issues)
			}
			foundWorkspace, foundUnreachable := false, false
			for _, issue := range result.Issues {
				if strings.Contains(issue.Message, "workspace unavailable") {
					foundWorkspace = true
				}
				if strings.Contains(issue.Message, "later candidates are unreachable") {
					foundUnreachable = true
				}
			}
			if !foundWorkspace || !foundUnreachable {
				t.Fatalf("entry input workspace did not follow runtime fallback semantics: %+v", result.Issues)
			}

			if err := os.MkdirAll(filepath.Join(root, "missing", "available"), 0o755); err != nil {
				t.Fatal(err)
			}
			if supplied := ValidateWithInputs(root, "", map[string]string{"dir": "available"}); !supplied.Valid {
				t.Fatalf("supplied entry input workspace was rejected: %+v", supplied.Issues)
			}
		})
	}
}

func TestValidationKeepsDownstreamExecutorWorkspaceInconclusive(t *testing.T) {
	root := t.TempDir()
	flow := `{"version":"3","name":"future-workspace","entry":"producer","nodes":[` +
		`{"id":"producer","execution":{"candidates":[{"executor":"process","config":{"command":["sh","-c","exit 0"]}}]}},` +
		`{"id":"consumer","execution":{"candidates":[{"executor":"worker"}]}}],` +
		`"edges":[{"from":"producer","to":"consumer"},{"from":"consumer","to":"$end"}]}`
	registry := `{"executors":{"worker":{"kind":"command","workspace":"{{ input.dir }}","command":["sh","-c","exit 0"]}}}`
	for name, data := range map[string]string{"workflow.json": flow, "executors.json": registry} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result := Validate(root, "")
	if !result.Valid {
		t.Fatalf("future downstream workspace was guessed unavailable: %+v", result.Issues)
	}
	found := false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "workspace contains runtime templates") {
			found = true
		}
	}
	if !found {
		t.Fatalf("future downstream workspace was not reported as inconclusive: %+v", result.Issues)
	}
}

func TestValidationResolvesSessionConfigurationBeforeWorkspace(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing-workspace")
	flow := `{"version":"3","name":"session","entry":"step","nodes":[{"id":"step","execution":{"candidates":[{"executor":"worker","session":{"mode":"fixed","id":"known-session"}},{"executor":"external"}]}}],"edges":[{"from":"step","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := `{"executors":{"worker":{"kind":"command","workspace":"` + missing + `","command":["sh","-c","exit 0"]}}}`
	if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	result := Validate(root, "")
	if result.Valid {
		t.Fatalf("configuration error was masked by the missing workspace: %+v", result.Issues)
	}
	foundConfiguration, foundWorkspace := false, false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "does not define resumeCommand") {
			foundConfiguration = true
		}
		if strings.Contains(issue.Message, "workspace unavailable") {
			foundWorkspace = true
		}
	}
	if !foundConfiguration || foundWorkspace {
		t.Fatalf("validation did not follow runtime session-before-workspace precedence: %+v", result.Issues)
	}
}

func TestValidationTreatsMessageDependentFallbackAsInconclusive(t *testing.T) {
	root := t.TempDir()
	// The synthetic validator message happens to match this policy; runtime
	// text differs, so validation must not treat the match as proof.
	flow := `{"version":"3","name":"message","entry":"step","nodes":[{"id":"step","execution":{"candidates":[{"executor":"worker"},{"executor":"external"}],"fallback":{"when":"let f = failure; f.message matches '^candidate executable unavailable:'"}}}],"edges":[{"from":"step","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := `{"executors":{"worker":{"kind":"command","command":["` + filepath.Join(root, "not-installed") + `"]}}}`
	if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	result := Validate(root, "")
	if !result.Valid {
		t.Fatalf("inconclusive message policy should not be rejected: %+v", result.Issues)
	}
	found := false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "inconclusive") {
			found = true
		}
	}
	if !found {
		t.Fatalf("synthetic message match was used to prove reachability: %+v", result.Issues)
	}
}

func TestValidationTreatsWholeFailureFallbackAsInconclusive(t *testing.T) {
	for _, expression := range []string{
		"toJSON(failure) contains 'candidate executable unavailable:'",
		"let f = failure; toJSON(f) contains 'candidate executable unavailable:'",
	} {
		root := t.TempDir()
		flow := `{"version":"3","name":"whole-failure","entry":"step","nodes":[{"id":"step","execution":{"candidates":[{"executor":"worker"},{"executor":"external"}],"fallback":{"when":"` + expression + `"}}}],"edges":[{"from":"step","to":"$end"}]}`
		if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
			t.Fatal(err)
		}
		registry := `{"executors":{"worker":{"kind":"command","command":["` + filepath.Join(root, "not-installed") + `"]}}}`
		if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		result := Validate(root, "")
		if !result.Valid {
			t.Fatalf("whole-object runtime dependency should remain inconclusive: %+v", result.Issues)
		}
		found := false
		for _, issue := range result.Issues {
			if strings.Contains(strings.ToLower(issue.Message), "inconclusive") {
				found = true
			}
		}
		if !found {
			t.Fatalf("whole-object failure read used synthetic data as proof: %+v", result.Issues)
		}
	}
}

// Session resolution happens before workspace/executable availability at
// runtime. Static validation must report the same deterministic session
// failure, otherwise a start_failed-only default fallback looks reachable
// while runtime selects no candidate at all.
func TestValidationRejectsEntrySessionInheritanceBeforeWorkspace(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing-workspace")
	flow := `{"version":"3","name":"session","entry":"step","nodes":[{"id":"step","execution":{"candidates":[{"executor":"agent","session":{"mode":"inherit"}},{"executor":"external"}]}}],"edges":[{"from":"step","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := `{"executors":{"agent":{"kind":"codex","workspace":"` + missing + `","command":["sh","-c","exit 0"]}}}`
	if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	result := Validate(root, "")
	if result.Valid {
		t.Fatalf("unresolvable entry session validated: %+v", result.Issues)
	}
	foundSession, foundWorkspace, foundUnreachable := false, false, false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "needs a session before the entry node runs") {
			foundSession = true
		}
		if strings.Contains(issue.Message, "workspace unavailable") {
			foundWorkspace = true
		}
		if strings.Contains(issue.Message, "unreachable") {
			foundUnreachable = true
		}
	}
	if !foundSession || foundWorkspace {
		t.Fatalf("validation did not resolve the session before workspace availability: %+v", result.Issues)
	}
	if !foundUnreachable {
		t.Fatalf("default fallback was not recognized as unable to select session_incompatible: %+v", result.Issues)
	}

	// The built-in external executor never records a session either, so an
	// entry candidate that inherits one can never reach waiting_input.
	flow = `{"version":"3","name":"external-inherit","entry":"step","nodes":[{"id":"step","execution":{"candidates":[{"executor":"external","session":{"mode":"inherit"}}]}}],"edges":[{"from":"step","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "executors.json")); err != nil {
		t.Fatal(err)
	}
	result = Validate(root, "")
	if result.Valid {
		t.Fatalf("entry external inherit validated: %+v", result.Issues)
	}
}

func TestValidationModelsInheritSourcePresenceAndProtocol(t *testing.T) {
	setup := func(flow, registry string) Validation {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		return Validate(root, "")
	}
	workspace := t.TempDir()
	command := `"command":["sh","-c","exit 0"],"resumeCommand":["sh","-c","exit 0"],"forkCommand":["sh","-c","exit 0"]`
	registry := `{"executors":{` +
		`"source":{"kind":"codex","workspace":"` + workspace + `",` + command + `},` +
		`"other":{"kind":"claude-code","workspace":"` + workspace + `",` + command + `}}}`

	// The explicit source is absent from the workflow, so no session can exist.
	flow := `{"version":"3","name":"missing","entry":"source","nodes":[{"id":"source","execution":{"candidates":[{"executor":"external"}]}},{"id":"target","execution":{"candidates":[{"executor":"external","session":{"mode":"inherit","from":"ghost"}}]}}],"edges":[{"from":"source","to":"target"},{"from":"target","to":"$end"}]}`
	result := setup(flow, registry)
	if result.Valid {
		t.Fatalf("missing inherit source validated: %+v", result.Issues)
	}

	// A source whose every candidate is external can never record a session.
	flow = `{"version":"3","name":"external-source","entry":"source","nodes":[{"id":"source","execution":{"candidates":[{"executor":"external"}]}},{"id":"target","execution":{"candidates":[{"executor":"external","session":{"mode":"fork","from":"source"}}]}}],"edges":[{"from":"source","to":"target"},{"from":"target","to":"$end"}]}`
	result = setup(flow, registry)
	if result.Valid {
		t.Fatalf("fork from a sessionless source validated: %+v", result.Issues)
	}

	// A recorded native:codex session cannot be inherited by external.
	flow = `{"version":"3","name":"protocol","entry":"source","nodes":[{"id":"source","execution":{"candidates":[{"executor":"source"}]}},{"id":"target","execution":{"candidates":[{"executor":"external","session":{"mode":"inherit","from":"source"}}]}}],"edges":[{"from":"source","to":"target"},{"from":"target","to":"$end"}]}`
	result = setup(flow, registry)
	if result.Valid {
		t.Fatalf("incompatible protocol validated: %+v", result.Issues)
	}

	// The same native provider is compatible.
	flow = `{"version":"3","name":"compatible","entry":"source","nodes":[{"id":"source","execution":{"candidates":[{"executor":"source"}]}},{"id":"target","execution":{"candidates":[{"executor":"source","session":{"mode":"inherit","from":"source"}}]}}],"edges":[{"from":"source","to":"target"},{"from":"target","to":"$end"}]}`
	result = setup(flow, registry)
	if !result.Valid {
		t.Fatalf("compatible provider was rejected: %+v", result.Issues)
	}

	// A source with candidates that could record different protocols is
	// indeterminate and must stay usable.
	flow = `{"version":"3","name":"indeterminate","entry":"source","nodes":[{"id":"source","execution":{"candidates":[{"executor":"source"},{"executor":"other"}]}},{"id":"target","execution":{"candidates":[{"executor":"source","session":{"mode":"inherit","from":"source"}}]}}],"edges":[{"from":"source","to":"target"},{"from":"target","to":"$end"}]}`
	result = setup(flow, registry)
	if !result.Valid {
		t.Fatalf("indeterminate source protocol was rejected: %+v", result.Issues)
	}

	// A constant-false fallback makes later source candidates unreachable, so
	// they cannot hide the first candidate's incompatible protocol.
	flow = `{"version":"3","name":"unreachable-protocol","entry":"source","nodes":[{"id":"source","execution":{"fallback":{"when":"false && failure.kind == 'exit_code'"},"candidates":[{"executor":"source"},{"executor":"other"}]}},{"id":"target","execution":{"candidates":[{"executor":"other","session":{"mode":"inherit","from":"source"}}]}}],"edges":[{"from":"source","to":"target"},{"from":"target","to":"$end"}]}`
	result = setup(flow, registry)
	if result.Valid {
		t.Fatalf("unreachable compatible source protocol masked the selected incompatible protocol: %+v", result.Issues)
	}

	flow = strings.Replace(flow, `"when":"false && failure.kind == 'exit_code'"`, `"when":"true"`, 1)
	result = setup(flow, registry)
	if !result.Valid {
		t.Fatalf("reachable alternative source protocol was rejected: %+v", result.Issues)
	}

	// Once the first candidate has resolved a fresh session and prepared its
	// command, it cannot fail with session_incompatible. Such a policy cannot
	// make the later provider a possible source protocol.
	flow = `{"version":"3","name":"impossible-source-failure","entry":"source","nodes":[{"id":"source","execution":{"fallback":{"when":"failure.kind == 'session_incompatible'"},"candidates":[{"executor":"source"},{"executor":"other"}]}},{"id":"target","execution":{"candidates":[{"executor":"other","session":{"mode":"inherit","from":"source"}}]}}],"edges":[{"from":"source","to":"target"},{"from":"target","to":"$end"}]}`
	result = setup(flow, registry)
	if result.Valid {
		t.Fatalf("an impossible post-launch failure contributed a source protocol: %+v", result.Issues)
	}
}

func TestValidationUsesOnlyReachableSuccessfulSourceCandidates(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing-worker")
	flow := `{"version":"3","name":"source-fallback","entry":"source","nodes":[` +
		`{"id":"source","execution":{"candidates":[{"executor":"worker"},{"executor":"external"}]}},` +
		`{"id":"target","execution":{"candidates":[{"executor":"worker","session":{"mode":"inherit","from":"source"}}]}}],` +
		`"edges":[{"from":"source","to":"target"},{"from":"target","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := `{"executors":{"worker":{"kind":"command","command":["` + missing + `"],"resumeCommand":["sh","-c","exit 0"]}}}`
	if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	result := Validate(root, "")
	if result.Valid {
		t.Fatalf("a source completed through a sessionless external fallback validated: %+v", result.Issues)
	}
	found := false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "cannot resolve a compatible session") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the unreachable source command was counted as a session source: %+v", result.Issues)
	}

	registry = `{"executors":{"worker":{"kind":"command","command":["sh","-c","exit 0"],"resumeCommand":["sh","-c","exit 0"]}}}`
	if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	result = Validate(root, "")
	if !result.Valid {
		t.Fatalf("an available compatible source candidate was rejected: %+v", result.Issues)
	}
}

func TestValidationPropagatesTransitiveSourceProtocols(t *testing.T) {
	root := t.TempDir()
	command := `"command":["sh","-c","exit 0"],"resumeCommand":["sh","-c","exit 0"],"forkCommand":["sh","-c","exit 0"]`
	registry := `{"executors":{"codex-a":{"kind":"codex",` + command + `},"codex-b":{"kind":"codex",` + command + `},"claude":{"kind":"claude-code",` + command + `}}}`
	if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	flow := `{"version":"3","name":"transitive-protocol","entry":"a","nodes":[` +
		`{"id":"a","execution":{"candidates":[{"executor":"codex-a"}]}},` +
		`{"id":"b","execution":{"candidates":[{"executor":"codex-b","session":{"mode":"inherit","from":"a"}}]}},` +
		`{"id":"c","execution":{"candidates":[{"executor":"claude","session":{"mode":"inherit","from":"b"}}]}}],` +
		`"edges":[{"from":"a","to":"b"},{"from":"b","to":"c"},{"from":"c","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	result := Validate(root, "")
	if result.Valid {
		t.Fatalf("a transitive native protocol mismatch validated: %+v", result.Issues)
	}

	flow = strings.Replace(flow, `"executor":"claude"`, `"executor":"codex-b"`, 1)
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	result = Validate(root, "")
	if !result.Valid {
		t.Fatalf("a compatible transitive native protocol was rejected: %+v", result.Issues)
	}

	// If the inherited candidate is deterministically impossible and its
	// reachable fallback is external, the intermediate node supplies no session.
	flow = `{"version":"3","name":"transitive-external","entry":"a","nodes":[` +
		`{"id":"a","execution":{"candidates":[{"executor":"external"}]}},` +
		`{"id":"b","execution":{"fallback":{"when":"failure.kind == 'session_incompatible'"},"candidates":[{"executor":"codex-b","session":{"mode":"inherit","from":"a"}},{"executor":"external"}]}},` +
		`{"id":"c","execution":{"candidates":[{"executor":"codex-b","session":{"mode":"inherit","from":"b"}}]}}],` +
		`"edges":[{"from":"a","to":"b"},{"from":"b","to":"c"},{"from":"c","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	result = Validate(root, "")
	if result.Valid {
		t.Fatalf("a transitive sessionless external fallback validated: %+v", result.Issues)
	}
}

func TestValidationChecksFixedResumeOnGuaranteedRevisit(t *testing.T) {
	setup := func(candidates, fallback string) Validation {
		root := t.TempDir()
		fallbackField := ""
		if fallback != "" {
			fallbackField = `"fallback":{"when":"` + fallback + `"},`
		}
		flow := `{"version":"3","name":"fixed-cycle","entry":"step","nodes":[{"id":"step","execution":{` + fallbackField + `"candidates":[` + candidates + `]}}],"edges":[{"from":"step","to":"step","maxIterations":1}]}`
		if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
			t.Fatal(err)
		}
		registry := `{"executors":{"worker":{"kind":"command","command":["sh","-c","exit 0"]}}}`
		if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		return Validate(root, "")
	}
	fixed := `{"executor":"worker","session":{"mode":"fixed"}}`
	result := setup(fixed, "")
	if result.Valid {
		t.Fatalf("a guaranteed fixed-session revisit without resumeCommand validated: %+v", result.Issues)
	}
	foundResume := false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "guaranteed revisit") && strings.Contains(issue.Message, "resumeCommand") {
			foundResume = true
		}
	}
	if !foundResume {
		t.Fatalf("the guaranteed resume failure was not reported: %+v", result.Issues)
	}

	result = setup(fixed+`,{"executor":"external"}`, "failure.kind == 'configuration_failed'")
	if !result.Valid {
		t.Fatalf("an explicit fallback for the guaranteed resume failure was rejected: %+v", result.Issues)
	}

	conditional := func(when string, inputs map[string]string) Validation {
		root := t.TempDir()
		inputDefinition := ""
		if strings.Contains(when, "inputs.loop") {
			inputDefinition = `"inputs":{"loop":{"required":false}},`
		}
		flow := `{"version":"3","name":"conditional-fixed-cycle",` + inputDefinition + `"entry":"step","nodes":[{"id":"step","execution":{"candidates":[` + fixed + `]}}],"edges":[{"from":"step","to":"step","when":"` + when + `","maxIterations":1}]}`
		if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
			t.Fatal(err)
		}
		registry := `{"executors":{"worker":{"kind":"command","command":["sh","-c","exit 0"]}}}`
		if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		return ValidateWithInputs(root, "", inputs)
	}
	if result = conditional("true", nil); result.Valid {
		t.Fatalf("a literal-true guaranteed revisit skipped resume validation: %+v", result.Issues)
	}
	if result = conditional("inputs.loop == 'yes'", map[string]string{"loop": "yes"}); result.Valid {
		t.Fatalf("a representative-input guaranteed revisit skipped resume validation: %+v", result.Issues)
	}
	if result = conditional("inputs.loop == 'yes'", map[string]string{"loop": "no"}); !result.Valid {
		t.Fatalf("a statically false revisit was treated as guaranteed: %+v", result.Issues)
	}
}

func TestValidationDoesNotInferMessageDependenceFromAStringLiteral(t *testing.T) {
	root := t.TempDir()
	flow := `{"version":"3","name":"message","entry":"step","nodes":[{"id":"step","execution":{"candidates":[{"executor":"worker"},{"executor":"external"}],"fallback":{"when":"failure.kind == 'some_message'"}}}],"edges":[{"from":"step","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := `{"executors":{"worker":{"kind":"command","command":["` + filepath.Join(root, "not-installed") + `"]}}}`
	if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	result := Validate(root, "")
	if result.Valid {
		t.Fatalf("unreachable fallback validated: %+v", result.Issues)
	}
	foundUnreachable, foundInconclusive := false, false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "unreachable") {
			foundUnreachable = true
		}
		if strings.Contains(issue.Message, "inconclusive") {
			foundInconclusive = true
		}
	}
	if !foundUnreachable || foundInconclusive {
		t.Fatalf("string literal was mistaken for a runtime message dependency: %+v", result.Issues)
	}
}

// Native adapters resolve their configured commands exactly like
// driver.BuildCommand: a configured base command never satisfies a resume or
// fork requirement, and availability is checked for the command the session
// mode actually launches.
func TestValidationUsesTheNativeCommandSelectedForTheSessionMode(t *testing.T) {
	setup := func(flow, registry string) Validation {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		return Validate(root, "")
	}
	workspace := t.TempDir()
	missingBinary := filepath.Join(t.TempDir(), "missing-binary")
	fixedFlow := `{"version":"3","name":"native","entry":"step","nodes":[{"id":"step","execution":{"candidates":[{"executor":"native","session":{"mode":"fixed","id":"known-session"}}]}}],"edges":[{"from":"step","to":"$end"}]}`
	freshFixedFlow := `{"version":"3","name":"native","entry":"step","nodes":[{"id":"step","execution":{"candidates":[{"executor":"native","session":{"mode":"fixed"}}]}}],"edges":[{"from":"step","to":"$end"}]}`

	for _, kind := range []string{"codex", "claude-code"} {
		// A base command without resumeCommand is a deterministic configuration
		// failure, even when the configured base command exists or is missing.
		for _, base := range []string{`"` + missingBinary + `"`, `"sh"`} {
			registry := `{"executors":{"native":{"kind":"` + kind + `","workspace":"` + workspace + `","command":[` + base + `]}}}`
			result := setup(fixedFlow, registry)
			if result.Valid {
				t.Fatalf("%s fixed session with only a base command validated: %+v", kind, result.Issues)
			}
			foundConfiguration, foundAvailability := false, false
			for _, issue := range result.Issues {
				if strings.Contains(issue.Message, "does not define resumeCommand") {
					foundConfiguration = true
				}
				if strings.Contains(issue.Message, "executable unavailable") {
					foundAvailability = true
				}
			}
			if !foundConfiguration || foundAvailability {
				t.Fatalf("%s did not report the selected-command configuration error first: %+v", kind, result.Issues)
			}
		}

		// The base command may be unavailable when resumeCommand exists; only
		// the launched command is checked.
		registry := `{"executors":{"native":{"kind":"` + kind + `","workspace":"` + workspace + `","command":["` + missingBinary + `"],"resumeCommand":["sh","-c","exit 0"]}}}`
		result := setup(fixedFlow, registry)
		if !result.Valid {
			t.Fatalf("%s rejected an available resumeCommand because the unused base command is missing: %+v", kind, result.Issues)
		}

		// Without an explicit id, the first visit is fresh. With no configured
		// base command, BuildCommand launches the native binary even when a
		// resumeCommand is present.
		registry = `{"executors":{"native":{"kind":"` + kind + `","workspace":"` + workspace + `","binary":"` + missingBinary + `","resumeCommand":["sh","-c","exit 0"]}}}`
		result = setup(freshFixedFlow, registry)
		if result.Valid {
			t.Fatalf("%s fixed first visit checked resumeCommand instead of the missing native binary: %+v", kind, result.Issues)
		}

		// Conversely, an available fresh native command is enough for an
		// acyclic first visit; an unused future resumeCommand is not launched.
		registry = `{"executors":{"native":{"kind":"` + kind + `","workspace":"` + workspace + `","command":["sh","-c","exit 0"],"resumeCommand":["` + missingBinary + `"]}}}`
		result = setup(freshFixedFlow, registry)
		if !result.Valid {
			t.Fatalf("%s rejected the available fresh command because a future resumeCommand is missing: %+v", kind, result.Issues)
		}
	}

	// Generic commands have no native binary fallback. A resumeCommand alone
	// cannot bootstrap a fixed session because the first visit has no id.
	registry := `{"executors":{"native":{"kind":"command","workspace":"` + workspace + `","resumeCommand":["sh","-c","exit 0"]}}}`
	result := setup(freshFixedFlow, registry)
	if result.Valid {
		t.Fatalf("command fixed first visit validated without a fresh command: %+v", result.Issues)
	}
	foundFreshFailure := false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "needs a configured command or config.command") {
			foundFreshFailure = true
		}
	}
	if !foundFreshFailure {
		t.Fatalf("missing fresh command was not reported: %+v", result.Issues)
	}

	// Codex rejects any existing session while ephemeral is enabled. The
	// configuration error precedes the missing executable.
	registry = `{"executors":{"native":{"kind":"codex","ephemeral":true,"workspace":"` + workspace + `","binary":"` + missingBinary + `"}}}`
	result = setup(fixedFlow, registry)
	if result.Valid {
		t.Fatalf("ephemeral codex accepted an existing session: %+v", result.Issues)
	}
	foundEphemeral, foundAvailability := false, false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "while ephemeral is enabled") {
			foundEphemeral = true
		}
		if strings.Contains(issue.Message, "executable unavailable") {
			foundAvailability = true
		}
	}
	if !foundEphemeral || foundAvailability {
		t.Fatalf("ephemeral session configuration was masked by availability: %+v", result.Issues)
	}
}

// An explicit session source must be able to complete before the target visit.
// Reachability alone is insufficient for parallel siblings, and a cycle must
// have a real first-visit bootstrap path before history can help later visits.
func TestValidationModelsExplicitSourceOrdering(t *testing.T) {
	setup := func(flow string) Validation {
		root := t.TempDir()
		workspace := t.TempDir()
		command := `{"kind":"command","workspace":"` + workspace + `","command":["sh","-c","exit 0"],"resumeCommand":["sh","-c","exit 0"],"forkCommand":["sh","-c","exit 0"]}`
		registry := `{"executors":{"worker":` + command + `,"bootstrap":` + command + `}}`
		if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		return Validate(root, "")
	}
	inherit := `{"executor":"worker","session":{"mode":"inherit","from":"later"}}`

	// seed -> work -> later, and work inherits from later.
	flow := `{"version":"3","name":"future","entry":"seed","nodes":[` +
		`{"id":"seed","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"work","execution":{"candidates":[` + inherit + `]}},` +
		`{"id":"later","execution":{"candidates":[{"executor":"worker"}]}}],` +
		`"edges":[{"from":"seed","to":"work"},{"from":"work","to":"later"},{"from":"later","to":"$end"}]}`
	result := setup(flow)
	if result.Valid {
		t.Fatalf("a descendant session source validated: %+v", result.Issues)
	}
	found := false
	for _, issue := range result.Issues {
		if strings.Contains(issue.Message, "not guaranteed to complete before node work") {
			found = true
		}
	}
	if !found {
		t.Fatalf("source ordering was not reported: %+v", result.Issues)
	}

	// A disconnected source never runs.
	flow = `{"version":"3","name":"unreachable","entry":"seed","nodes":[` +
		`{"id":"seed","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"work","execution":{"candidates":[` + inherit + `]}},` +
		`{"id":"later","execution":{"candidates":[{"executor":"worker"}]}}],` +
		`"edges":[{"from":"seed","to":"work"},{"from":"work","to":"$end"}]}`
	result = setup(flow)
	if result.Valid {
		t.Fatalf("a never-executed session source validated: %+v", result.Issues)
	}

	// Merely adding a back edge does not bootstrap the first work visit: later
	// cannot record a session until work has already succeeded.
	flow = `{"version":"3","name":"cycle","entry":"seed","nodes":[` +
		`{"id":"seed","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"work","execution":{"candidates":[` + inherit + `]}},` +
		`{"id":"later","execution":{"candidates":[{"executor":"worker"}]}}],` +
		`"edges":[{"from":"seed","to":"work"},{"from":"work","to":"later"},{"from":"later","to":"work","maxIterations":2}]}`
	result = setup(flow)
	if result.Valid {
		t.Fatalf("an unbootstrapped cyclic source validated: %+v", result.Issues)
	}

	// The same cycle is runnable when the first visit can explicitly fall back
	// to a fresh-session candidate. That visit reaches later, so the next work
	// visit can use the preferred inherited session.
	flow = `{"version":"3","name":"fallback-bootstrap","entry":"seed","nodes":[` +
		`{"id":"seed","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"work","execution":{"fallback":{"when":"failure.kind == 'session_incompatible'"},"candidates":[` + inherit + `,{"executor":"bootstrap"}]}},` +
		`{"id":"later","execution":{"candidates":[{"executor":"worker"}]}}],` +
		`"edges":[{"from":"seed","to":"work"},{"from":"work","to":"later"},{"from":"later","to":"work","maxIterations":2}]}`
	result = setup(flow)
	if !result.Valid {
		t.Fatalf("a cycle with a fresh-session bootstrap fallback was rejected: %+v", result.Issues)
	}

	// A genuine bootstrap visits later before work. The back edge can then
	// reactivate work with the already-recorded source session.
	flow = `{"version":"3","name":"bootstrapped-cycle","entry":"seed","nodes":[` +
		`{"id":"seed","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"work","execution":{"candidates":[` + inherit + `]}},` +
		`{"id":"later","execution":{"candidates":[{"executor":"worker"}]}}],` +
		`"edges":[{"from":"seed","to":"later"},{"from":"later","to":"work"},{"from":"work","to":"later","maxIterations":2}]}`
	result = setup(flow)
	if !result.Valid {
		t.Fatalf("a genuinely bootstrapped cyclic source was rejected: %+v", result.Issues)
	}

	// dispatch=all siblings are prepared from the same state snapshot, so the
	// source sibling has not completed when target resolves its session.
	flow = `{"version":"3","name":"parallel-siblings","entry":"seed","nodes":[` +
		`{"id":"seed","dispatch":"all","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"work","execution":{"candidates":[{"executor":"worker","session":{"mode":"inherit","from":"later"}}]}},` +
		`{"id":"later","execution":{"candidates":[{"executor":"worker"}]}}],` +
		`"edges":[{"from":"seed","to":"later"},{"from":"seed","to":"work"},{"from":"later","to":"$end"},{"from":"work","to":"$end"}]}`
	result = setup(flow)
	if result.Valid {
		t.Fatalf("a same-wave parallel source validated: %+v", result.Issues)
	}

	// A join is a real completion barrier: each join.from predecessor has
	// finished before the target is activated.
	flow = `{"version":"3","name":"join-source","entry":"seed","nodes":[` +
		`{"id":"seed","dispatch":"all","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"later","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"other","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"work","join":{"from":["later","other"]},"execution":{"candidates":[{"executor":"worker","session":{"mode":"inherit","from":"later"}}]}}],` +
		`"edges":[{"from":"seed","to":"later"},{"from":"seed","to":"other"},{"from":"later","to":"work"},{"from":"other","to":"work"},{"from":"work","to":"$end"}]}`
	result = setup(flow)
	if !result.Valid {
		t.Fatalf("a completed join predecessor was rejected: %+v", result.Issues)
	}

	// A completed source can dispatch the target in the following wave; this
	// is ordered even if that dispatch also starts another branch.
	flow = `{"version":"3","name":"prior-wave","entry":"seed","nodes":[` +
		`{"id":"seed","dispatch":"all","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"later","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"other","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"work","execution":{"candidates":[{"executor":"worker","session":{"mode":"inherit","from":"later"}}]}}],` +
		`"edges":[{"from":"seed","to":"later"},{"from":"seed","to":"other"},{"from":"later","to":"$end"},{"from":"other","to":"work"},{"from":"work","to":"$end"}]}`
	result = setup(flow)
	if !result.Valid {
		t.Fatalf("a source completed in the prior wave was rejected: %+v", result.Issues)
	}
}

func TestValidationUsesSelectedRouteForExplicitSourceOrdering(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	command := `{"kind":"command","workspace":"` + workspace + `","command":["sh","-c","exit 0"],"resumeCommand":["sh","-c","exit 0"]}`
	registry := `{"executors":{"worker":` + command + `}}`
	flow := `{"version":"3","name":"selected-source","inputs":{"route":{"required":true}},"entry":"router","nodes":[` +
		`{"id":"router","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"source","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"bypass","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"target","execution":{"candidates":[{"executor":"worker","session":{"mode":"inherit","from":"source"}}]}}],` +
		`"edges":[{"from":"router","to":"source","when":"inputs.route == 'source'"},{"from":"router","to":"bypass"},` +
		`{"from":"source","to":"target"},{"from":"bypass","to":"target"},{"from":"target","to":"$end"}]}`
	for name, data := range map[string]string{"workflow.json": flow, "executors.json": registry} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	selected := ValidateWithInputs(root, "", map[string]string{"route": "source"})
	if !selected.Valid {
		t.Fatalf("selected source route was rejected by an unselected bypass: %+v", selected.Issues)
	}
	bypassed := ValidateWithInputs(root, "", map[string]string{"route": "bypass"})
	if bypassed.Valid {
		t.Fatalf("selected bypass fabricated source completion: %+v", bypassed.Issues)
	}
}

func TestValidationPropagatesSelectedRouteThroughExplicitSessionSource(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	command := `{"kind":"command","workspace":"` + workspace + `","command":["sh","-c","exit 0"],"resumeCommand":["sh","-c","exit 0"]}`
	registry := `{"executors":{"worker":` + command + `}}`
	flow := `{"version":"3","name":"transitive-explicit-route","inputs":{"route":{"required":true}},"entry":"router","nodes":[` +
		`{"id":"router","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"source","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"bypass","execution":{"candidates":[{"executor":"worker"}]}},` +
		`{"id":"bridge","execution":{"candidates":[{"executor":"worker","session":{"mode":"inherit","from":"source"}}]}},` +
		`{"id":"target","execution":{"candidates":[{"executor":"worker","session":{"mode":"inherit","from":"bridge"}}]}}],` +
		`"edges":[{"from":"router","to":"source","when":"inputs.route == 'source'"},{"from":"router","to":"bypass"},` +
		`{"from":"source","to":"bridge"},{"from":"bypass","to":"bridge"},{"from":"bridge","to":"target"},{"from":"target","to":"$end"}]}`
	for name, data := range map[string]string{"workflow.json": flow, "executors.json": registry} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	selected := ValidateWithInputs(root, "", map[string]string{"route": "source"})
	if !selected.Valid {
		t.Fatalf("transitive explicit source lost its selected route: %+v", selected.Issues)
	}
	bypassed := ValidateWithInputs(root, "", map[string]string{"route": "bypass"})
	if bypassed.Valid {
		t.Fatalf("transitive explicit source fabricated completion on bypass: %+v", bypassed.Issues)
	}
}

// The external adapter waits for another application and has no provider
// session lifecycle. Every non-new policy is a configuration error, and a
// configured command is rejected before waiting.
func TestValidationRejectsExternalSessionPolicies(t *testing.T) {
	setup := func(candidate, registry string) Validation {
		root := t.TempDir()
		flow := `{"version":"3","name":"external","entry":"step","nodes":[{"id":"step","execution":{"candidates":[` + candidate + `]}}],"edges":[{"from":"step","to":"$end"}]}`
		if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
			t.Fatal(err)
		}
		if registry != "" {
			if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return Validate(root, "")
	}
	for _, session := range []string{`{"mode":"fixed"}`, `{"mode":"fixed","id":"known-session"}`, `{"mode":"fork"}`} {
		result := setup(`{"executor":"external","session":`+session+`}`, "")
		if result.Valid {
			t.Fatalf("external session policy %s validated: %+v", session, result.Issues)
		}
		// A fork at the entry node fails session resolution before the adapter
		// policy is consulted; both outcomes reject the candidate.
		if !strings.Contains(session, "fork") {
			found := false
			for _, issue := range result.Issues {
				if strings.Contains(issue.Message, "does not support session mode") {
					found = true
				}
			}
			if !found {
				t.Fatalf("external session policy %s was not reported: %+v", session, result.Issues)
			}
		}
	}
	result := setup(`{"executor":"external"}`, `{"executors":{"external":{"kind":"external","command":["sh"]}}}`)
	if result.Valid {
		t.Fatalf("external executor with a command validated: %+v", result.Issues)
	}
}

func TestValidationReportsAttemptSensitiveGuaranteedRevisit(t *testing.T) {
	root := t.TempDir()
	workspace := t.TempDir()
	registry := `{"executors":{"missing":{"kind":"command","workspace":"` + workspace + `","command":["` + filepath.Join(workspace, "missing") + `"]},"worker":{"kind":"command","workspace":"` + workspace + `","command":["sh","-c","exit 0"]}}}`
	flow := `{"version":"3","name":"attempt-revisit","entry":"work","nodes":[{"id":"work","execution":{"fallback":{"when":"let f = failure; f.kind == 'start_failed' && f.attempt == 1"},"candidates":[{"executor":"missing"},{"executor":"worker"}]}}],"edges":[{"from":"work","to":"work","when":"true","maxIterations":1},{"from":"work","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	result := Validate(root, "")
	if !result.Valid {
		t.Fatalf("an inconclusive revisit was rejected instead of diagnosed: %+v", result.Issues)
	}
	found := false
	for _, issue := range result.Issues {
		message := strings.ToLower(issue.Message)
		if strings.Contains(message, "attempt") && strings.Contains(message, "revisit") && strings.Contains(message, "inconclusive") {
			found = true
		}
	}
	if !found {
		t.Fatalf("cross-visit attempt uncertainty was not reported: %+v", result.Issues)
	}
}

func TestValidationUsesSelectedImplicitSessionRoute(t *testing.T) {
	setup := func(pool bool, route string) Validation {
		root := t.TempDir()
		workspace := t.TempDir()
		command := `["sh","-c","exit 0"]`
		registry := `{"executors":{"router":{"kind":"command","workspace":"` + workspace + `","command":` + command + `},"a":{"kind":"codex","workspace":"` + workspace + `","command":` + command + `,"resumeCommand":` + command + `},"b":{"kind":"claude-code","workspace":"` + workspace + `","command":` + command + `,"resumeCommand":` + command + `},"target-codex":{"kind":"codex","workspace":"` + workspace + `","command":` + command + `,"resumeCommand":` + command + `},"target-claude":{"kind":"claude-code","workspace":"` + workspace + `","command":` + command + `,"resumeCommand":` + command + `}}}`
		candidates := `[{"executor":"target-codex","session":{"mode":"inherit"}}]`
		fallback := ""
		if pool {
			candidates = `[{"executor":"target-codex","session":{"mode":"inherit"}},{"executor":"target-claude","session":{"mode":"inherit"}}]`
			fallback = `"fallback":{"when":"failure.kind == 'session_incompatible'"},`
		}
		flow := `{"version":"3","name":"implicit-route","inputs":{"route":{"type":"string","required":true}},"entry":"router","nodes":[{"id":"router","execution":{"candidates":[{"executor":"router"}]}},{"id":"a","execution":{"candidates":[{"executor":"a"}]}},{"id":"b","execution":{"candidates":[{"executor":"b"}]}},{"id":"work","execution":{` + fallback + `"candidates":` + candidates + `}}],"edges":[{"from":"router","to":"a","when":"inputs.route == 'codex'"},{"from":"router","to":"b"},{"from":"a","to":"work"},{"from":"b","to":"work"},{"from":"work","to":"$end"}]}`
		if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
			t.Fatal(err)
		}
		inputs := map[string]string{}
		if route != "" {
			inputs["route"] = route
		}
		return ValidateWithInputs(root, "", inputs)
	}
	if result := setup(false, "codex"); !result.Valid {
		t.Fatalf("selected compatible route was rejected: %+v", result.Issues)
	}
	if result := setup(false, "claude"); result.Valid {
		t.Fatalf("compatible unselected route masked the selected incompatible route: %+v", result.Issues)
	}
	for _, route := range []string{"codex", "claude"} {
		if result := setup(true, route); !result.Valid {
			t.Fatalf("route-specific fallback pool rejected %s: %+v", route, result.Issues)
		}
	}
	result := setup(false, "")
	if result.Valid {
		t.Fatalf("unresolved incompatible route was silently accepted: %+v", result.Issues)
	}
	found := false
	for _, issue := range result.Issues {
		if strings.Contains(strings.ToLower(issue.Message), "incoming route is unresolved") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unresolved incoming route was not reported: %+v", result.Issues)
	}
}

func TestValidationPropagatesSelectedRouteThroughSessionSources(t *testing.T) {
	setup := func(inputs map[string]string, targetFallback bool) Validation {
		root := t.TempDir()
		workspace := t.TempDir()
		command := `["sh","-c","exit 0"]`
		registry := `{"executors":{"router":{"kind":"command","workspace":"` + workspace + `","command":` + command + `},"a":{"kind":"codex","workspace":"` + workspace + `","command":` + command + `,"resumeCommand":` + command + `},"b":{"kind":"claude-code","workspace":"` + workspace + `","command":` + command + `,"resumeCommand":` + command + `},"source-codex":{"kind":"codex","workspace":"` + workspace + `","command":` + command + `,"resumeCommand":` + command + `},"source-claude":{"kind":"claude-code","workspace":"` + workspace + `","command":` + command + `,"resumeCommand":` + command + `},"target":{"kind":"codex","workspace":"` + workspace + `","command":` + command + `,"resumeCommand":` + command + `}}}`
		targetExecution := `{"candidates":[{"executor":"target","session":{"mode":"inherit","from":"source"}}]}`
		if targetFallback {
			targetExecution = `{"fallback":{"when":"failure.kind == 'session_incompatible'"},"candidates":[{"executor":"target","session":{"mode":"inherit","from":"source"}},{"executor":"source-claude","session":{"mode":"inherit","from":"source"}}]}`
		}
		flow := `{"version":"3","name":"transitive-route","inputs":{"route":{"type":"string","required":true}},"entry":"router","nodes":[{"id":"router","execution":{"candidates":[{"executor":"router"}]}},{"id":"a","execution":{"candidates":[{"executor":"a"}]}},{"id":"b","execution":{"candidates":[{"executor":"b"}]}},{"id":"source","execution":{"fallback":{"when":"failure.kind == 'session_incompatible'"},"candidates":[{"executor":"source-codex","session":{"mode":"inherit"}},{"executor":"source-claude","session":{"mode":"inherit"}}]}},{"id":"work","execution":` + targetExecution + `}],"edges":[{"from":"router","to":"a","when":"inputs.route == 'codex'"},{"from":"router","to":"b"},{"from":"a","to":"source"},{"from":"b","to":"source"},{"from":"source","to":"work"},{"from":"work","to":"$end"}]}`
		if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
			t.Fatal(err)
		}
		return ValidateWithInputs(root, "", inputs)
	}
	if result := setup(map[string]string{"route": "codex"}, false); !result.Valid {
		t.Fatalf("selected Codex route was rejected: %+v", result.Issues)
	}
	if result := setup(map[string]string{"route": "claude"}, false); result.Valid {
		t.Fatalf("unselected Codex predecessor masked the transitive Claude protocol: %+v", result.Issues)
	}
	if result := setup(nil, false); result.Valid {
		t.Fatalf("a compatible source route masked another reachable incompatible route: %+v", result.Issues)
	}
	if result := setup(nil, true); !result.Valid {
		t.Fatalf("route-specific target fallback was rejected: %+v", result.Issues)
	}
}

func TestValidationChecksImplicitSessionRoutesOnRevisit(t *testing.T) {
	setup := func(withFallback bool) Validation {
		root := t.TempDir()
		workspace := t.TempDir()
		command := `["sh","-c","exit 0"]`
		registry := `{"executors":{"a":{"kind":"codex","workspace":"` + workspace + `","command":` + command + `,"resumeCommand":` + command + `},"b":{"kind":"claude-code","workspace":"` + workspace + `","command":` + command + `,"resumeCommand":` + command + `},"work-codex":{"kind":"codex","workspace":"` + workspace + `","command":` + command + `,"resumeCommand":` + command + `},"work-claude":{"kind":"claude-code","workspace":"` + workspace + `","command":` + command + `,"resumeCommand":` + command + `}}}`
		candidates := `[{"executor":"work-codex","session":{"mode":"inherit"}}]`
		fallback := ""
		if withFallback {
			candidates = `[{"executor":"work-codex","session":{"mode":"inherit"}},{"executor":"work-claude","session":{"mode":"inherit"}}]`
			fallback = `"fallback":{"when":"failure.kind == 'session_incompatible'"},`
		}
		flow := `{"version":"3","name":"implicit-revisit","entry":"a","nodes":[{"id":"a","execution":{"candidates":[{"executor":"a"}]}},{"id":"work","execution":{` + fallback + `"candidates":` + candidates + `}},{"id":"b","execution":{"candidates":[{"executor":"b"}]}}],"edges":[{"from":"a","to":"work"},{"from":"work","to":"b","when":"true","maxIterations":1},{"from":"work","to":"$end"},{"from":"b","to":"work"}]}`
		if err := os.WriteFile(filepath.Join(root, "executors.json"), []byte(registry), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
			t.Fatal(err)
		}
		return Validate(root, "")
	}
	if result := setup(false); result.Valid {
		t.Fatalf("an incompatible implicit session on revisit validated: %+v", result.Issues)
	}
	if result := setup(true); !result.Valid {
		t.Fatalf("a revisit with a compatible configured fallback was rejected: %+v", result.Issues)
	}
}

func TestValidationUsesEntryInputAliasesForRevisits(t *testing.T) {
	for _, expression := range []string{"input.loop == 'yes'", "nodes.work.input.loop == 'yes'"} {
		for _, loop := range []string{"yes", "no"} {
			root := t.TempDir()
			flow := `{"version":"3","name":"entry-alias","inputs":{"loop":{"type":"string"}},"entry":"work","nodes":[{"id":"work","execution":{"candidates":[{"executor":"process","session":{"mode":"fixed"},"config":{"command":["sh","-c","exit 0"]}}]}}],"edges":[{"from":"work","to":"work","when":"` + expression + `","maxIterations":1},{"from":"work","to":"$end"}]}`
			if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
				t.Fatal(err)
			}
			result := ValidateWithInputs(root, "", map[string]string{"loop": loop})
			if got, want := result.Valid, loop == "no"; got != want {
				t.Fatalf("%s with loop=%s: valid=%v want %v, issues=%+v", expression, loop, got, want, result.Issues)
			}
		}
	}
}
