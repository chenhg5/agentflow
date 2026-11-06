package trigger

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestResolveInputsAndJSONPath(t *testing.T) {
	definition := Definition{Inputs: map[string]string{"static": "value"}, Bindings: map[string]Binding{
		"repo":     {From: "json", Path: "$.repository.full_name"},
		"delivery": {From: "header", Name: "X-Delivery"},
	}}
	inputs, err := ResolveInputs(definition, Event{Payload: json.RawMessage(`{"repository":{"full_name":"org/repo"}}`), Headers: map[string]string{"X-Delivery": "d-1"}})
	if err != nil || inputs["repo"] != "org/repo" || inputs["delivery"] != "d-1" || inputs["static"] != "value" {
		t.Fatalf("unexpected inputs: %#v, err=%v", inputs, err)
	}
}

func TestDispatcherIsIdempotent(t *testing.T) {
	root := t.TempDir()
	starter := &fakeStarter{next: "run_1"}
	dispatcher := &Dispatcher{Root: root, Starter: starter, Config: Config{Triggers: []Definition{{ID: "hook", Enabled: true, Source: SourceConfig{Kind: "webhook"}, Target: TargetConfig{Workflow: "workflow.json"}}}}}
	event := Event{TriggerID: "hook", SourceKind: "webhook", DeliveryID: "delivery-1", ReceivedAt: time.Now().UTC()}
	first, err := dispatcher.Dispatch(context.Background(), event)
	if err != nil || first.Duplicate || starter.calls != 1 {
		t.Fatalf("first dispatch: %#v err=%v calls=%d", first, err, starter.calls)
	}
	second, err := dispatcher.Dispatch(context.Background(), event)
	if err != nil || !second.Duplicate || second.Run.RunID != "run_1" || starter.calls != 1 {
		t.Fatalf("duplicate dispatch: %#v err=%v calls=%d", second, err, starter.calls)
	}
}

func TestRuntimeQueuesAndExecutesShellWorkflow(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(cwd, "workflow.json")
	workflow, err := json.Marshal(model.Workflow{Version: "3", Name: "triggered", Entry: "shell", Nodes: []model.Node{{ID: "shell", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "process", Config: map[string]any{"command": []string{"sh", "-c", `printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}}}}}}}, Edges: []model.Transition{{From: "shell", To: "$end"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflowPath, workflow, 0o644); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(root, cwd, 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := runtime.StartWorkers(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { cancel(); runtime.Wait() }()
	run, err := runtime.Start(ctx, StartRunCommand{WorkflowPath: workflowPath, Inputs: map[string]string{}, TriggerID: "manual", TriggerKind: "cron", DeliveryID: "d-1", ReceivedAt: time.Now().UTC(), IdempotencyKey: "manual:d-1"})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := runtime.WaitForRun(ctx, run.RunID)
	if err != nil || completed.State.Status != "succeeded" {
		t.Fatalf("run status=%v err=%v", completed.State.Status, err)
	}
	if _, err := store.LoadRun(root, run.RunID); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeRejectsUnregisteredCandidateBeforeCreatingRun(t *testing.T) {
	root := t.TempDir()
	workflowPath := filepath.Join(root, "workflow.json")
	workflow := `{
  "version": "3",
  "name": "invalid-trigger-candidate",
  "entry": "work",
  "nodes": [{
    "id": "work",
    "execution": {
      "candidates": [{"executor": "typo"}, {"executor": "process"}],
      "fallback": {"when": "failure.kind == 'configuration_failed'"}
    }
  }],
  "edges": [{"from": "work", "to": "$end"}]
}`
	if err := os.WriteFile(workflowPath, []byte(workflow), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(root, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Start(context.Background(), StartRunCommand{WorkflowPath: workflowPath, Inputs: map[string]string{}, TriggerID: "hook", TriggerKind: "webhook", DeliveryID: "d-1", ReceivedAt: time.Now().UTC()})
	if err == nil || !strings.Contains(err.Error(), `candidate 1 references unregistered executor "typo"`) {
		t.Fatalf("expected unregistered executor error, got %v", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(root, "runs"))
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid trigger workflow created %d run(s)", len(entries))
	}
}

func TestDeliveryAndConcurrencyAreScopedToTrigger(t *testing.T) {
	root := t.TempDir()
	flow := `{"version":"3","name":"shared-target","entry":"wait","nodes":[{"id":"wait","execution":{"candidates":[{"executor":"external"}]}}],"edges":[{"from":"wait","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(root, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	config := Config{Triggers: []Definition{
		{ID: "first", Enabled: true, Source: SourceConfig{Kind: "webhook"}, Target: TargetConfig{Workflow: "workflow.json"}},
		{ID: "second", Enabled: true, Source: SourceConfig{Kind: "webhook"}, Target: TargetConfig{Workflow: "workflow.json"}},
	}}
	dispatcher := &Dispatcher{Root: root, Config: config, Starter: runtime}
	ctx := context.Background()
	event := Event{TriggerID: "first", SourceKind: "webhook", DeliveryID: "same-delivery", ReceivedAt: time.Now().UTC()}
	first, err := dispatcher.Dispatch(ctx, event)
	if err != nil || first.Status != "queued" {
		t.Fatalf("first trigger: %+v, %v", first, err)
	}
	event.TriggerID = "second"
	second, err := dispatcher.Dispatch(ctx, event)
	if err != nil || second.Duplicate || second.Status != "queued" || second.Run.RunID == first.Run.RunID {
		t.Fatalf("delivery leaked between triggers: %+v, %v", second, err)
	}
	replayed, err := dispatcher.Dispatch(ctx, event)
	if err != nil || !replayed.Duplicate || replayed.Run.RunID != second.Run.RunID {
		t.Fatalf("replay: %+v, %v", replayed, err)
	}
	event.DeliveryID = "next-delivery"
	skipped, err := dispatcher.Dispatch(ctx, event)
	if err != nil || skipped.Status != "skipped" || skipped.Reason != "active_run" {
		t.Fatalf("default overlap policy: %+v, %v", skipped, err)
	}
	dispatcher.Config.Triggers[1].Policy.Concurrency = "allow"
	event.DeliveryID = "allowed-delivery"
	allowed, err := dispatcher.Dispatch(ctx, event)
	if err != nil || allowed.Status != "queued" {
		t.Fatalf("explicit overlap policy: %+v, %v", allowed, err)
	}
}

func TestConcurrentDispatchersDeduplicateOneDelivery(t *testing.T) {
	root := t.TempDir()
	starter := &fakeStarter{next: "run_1"}
	config := Config{Triggers: []Definition{{ID: "shared", Enabled: true, Source: SourceConfig{Kind: "webhook"}, Target: TargetConfig{Workflow: "workflow.json"}}}}
	// Each dispatcher represents a separate service/CLI delivery path. Its
	// in-memory mutex cannot protect the other instance.
	results := make(chan DispatchResult, 2)
	errors := make(chan error, 2)
	start := make(chan struct{})
	for index := 0; index < 2; index++ {
		dispatcher := &Dispatcher{Root: root, Config: config, Starter: starter}
		go func() {
			<-start
			result, err := dispatcher.Dispatch(context.Background(), Event{TriggerID: "shared", SourceKind: "webhook", DeliveryID: "one-delivery", ReceivedAt: time.Now().UTC()})
			results <- result
			errors <- err
		}()
	}
	close(start)
	duplicates := 0
	for index := 0; index < 2; index++ {
		result := <-results
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		if result.Duplicate {
			duplicates++
		}
	}
	if duplicates != 1 || starter.calls != 1 {
		t.Fatalf("duplicate starts: duplicates=%d, starts=%d", duplicates, starter.calls)
	}
}

type fakeStarter struct {
	next  string
	calls int
}

func (f *fakeStarter) Prepare(_ context.Context, command StartRunCommand) (StartRunCommand, error) {
	return command, nil
}

func (f *fakeStarter) ValidateReplay(context.Context, StartRunCommand, RunRef) error { return nil }

func (f *fakeStarter) Start(context.Context, StartRunCommand) (RunRef, error) {
	f.calls++
	return RunRef{RunID: f.next, Status: "queued"}, nil
}

func TestDeliveryStatusViewReadsTheRunsCurrentStatus(t *testing.T) {
	root := t.TempDir()
	workflow := model.Workflow{Version: "3", Name: "daily", Entry: "a", Nodes: []model.Node{{ID: "a", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "external"}}}}}, Edges: []model.Transition{{From: "a", To: "$end"}}}
	finished, err := store.CreateRun(root, workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	finished.State.Status = "succeeded"
	if err := store.SaveState(finished); err != nil {
		t.Fatal(err)
	}
	waiting, err := store.CreateRun(root, workflow, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	waiting.State.Status = "waiting_input"
	if err := store.SaveState(waiting); err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{Root: root}
	for _, receipt := range []DeliveryReceipt{
		{TriggerID: "daily", DeliveryID: "d1", Status: "queued", Run: RunRef{RunID: finished.RunID, Status: "queued"}, ReceivedAt: "2026-10-28T08:15:00Z"},
		{TriggerID: "daily", DeliveryID: "d2", Status: "queued", Run: RunRef{RunID: waiting.RunID, Status: "queued"}, ReceivedAt: "2026-10-29T08:15:00Z"},
		{TriggerID: "daily", DeliveryID: "d3", Status: "queued", Run: RunRef{RunID: "run_gone", Status: "queued"}, ReceivedAt: "2026-10-27T08:15:00Z"},
		{TriggerID: "daily", DeliveryID: "d4", Status: "skipped", Reason: "active_run", ReceivedAt: "2026-10-30T08:15:00Z"},
	} {
		if err := dispatcher.saveReceipt(receipt); err != nil {
			t.Fatal(err)
		}
	}
	views, err := DeliveryStatusView(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]DeliveryView{}
	for _, view := range views {
		got[view.DeliveryID] = view
	}
	if views[0].DeliveryID != "d4" || views[3].DeliveryID != "d3" {
		t.Fatalf("newest first: %v, %v", views[0].DeliveryID, views[3].DeliveryID)
	}
	if view := got["d1"]; view.Status != "delivered" || view.RunStatus != "succeeded" || view.Run.Status != "succeeded" {
		t.Fatalf("a finished run must not show as queued: %+v", view)
	}
	if view := got["d2"]; view.RunStatus != "waiting_input" {
		t.Fatalf("waiting run: %+v", view)
	}
	if view := got["d3"]; view.RunStatus != "missing" {
		t.Fatalf("deleted run: %+v", view)
	}
	if view := got["d4"]; view.Status != "skipped" || view.RunStatus != "" {
		t.Fatalf("skipped delivery: %+v", view)
	}
	// the receipts on disk are left as written: the dispatcher still reads them
	if receipt, err := dispatcher.loadReceipt("daily", "d1"); err != nil || receipt.Status != "queued" {
		t.Fatalf("receipt changed on disk: %+v, %v", receipt, err)
	}
}

func TestADamagedRunOfAnotherWorkflowDoesNotBlockDispatch(t *testing.T) {
	root := t.TempDir()
	flow := `{"version":"3","name":"target","entry":"wait","nodes":[{"id":"wait","execution":{"candidates":[{"executor":"external"}]}}],"edges":[{"from":"wait","to":"$end"}]}`
	if err := os.WriteFile(filepath.Join(root, "workflow.json"), []byte(flow), 0o644); err != nil {
		t.Fatal(err)
	}
	// another workflow's run whose snapshot is damaged: its state says it is
	// not this workflow, so neither the idempotency check nor the active-run
	// check needs the rest of it
	broken := filepath.Join(root, "runs", "run_20260101000000_broken")
	os.MkdirAll(broken, 0o755)
	os.WriteFile(filepath.Join(broken, "state.json"), []byte(`{"runId":"run_20260101000000_broken","workflowName":"other","idempotency_key":"k","status":"succeeded"}`), 0o644)
	os.WriteFile(filepath.Join(broken, "workflow.json"), []byte("{not json"), 0o644)
	runtime, err := NewRuntime(root, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &Dispatcher{Root: root, Starter: runtime, Config: Config{Triggers: []Definition{
		{ID: "hook", Enabled: true, Source: SourceConfig{Kind: "webhook"}, Target: TargetConfig{Workflow: "workflow.json"}}}}}
	result, err := dispatcher.Dispatch(context.Background(), Event{TriggerID: "hook", SourceKind: "webhook", DeliveryID: "d1", ReceivedAt: time.Now().UTC()})
	if err != nil || result.Status != "queued" {
		t.Fatalf("a damaged run of another workflow must not block dispatch: %+v, %v", result, err)
	}
}
