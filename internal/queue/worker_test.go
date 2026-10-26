package queue

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/driver"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestQueueExecutorProcess(t *testing.T) {
	if os.Getenv("AF_QUEUE_EXECUTOR") != "1" {
		return
	}
	var config map[string]any
	data, _ := os.ReadFile(os.Getenv("AGENTFLOW_CONFIG_FILE"))
	_ = json.Unmarshal(data, &config)
	gate := filepath.Join(os.Getenv("AGENTFLOW_WORKSPACE"), "gate")
	_ = os.WriteFile(filepath.Join(os.Getenv("AGENTFLOW_WORKSPACE"), "started"), []byte("1"), 0600)
	for config["mode"] != "fast" {
		if _, err := os.Stat(gate); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if os.WriteFile(os.Getenv("AGENTFLOW_RESULT_FILE"), []byte(`{"ok":true}`), 0600) != nil {
		os.Exit(92)
	}
	os.Exit(0)
}
func testWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"executors": map[string]driver.Executor{"fixture": {Kind: "command", Workspace: root, Command: []string{binary, "-test.run=^TestQueueExecutorProcess$"}, Env: map[string]string{"AF_QUEUE_EXECUTOR": "1"}}}}
	data, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(root, "executors.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return root
}
func waitQueue(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("queue did not reach expected state")
}
func makeRun(t *testing.T, root string, flow model.Workflow) string {
	t.Helper()
	r, err := store.CreateOrGetRun(context.Background(), root, store.CreateRequest{Workflow: flow, Queued: true})
	if err != nil {
		t.Fatal(err)
	}
	return r.Run.RunID
}

func TestMixedWaitingRunOccupiesSlotAndShutdownDrains(t *testing.T) {
	root := testWorkspace(t)
	candidate := func(name string) model.Execution {
		return model.Execution{Candidates: []model.ExecutorCandidate{{Executor: name}}}
	}
	flow := model.Workflow{Version: "3", Name: "mixed", Entry: "start", Nodes: []model.Node{{ID: "start", Dispatch: "all", Execution: candidate("fixture")}, {ID: "human", Execution: candidate("external")}, {ID: "work", Execution: candidate("fixture")}}, Edges: []model.Transition{{From: "start", To: "human"}, {From: "start", To: "work"}, {From: "human", To: "$end"}, {From: "work", To: "$end"}}}
	flow.Nodes[0].Execution.Candidates[0].Config = map[string]any{"mode": "fast"}
	first := makeRun(t, root, flow)
	ctx, cancel := context.WithCancel(context.Background())
	worker, err := Start(ctx, root, root, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.WriteFile(filepath.Join(root, "gate"), nil, 0600); cancel(); _ = worker.Wait() }()
	waitQueue(t, func() bool {
		r, _ := store.LoadRun(root, first)
		return r.State.Nodes["human"].Status == "waiting_input" && r.State.Nodes["work"].Status == "running"
	})
	second := makeRun(t, root, model.Workflow{Version: "3", Name: "next", Entry: "human", Nodes: []model.Node{{ID: "human", Execution: candidate("external")}}, Edges: []model.Transition{{From: "human", To: "$end"}}})
	time.Sleep(75 * time.Millisecond)
	next, _ := store.LoadRun(root, second)
	if next.State.Status != "queued" {
		t.Fatal("mixed run incorrectly released slot")
	}
	cancel()
	if _, err := Start(context.Background(), root, root, 9, true); !errors.Is(err, ErrWorkerBusy) {
		t.Fatal("draining owner lock released early", err)
	}
	if err := os.WriteFile(filepath.Join(root, "gate"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := worker.Wait(); err != nil {
		t.Fatal(err)
	}
	next, _ = store.LoadRun(root, second)
	if next.State.Status != "queued" {
		t.Fatal("draining worker admitted new work")
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	restarted, err := Start(ctx2, root, root, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	waitQueue(t, func() bool { r, _ := store.LoadRun(root, second); return r.State.Status == "waiting_input" })
	cancel2()
	if err := restarted.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestWaitingSynchronousCallerAdoptsManagedMode(t *testing.T) {
	root := testWorkspace(t)
	flow := model.Workflow{Version: "3", Name: "switch", Entry: "human", Nodes: []model.Node{{ID: "human", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "external"}}}}}, Edges: []model.Transition{{From: "human", To: "$end"}}}
	id := makeRun(t, root, flow)
	release, err := store.AcquireWorkspaceLock(context.Background(), root, "worker.lock", false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		run, err := Drive(ctx, root, root, id)
		if err == nil && run.State.Status != "queued" {
			err = errors.New("waiting caller executed outside managed service")
		}
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := store.EnableQueue(ctx, root); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("caller ignored managed-mode transition")
	}
}
