package engine

import (
	stdctx "context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestResourceWaitDoesNotConsumeAttemptOrFallback(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "resources.json"), []byte(`{"resources":{"writer":{"capacity":1}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	flow := model.Workflow{Version: "3", Name: "resource", Entry: "work", Nodes: []model.Node{{ID: "work", Resources: []model.ResourceRequest{{Key: "writer", Units: 1}}, Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "role"}, {Executor: "backup"}}}}}, Edges: []model.Transition{{From: "work", To: "$end"}}}
	run, err := store.CreateRun(root, flow, map[string]string{"n": "0"}, "")
	if err != nil {
		t.Fatal(err)
	}
	held, err := store.AcquireResources(stdctx.Background(), root, store.ResourceClaim{RunID: run.RunID, NodeID: "holder", Attempt: 1, Token: store.NewToken(), Requests: flow.Nodes[0].Resources})
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	executors := map[string]driver.Executor{"role": environmentExecutor(t, "command", "PATH"), "backup": environmentExecutor(t, "command", "PATH")}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	state := run.State.Nodes["work"]
	if run.State.Status != "waiting_resource" || state.Status != "waiting_resource" || state.Attempts != 0 || len(state.AttemptSequence) != 0 || state.Failure != nil {
		t.Fatalf("resource wait consumed an attempt: %+v", state)
	}
	if state.Continuation == nil || state.Continuation.Candidate != 0 || state.Continuation.CandidateAttempt != 1 {
		t.Fatal("candidate cursor lost")
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, executors); err != nil {
		t.Fatal(err)
	}
	state = run.State.Nodes["work"]
	if run.State.Status != "succeeded" || state.Attempts != 1 || state.SelectedExecutor != "role" || state.AttemptSequence[0].ResourceClaimID == "" {
		t.Fatalf("wrong resumed execution: %+v", state)
	}
	status, err := store.ResourceStatus(stdctx.Background(), root)
	if err != nil || status.Used["writer"] != 0 {
		t.Fatal("claim leaked", status, err)
	}
}

func TestExternalWaitDoesNotReserveActiveResources(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "resources.json"), []byte(`{"resources":{"writer":{"capacity":1}}}`), 0600)
	flow := model.Workflow{Version: "3", Name: "external-resource", Entry: "human", Nodes: []model.Node{{ID: "human", Resources: []model.ResourceRequest{{Key: "writer", Units: 1}}, Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "external"}}}}}, Edges: []model.Transition{{From: "human", To: "$end"}}}
	run, err := store.CreateRun(root, flow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Execute(run, nil); err != nil {
		t.Fatal(err)
	}
	status, err := store.ResourceStatus(stdctx.Background(), root)
	if err != nil || run.State.Status != "waiting_input" || status.Used["writer"] != 0 || len(status.Claims) != 0 {
		t.Fatal("external held active quota", status, err)
	}
}
