package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/engine"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func af08Graph(t *testing.T, name string) (string, model.Workflow, map[string]driver.Executor) {
	t.Helper()
	root := t.TempDir()
	if evidence := os.Getenv("AF08_EVIDENCE_DIR"); evidence != "" {
		root = filepath.Join(evidence, strings.ReplaceAll(t.Name(), "/", "_"), name)
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executors := map[string]driver.Executor{
		"codex":  {Kind: "codex", Command: []string{executable}, ResumeCommand: []string{executable}},
		"claude": {Kind: "claude-code", Command: []string{executable}, ResumeCommand: []string{executable}},
		"worker": {Kind: "command", Command: []string{executable}, ResumeCommand: []string{executable}},
		"fixed":  {Kind: "command", Command: []string{executable}},
		"human":  {Kind: "external"},
	}
	newNode := func(id, executor string) model.Node {
		return model.Node{ID: id, Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: executor}}}}
	}
	source := newNode("source", "codex")
	source.Execution.Candidates = append(source.Execution.Candidates, model.ExecutorCandidate{Executor: "claude"})
	source.Execution.Fallback = &model.FallbackSpec{When: "failure.kind == 'exit_code'"}
	waiter := newNode("waiter", "codex")
	waiter.Execution.Candidates[0].Session = model.SessionSpec{Mode: "inherit", From: "source"}
	waiter.Execution.Candidates = append(waiter.Execution.Candidates, model.ExecutorCandidate{Executor: "human"})
	waiter.Execution.Fallback = &model.FallbackSpec{When: "failure.kind == 'session_incompatible'"}
	waiter.OutputSchema = map[string]any{"type": "object", "required": []string{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}}}
	flow := model.Workflow{Version: "3", Name: name, Entry: "source"}
	switch name {
	case "mixed-wait":
		source.Dispatch = "all"
		flow.Nodes = []model.Node{source, waiter, newNode("branch", "worker"), newNode("reader", "worker")}
		flow.Edges = []model.Transition{{From: "source", To: "waiter"}, {From: "source", To: "branch"}, {From: "waiter", To: "$end"}, {From: "branch", To: "reader"}, {From: "reader", To: "$end"}}
	case "conditional-fixed":
		work := newNode("work", "fixed")
		work.Execution.Candidates[0].Session = model.SessionSpec{Mode: "fixed"}
		work.Execution.Candidates = append(work.Execution.Candidates, model.ExecutorCandidate{Executor: "human"})
		work.Execution.Fallback = &model.FallbackSpec{When: "failure.kind == 'configuration_failed'"}
		flow.Entry, flow.Nodes = "work", []model.Node{work}
		limit := 1
		flow.Edges = []model.Transition{{From: "work", To: "work", When: "output.again", MaxIterations: limit}, {From: "work", To: "$end"}}
	case "definite-protocol-mismatch":
		work := newNode("work", "worker")
		work.Execution.Candidates[0].Session = model.SessionSpec{Mode: "inherit", From: "source"}
		flow.Nodes = []model.Node{source, work}
		flow.Edges = []model.Transition{{From: "source", To: "work"}, {From: "work", To: "$end"}}
	case "multiple-sources":
		seed := newNode("seed", "worker")
		seed.Dispatch = "all"
		source.ID = "a"
		merge := newNode("merge", "codex")
		merge.Join = &model.JoinSpec{From: []string{"a", "b"}}
		merge.Execution.Candidates[0].Session = model.SessionSpec{Mode: "inherit", From: "a"}
		merge.Execution.Candidates = append(merge.Execution.Candidates, model.ExecutorCandidate{Executor: "claude", Session: model.SessionSpec{Mode: "inherit", From: "b"}}, model.ExecutorCandidate{Executor: "human"})
		merge.Execution.Fallback = &model.FallbackSpec{When: "failure.kind == 'session_incompatible'"}
		flow.Entry, flow.Nodes = "seed", []model.Node{seed, source, newNode("b", "worker"), merge}
		flow.Edges = []model.Transition{{From: "seed", To: "a"}, {From: "seed", To: "b"}, {From: "a", To: "merge"}, {From: "b", To: "merge"}, {From: "merge", To: "$end"}}
	}
	if err := store.WriteAtomicJSON(filepath.Join(root, "workflow.json"), flow); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAtomicJSON(filepath.Join(root, "executors.json"), Registry{Executors: executors}); err != nil {
		t.Fatal(err)
	}
	return root, flow, executors
}

func TestAF08ProviderProcess(t *testing.T) {
	path := os.Getenv("AGENTFLOW_RESULT_FILE")
	if path == "" {
		return
	}
	var config map[string]any
	if err := store.ReadAtomicJSON(os.Getenv("AGENTFLOW_CONFIG_FILE"), &config); err != nil {
		t.Fatal(err)
	}
	if config["fail"] == true {
		os.Exit(17)
	}
	result := map[string]any{"again": true, "text": "provider-result", "observed": config["observed"], "session_seen": os.Getenv("AGENTFLOW_SESSION_ID")}
	if value, ok := config["again"]; ok {
		result["again"] = value
	}
	if config["stop_on_resume"] == true && os.Getenv("AGENTFLOW_SESSION_ID") != "" {
		result["again"] = false
	}
	if err := store.WriteAtomicJSON(path, result); err != nil {
		t.Fatal(err)
	}
	if config["omit_session"] == true {
		return
	}
	sid := os.Getenv("AGENTFLOW_SESSION_ID")
	if sid == "" {
		sid = "fixture-session-" + os.Getenv("AGENTFLOW_NODE_ID")
	}
	if err := os.WriteFile(os.Getenv("AGENTFLOW_SESSION_FILE"), []byte(sid), 0600); err != nil {
		t.Fatal(err)
	}
}

func af08Runtime(t *testing.T, name string, failCodex bool) (*model.Run, map[string]driver.Executor, Validation) {
	t.Helper()
	root, flow, executors := af08Graph(t, name)
	af08ExecutorCommands(t, executors)
	if failCodex {
		for i := range flow.Nodes {
			if flow.Nodes[i].ID == "source" || flow.Nodes[i].ID == "a" {
				flow.Nodes[i].Execution.Candidates[0].Config = map[string]any{"fail": true}
			}
		}
	}
	if name == "mixed-wait" {
		flow.Nodes[3].Execution.Candidates[0].Config = map[string]any{"observed": "{{ nodes.waiter.result.text }}"}
	}
	return af08Execute(t, root, flow, executors)
}

func af08ExecutorCommands(t *testing.T, executors map[string]driver.Executor) {
	t.Helper()
	executable, _ := os.Executable()
	for id, entry := range executors {
		if entry.Kind == "external" {
			continue
		}
		entry.Command = []string{executable, "-test.run=^TestAF08ProviderProcess$"}
		if len(entry.ResumeCommand) > 0 {
			entry.ResumeCommand = entry.Command
		}
		executors[id] = entry
	}
}

func af08Execute(t *testing.T, root string, flow model.Workflow, executors map[string]driver.Executor) (*model.Run, map[string]driver.Executor, Validation) {
	t.Helper()
	for name, entry := range executors {
		if entry.Kind != "external" {
			entry.Workspace = root
			executors[name] = entry
		}
	}
	if err := store.WriteAtomicJSON(filepath.Join(root, "workflow.json"), flow); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAtomicJSON(filepath.Join(root, "executors.json"), Registry{Executors: executors}); err != nil {
		t.Fatal(err)
	}
	validation := Validate(root, "")
	run, err := store.CreateRun(root, flow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.WriteAtomicJSON(filepath.Join(root, "runtime-parity.json"), map[string]any{"validation": validation, "state": run.State, "real_provider_verified": false}); err != nil {
			t.Error(err)
		}
	})
	return run, executors, validation
}

func TestAF08RuntimeMultipleExplicitSourcesRemainConditional(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "compatible", true: "external"}[fail], func(t *testing.T) {
			run, _, validation := af08Runtime(t, "multiple-sources", fail)
			if !validation.Valid || validation.ExecutionVerified {
				t.Fatal(validation)
			}
			want := "succeeded"
			if fail {
				want = "waiting_input"
			}
			if run.State.Status != want {
				t.Fatal(run.State)
			}
			found := false
			for _, check := range validation.Analysis {
				if check.Node == "merge" && check.Source == "a" && check.ReasonCode == "source_protocol_conditional" && len(check.Protocols) >= 2 {
					found = true
				}
			}
			if !found {
				t.Fatal("mixed source protocols were collapsed", validation.Analysis)
			}
		})
	}
}

func TestAF08RuntimeJoinAndReloadPreserveGuaranteedContainer(t *testing.T) {
	root, flow, executors := af08Graph(t, "mixed-wait")
	af08ExecutorCommands(t, executors)
	flow.Nodes[0].Execution = model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "worker"}}}
	flow.Nodes[1].Execution = model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "human"}}}
	flow.Nodes[3].Join = &model.JoinSpec{From: []string{"waiter", "branch"}}
	flow.Nodes[3].Execution.Candidates[0].Config = map[string]any{"observed": "{{ nodes.waiter.result.text }}"}
	flow.Edges = []model.Transition{{From: "source", To: "waiter"}, {From: "source", To: "branch"}, {From: "waiter", To: "reader"}, {From: "branch", To: "reader"}, {From: "reader", To: "$end"}}
	run, _, validation := af08Execute(t, root, flow, executors)
	if !validation.Valid || run.State.Status != "waiting_input" {
		t.Fatal(validation, run.State)
	}
	found := false
	for _, check := range validation.Analysis {
		if check.Node == "reader" && check.Reference == "nodes.waiter.result" && check.Status == "guaranteed" {
			found = true
		}
	}
	if !found {
		t.Fatal("join proof missing", validation.Analysis)
	}
	reloaded, err := store.LoadRun(root, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.EnqueueControl(run.RunDir, model.Control{Kind: engine.ControlSubmit, NodeID: "waiter", Token: reloaded.State.Nodes["waiter"].LeaseToken, Output: map[string]any{"text": "joined-output"}}); err != nil {
		t.Fatal(err)
	}
	if err = engine.Execute(reloaded, executors); err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Status != "succeeded" || reloaded.State.Nodes["reader"].Result.(map[string]any)["observed"] != "joined-output" {
		t.Fatal(reloaded.State)
	}
	*run = *reloaded
	first, _ := json.Marshal(validation.Analysis)
	for i := 0; i < 3; i++ {
		next := Validate(root, "")
		encoded, _ := json.Marshal(next.Analysis)
		if string(encoded) != string(first) || next.ExecutionVerified {
			t.Fatal("analysis changed across persisted runtime/reload", string(encoded), string(first))
		}
	}
}

func TestAF08RuntimeCompatibleFixedHistoryIsReusable(t *testing.T) {
	root, flow, executors := af08Graph(t, "conditional-fixed")
	af08ExecutorCommands(t, executors)
	entry := executors["fixed"]
	entry.ResumeCommand = entry.Command
	executors["fixed"] = entry
	flow.Nodes[0].Execution.Candidates[0].Config = map[string]any{"stop_on_resume": true}
	run, _, validation := af08Execute(t, root, flow, executors)
	if !validation.Valid || run.State.Status != "succeeded" || len(run.State.History) != 2 {
		t.Fatal(validation, run.State)
	}
	if run.State.Nodes["work"].SessionID == "" || run.State.Nodes["work"].SessionID != run.State.History[0].State.SessionID {
		t.Fatal("compatible fixed session was not reused", run.State)
	}
	if run.State.History[0].State.Result.(map[string]any)["session_seen"] != "" || run.State.Nodes["work"].Result.(map[string]any)["session_seen"] != run.State.Nodes["work"].SessionID {
		t.Fatal("first visit reused an unproven future session", run.State)
	}
}

func TestAF08FutureSessionSourceRemainsAnError(t *testing.T) {
	root, flow, executors := af08Graph(t, "mixed-wait")
	af08ExecutorCommands(t, executors)
	flow.Nodes[1].Execution = model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "codex", Session: model.SessionSpec{Mode: "inherit", From: "reader"}}}}
	run, _, validation := af08Execute(t, root, flow, executors)
	if validation.Valid || run.State.Status != "failed" || run.State.Nodes["waiter"].Failure.Kind != "session_incompatible" {
		t.Fatal(validation, run.State)
	}
	found := false
	for _, check := range validation.Analysis {
		if check.Node == "waiter" && check.Source == "reader" && check.Status == "unavailable" && check.ReasonCode == "session_incompatible" {
			found = true
		}
	}
	if !found {
		t.Fatal("future-source reason missing", validation.Analysis)
	}
}

func TestAF08RuntimeMixedProtocolWaitAndSuccessfulAlternative(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "compatible", true: "waiting"}[fail], func(t *testing.T) {
			run, _, validation := af08Runtime(t, "mixed-wait", fail)
			if !validation.Valid || validation.ExecutionVerified {
				t.Fatal(validation)
			}
			found := false
			for _, check := range validation.Analysis {
				if check.Node == "reader" && check.Reference == "nodes.waiter.result" && check.Status == "conditional" {
					found = true
				}
			}
			if !found {
				t.Fatal("missing conditional result diagnostic", validation.Analysis)
			}
			reader := run.State.Nodes["reader"].Result.(map[string]any)
			if fail {
				if run.State.Status != "waiting_input" || run.State.Nodes["waiter"].SelectedExecutor != "human" || run.State.Nodes["waiter"].SessionID != "" || reader["observed"] != nil {
					t.Fatal(run.State, reader)
				}
			} else if run.State.Status != "succeeded" || reader["observed"] != "provider-result" {
				t.Fatal(run.State, reader)
			}
		})
	}
}

func TestAF08RuntimeConditionalFixedRevisitWait(t *testing.T) {
	run, _, validation := af08Runtime(t, "conditional-fixed", false)
	if !validation.Valid || run.State.Status != "waiting_input" || run.State.Nodes["work"].SelectedExecutor != "human" || run.State.Nodes["work"].SessionID != "" {
		t.Fatal(validation, run.State)
	}
	found := false
	for _, check := range validation.Analysis {
		if check.Reference == "session_revisit" && check.ReasonCode == "fixed_revisit_launch_failure" && check.Status == "conditional" {
			found = true
		}
	}
	if !found {
		t.Fatal("fixed first/revisit distinction missing", validation.Analysis)
	}
	if len(run.State.History) != 1 || run.State.History[0].State.SessionID == "" {
		t.Fatal("fixture did not establish compatible history", run.State.History)
	}
}

func TestAF08ConditionalFixedParallelReadHasNoPrematureResult(t *testing.T) {
	root, flow, executors := af08Graph(t, "conditional-fixed")
	af08ExecutorCommands(t, executors)
	work := flow.Nodes[0]
	makeWorker := func(id string) model.Node {
		return model.Node{ID: id, Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "worker"}}}}
	}
	seed := makeWorker("seed")
	seed.Dispatch = "all"
	reader := makeWorker("reader")
	reader.Execution.Candidates[0].Config = map[string]any{"observed": "{{ nodes.work.result.text }}"}
	flow.Entry = "seed"
	flow.Nodes = []model.Node{seed, work, makeWorker("step1"), makeWorker("step2"), reader}
	flow.Edges = append(flow.Edges, model.Transition{From: "seed", To: "work"}, model.Transition{From: "seed", To: "step1"}, model.Transition{From: "step1", To: "step2"}, model.Transition{From: "step2", To: "reader"}, model.Transition{From: "reader", To: "$end"})
	run, _, validation := af08Execute(t, root, flow, executors)
	if !validation.Valid || run.State.Status != "waiting_input" || run.State.Nodes["reader"].Result.(map[string]any)["observed"] != nil {
		t.Fatal(validation, run.State)
	}
	found := false
	for _, check := range validation.Analysis {
		if check.Node == "reader" && check.Reference == "nodes.work.result" && check.Status == "conditional" {
			found = true
		}
	}
	if !found {
		t.Fatal("fixed revisit promoted a premature parallel result", validation.Analysis)
	}
}

func TestAF08RuntimeDefiniteMismatchStillFails(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "codex", true: "claude"}[fail], func(t *testing.T) {
			run, _, validation := af08Runtime(t, "definite-protocol-mismatch", fail)
			if validation.Valid || run.State.Status != "failed" || run.State.Nodes["work"].Failure.Kind != "session_incompatible" {
				t.Fatal(validation, run.State)
			}
		})
	}
}

func TestAF08DiagnosticsDoNotDependOnNodeArrayOrder(t *testing.T) {
	root, flow, executors := af08Graph(t, "mixed-wait")
	flow.Nodes[3].Execution.Candidates[0].Config = map[string]any{"observed": "{{ nodes.waiter.result.text }}"}
	check := func() staticValueAvailability {
		parent := "branch"
		return nodeResultAvailability(root, flow, "waiter", "reader", nil, &parent, executors, map[string]bool{})
	}
	first := check()
	for i, j := 0, len(flow.Nodes)-1; i < j; i, j = i+1, j-1 {
		flow.Nodes[i], flow.Nodes[j] = flow.Nodes[j], flow.Nodes[i]
	}
	if next := check(); first != staticValueUnknown || next != first {
		t.Fatal(first, next)
	}
	if err := store.WriteAtomicJSON(filepath.Join(root, "workflow.json"), flow); err != nil {
		t.Fatal(err)
	}
	result := Validate(root, "")
	encoded, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(encoded), `"reason_code":"completion_conditional"`) || result.ExecutionVerified {
		t.Fatal(string(encoded), err)
	}
}

func TestAF08MixedProtocolWaitIsConditional(t *testing.T) {
	root, flow, executors := af08Graph(t, "mixed-wait")
	deferred := nodeDefersCompletion(root, flow, "waiter", nil, executors, map[string]bool{})
	parent := "branch"
	availability := nodeResultAvailability(root, flow, "waiter", "reader", nil, &parent, executors, map[string]bool{})
	_ = store.WriteAtomicJSON(filepath.Join(root, "observed.json"), map[string]any{"deferred": deferred, "availability": availability, "validation": Validate(root, "")})
	if !deferred || availability != staticValueUnknown {
		t.Fatalf("external wait lost: deferred=%v availability=%v (want conditional)", deferred, availability)
	}
}

func TestAF08ConditionalFixedRevisitMayWait(t *testing.T) {
	root, flow, executors := af08Graph(t, "conditional-fixed")
	deferred := nodeDefersCompletion(root, flow, "work", nil, executors, map[string]bool{})
	_ = store.WriteAtomicJSON(filepath.Join(root, "observed.json"), map[string]any{"deferred": deferred, "validation": Validate(root, "")})
	if !deferred {
		t.Fatal("conditional fixed revisit lost its configuration_failed external fallback")
	}
}

func TestAF08EveryKnownSourceProtocolMismatchIsError(t *testing.T) {
	root, _, _ := af08Graph(t, "definite-protocol-mismatch")
	result := Validate(root, "")
	_ = store.WriteAtomicJSON(filepath.Join(root, "observed.json"), result)
	if result.Valid {
		t.Fatalf("all native source protocols are incompatible with command:worker; got valid: %+v", result.Issues)
	}
}

func TestAF08JoinProvesExternalResultCompletion(t *testing.T) {
	root, flow, executors := af08Graph(t, "mixed-wait")
	flow.Nodes[0].Execution.Candidates = []model.ExecutorCandidate{{Executor: "worker"}}
	flow.Nodes[1].Execution = model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "human"}}}
	flow.Nodes[3].Join = &model.JoinSpec{From: []string{"waiter", "branch"}}
	flow.Edges = []model.Transition{{From: "source", To: "waiter"}, {From: "source", To: "branch"}, {From: "waiter", To: "reader"}, {From: "branch", To: "reader"}, {From: "reader", To: "$end"}}
	if err := store.WriteAtomicJSON(filepath.Join(root, "workflow.json"), flow); err != nil {
		t.Fatal(err)
	}
	parent := "branch"
	availability := nodeResultAvailability(root, flow, "waiter", "reader", nil, &parent, executors, map[string]bool{})
	_ = store.WriteAtomicJSON(filepath.Join(root, "observed.json"), map[string]any{"availability": availability, "expected_status": "guaranteed", "validation": Validate(root, "")})
	if availability != staticValuePresent {
		t.Fatalf("join waits for external completion; got availability %v", availability)
	}
}

func TestAF08OptionalNewSessionCanSelectWaiting(t *testing.T) {
	root, flow, executors := af08Graph(t, "mixed-wait")
	flow.Nodes[0].Execution = model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "codex", Config: map[string]any{"omit_session": true}}}}
	deferred := nodeDefersCompletion(root, flow, "waiter", nil, executors, map[string]bool{})
	if err := store.WriteAtomicJSON(filepath.Join(root, "workflow.json"), flow); err != nil {
		t.Fatal(err)
	}
	_ = store.WriteAtomicJSON(filepath.Join(root, "observed.json"), map[string]any{"deferred": deferred, "expected_status": "conditional"})
	if !deferred {
		t.Fatal("new-session provider success does not prove it emitted a session ID")
	}
}

func TestAF08RuntimeMissingNewSessionWaitsWithoutResult(t *testing.T) {
	root, flow, executors := af08Graph(t, "mixed-wait")
	af08ExecutorCommands(t, executors)
	flow.Nodes[0].Execution = model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "codex", Config: map[string]any{"omit_session": true}}}}
	flow.Nodes[3].Execution.Candidates[0].Config = map[string]any{"observed": "{{ nodes.waiter.result.text }}"}
	run, _, validation := af08Execute(t, root, flow, executors)
	if !validation.Valid || run.State.Status != "waiting_input" || run.State.Nodes["source"].SessionID != "" || run.State.Nodes["reader"].Result.(map[string]any)["observed"] != nil {
		t.Fatal(validation, run.State)
	}
}
