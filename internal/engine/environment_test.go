package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestEnvironmentExecutorProcess(t *testing.T) {
	if os.Getenv("AF_ENV_EXECUTOR") != "1" {
		return
	}
	var input, config map[string]any
	data, _ := os.ReadFile(os.Getenv("AGENTFLOW_INPUT_FILE"))
	_ = json.Unmarshal(data, &input)
	data, _ = os.ReadFile(os.Getenv("AGENTFLOW_CONFIG_FILE"))
	_ = json.Unmarshal(data, &config)
	n := 0
	switch value := input["n"].(type) {
	case float64:
		n = int(value)
	case string:
		n, _ = strconv.Atoi(value)
	}
	_, visible := os.LookupEnv("AF_CANARY_VISIBLE")
	_, secret := os.LookupEnv("AF_CANARY_SECRET")
	output := map[string]any{"visible": visible, "secret": secret, "n": n + 1, "sessionSeen": os.Getenv("AGENTFLOW_SESSION_ID")}
	session := os.Getenv("AGENTFLOW_SESSION_ID")
	if session == "" || os.Getenv("AGENTFLOW_SESSION_MODE") == "fork" {
		session = "fixture-" + os.Getenv("AGENTFLOW_NODE_TOKEN")
	}
	if os.WriteFile(os.Getenv("AGENTFLOW_SESSION_FILE"), []byte(session), 0o600) != nil {
		os.Exit(90)
	}
	data, _ = json.Marshal(output)
	fmt.Print(string(data))
	if os.WriteFile(os.Getenv("AGENTFLOW_RESULT_FILE"), data, 0o600) != nil {
		os.Exit(91)
	}
	if config["fail"] == true {
		os.Exit(75)
	}
	os.Exit(0)
}

func environmentExecutor(t *testing.T, kind string, inherit string) driver.Executor {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := []string{binary, "-test.run=^TestEnvironmentExecutorProcess$"}
	return driver.Executor{Kind: kind, Command: command, ResumeCommand: command, ForkCommand: command, Workspace: t.TempDir(), Env: map[string]string{"AF_ENV_EXECUTOR": "1"}, Environment: &driver.EnvironmentPolicy{Mode: "allowlist", Inherit: []string{inherit}}}
}

func TestEnvironmentAcrossExecutorAndSessionModes(t *testing.T) {
	t.Setenv("AF_CANARY_VISIBLE", "synthetic-visible")
	t.Setenv("AF_CANARY_SECRET", "synthetic-secret")
	for _, kind := range []string{"command", "codex", "claude-code"} {
		for _, mode := range []string{"new", "fixed", "inherit", "fork"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				executor := environmentExecutor(t, kind, "AF_CANARY_VISIBLE")
				work := model.Node{ID: "work", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "role", Session: model.SessionSpec{Mode: mode}}}}}
				flow := model.Workflow{Version: "3", Name: "environment", Entry: "work", Nodes: []model.Node{work}, Edges: []model.Transition{{From: "work", To: "work", When: "nodes.work.result.n < 2", MaxIterations: 1}, {From: "work", To: "$end"}}}
				if mode == "inherit" || mode == "fork" {
					flow.Entry = "seed"
					flow.Nodes[0].Execution.Candidates[0].Session.From = "seed"
					flow.Nodes = append(flow.Nodes, model.Node{ID: "seed", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "role"}}}})
					flow.Edges = []model.Transition{{From: "seed", To: "work"}, {From: "work", To: "$end"}}
				}
				run, err := store.CreateRun(t.TempDir(), flow, map[string]string{"n": "0"}, "")
				if err != nil {
					t.Fatal(err)
				}
				if err := Execute(run, map[string]driver.Executor{"role": executor}); err != nil {
					t.Fatal(err)
				}
				state := run.State.Nodes["work"]
				if run.State.Status != "succeeded" {
					t.Fatalf("mode failed: %s %s", run.State.Status, state.Error)
				}
				if state.Result.(map[string]any)["visible"] != true || state.Result.(map[string]any)["secret"] != false {
					t.Fatalf("wrong environment: %v", state.Result)
				}
				if mode == "inherit" && state.SessionID != run.State.Nodes["seed"].SessionID {
					t.Fatal("inherit did not reuse source")
				}
				if mode == "fork" && state.SessionID == run.State.Nodes["seed"].SessionID {
					t.Fatal("fork reused original session")
				}
				if mode == "fixed" && state.Result.(map[string]any)["sessionSeen"] != state.SessionID {
					t.Fatal("fixed session did not resume")
				}
				if mode == "new" && state.Result.(map[string]any)["sessionSeen"] != "" {
					t.Fatal("new session unexpectedly resumed")
				}
				assertNoEnvironmentCanary(t, run.RunDir)
			})
		}
	}
}

func TestEnvironmentRetryFallbackAndManualRetry(t *testing.T) {
	t.Setenv("AF_CANARY_VISIBLE", "synthetic-visible")
	t.Setenv("AF_CANARY_SECRET", "synthetic-secret")
	primary := environmentExecutor(t, "command", "AF_CANARY_VISIBLE")
	backup := environmentExecutor(t, "command", "AF_CANARY_SECRET")
	flow := candidateWorkflow([]model.ExecutorCandidate{{Executor: "primary", Config: map[string]any{"fail": true}}, {Executor: "backup"}}, &model.FallbackSpec{When: "failure.kind == 'exit_code'"}, 2)
	run := createCandidateRun(t, flow)
	if err := Execute(run, map[string]driver.Executor{"primary": primary, "backup": backup}); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["step"]
	if run.State.Status != "succeeded" || len(state.AttemptSequence) != 3 || state.Result.(map[string]any)["visible"] != false || state.Result.(map[string]any)["secret"] != true {
		t.Fatalf("fallback policy failed: status=%s", run.State.Status)
	}
	for _, attempt := range state.AttemptSequence[:2] {
		data, err := os.ReadFile(run.RunDir + "/" + attempt.Artifacts["stdoutLog"])
		if err != nil {
			t.Fatal(err)
		}
		var observed map[string]any
		if json.Unmarshal(data, &observed) != nil || observed["visible"] != true || observed["secret"] != false {
			t.Fatal("retry leaked another candidate's environment")
		}
	}
	flow = candidateWorkflow([]model.ExecutorCandidate{{Executor: "primary", Config: map[string]any{"fail": true}}}, nil, 1)
	run = createCandidateRun(t, flow)
	if err := Execute(run, map[string]driver.Executor{"primary": primary}); err != nil {
		t.Fatal(err)
	}
	if run.State.Status != "failed" {
		t.Fatal("expected initial failure")
	}
	if reason, err := applyRetry(run, model.Control{Kind: ControlRetry}); err != nil || reason != "" {
		t.Fatalf("retry rejected: %s %v", reason, err)
	}
	if err := store.SaveState(run); err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, map[string]driver.Executor{"primary": primary}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(run.RunDir + "/" + run.State.Nodes["step"].AttemptSequence[0].Artifacts["stdoutLog"])
	if err != nil {
		t.Fatal(err)
	}
	var observed map[string]any
	if json.Unmarshal(data, &observed) != nil || observed["visible"] != true || observed["secret"] != false {
		t.Fatal("manual retry did not retain environment policy")
	}
	assertNoEnvironmentCanary(t, run.RunDir)
}

func assertNoEnvironmentCanary(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte("synthetic-visible")) || bytes.Contains(data, []byte("synthetic-secret")) {
			t.Errorf("environment value leaked to %s", entry.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
