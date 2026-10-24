package trigger

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	root := filepath.Join(t.TempDir(), ".agentflow")
	cwd := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(cwd, "workflow.json")
	workflow, err := json.Marshal(model.Workflow{Version: "3", Name: "triggered", Entry: "shell", Nodes: []model.Node{{ID: "shell", Executor: "process", Config: map[string]any{"command": []string{"sh", "-c", `printf '{"ok":true}' > "$AGENTFLOW_RESULT_FILE"`}}}}, Edges: []model.Transition{{From: "shell", To: "$end"}}})
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
	defer cancel()
	runtime.StartWorkers(ctx)
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

type fakeStarter struct {
	next  string
	calls int
}

func (f *fakeStarter) Start(context.Context, StartRunCommand) (RunRef, error) {
	f.calls++
	return RunRef{RunID: f.next, Status: "queued"}, nil
}
