package telemetry_test

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/engine"
	"github.com/chenhg5/agentflow/internal/hub"
	"github.com/chenhg5/agentflow/internal/hub/app"
	"github.com/chenhg5/agentflow/internal/hub/domain"
	"github.com/chenhg5/agentflow/internal/hub/store/memory"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
	"github.com/chenhg5/agentflow/internal/telemetry"
)

// A real run, executed by the engine, ends up on a real Hub.
func TestRunsReachTheHub(t *testing.T) {
	hubStore := memory.New()
	handler, err := hub.Handler(hubStore, hub.Options{IngestToken: "tok"}, true)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("AGENTFLOW_HUB_URL", server.URL)
	t.Setenv("AGENTFLOW_HUB_TOKEN", "tok")
	t.Setenv("AGENTFLOW_MACHINE", "ci")

	root := t.TempDir()
	stop := telemetry.Start(root, time.Hour, 5*time.Second)
	run, err := store.CreateRun(root, oneStep(`printf '{"summary":"said hello"}' > "$AGENTFLOW_RESULT_FILE"`), map[string]string{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Execute(run, nil); err != nil {
		t.Fatal(err)
	}
	stop()

	got, err := app.Query{Store: hubStore}.Run(context.Background(), domain.RunKey{Machine: "ci", RunID: run.RunID})
	if err != nil {
		t.Fatalf("run not on the hub: %v", err)
	}
	if got.Status != domain.StatusSucceeded || got.Workflow != "hello" || len(got.Steps) != 1 || got.Steps[0].Summary != "said hello" || got.Steps[0].Type != "process" {
		t.Fatalf("hub has %+v", got)
	}
	if pending, _ := telemetry.NewOutbox(root).Pending(); len(pending) != 0 {
		t.Fatalf("outbox not drained: %d", len(pending))
	}
}

func TestStartIsANoOpWithoutAHub(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	t.Setenv("AGENTFLOW_HUB_URL", "")
	root := t.TempDir()
	stop := telemetry.Start(root, time.Hour, time.Second)
	run, _ := store.CreateRun(root, oneStep(`printf '{}' > "$AGENTFLOW_RESULT_FILE"`), map[string]string{}, "")
	engine.Execute(run, nil)
	stop()
	if pending, _ := telemetry.NewOutbox(root).Pending(); len(pending) != 0 {
		t.Fatal("nothing should be recorded without a hub")
	}
}

func oneStep(script string) model.Workflow {
	return model.Workflow{Version: "3", Name: "hello", Entry: "say",
		Nodes: []model.Node{{ID: "say", Execution: model.Execution{Candidates: []model.ExecutorCandidate{
			{Executor: "process", Config: map[string]any{"command": []string{"sh", "-c", script}}},
		}}}},
		Edges: []model.Transition{{From: "say", To: "$end"}},
	}
}
