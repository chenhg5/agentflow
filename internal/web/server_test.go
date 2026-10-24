package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestWorkflowAndRunViews(t *testing.T) {
	root := t.TempDir()
	workflowPath := filepath.Join(root, "workflow.json")
	workflowData := []byte(`{"version":"2","name":"demo","entry":"build","nodes":[{"id":"build","type":"program","command":["true"]}],"edges":[{"from":"build","to":"$end"}]}`)
	if err := os.WriteFile(workflowPath, workflowData, 0o644); err != nil {
		t.Fatal(err)
	}

	workflowServer := NewServer(Options{Root: filepath.Join(root, ".agentflow"), WorkflowPath: workflowPath, Port: -1})
	workflowResponse := httptest.NewRecorder()
	workflowServer.Handler().ServeHTTP(workflowResponse, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if workflowResponse.Code != http.StatusOK {
		t.Fatalf("workflow state status = %d", workflowResponse.Code)
	}
	var workflowView map[string]any
	if err := json.Unmarshal(workflowResponse.Body.Bytes(), &workflowView); err != nil {
		t.Fatal(err)
	}
	if workflowView["mode"] != "workflow" {
		t.Fatalf("workflow mode = %v", workflowView["mode"])
	}
	graphView, ok := workflowView["graph"].(map[string]any)
	if !ok {
		t.Fatalf("workflow graph missing: %#v", workflowView["graph"])
	}
	if edges, ok := graphView["edges"].([]any); !ok || len(edges) != 1 {
		t.Fatalf("workflow graph edges = %#v", graphView["edges"])
	}

	run, err := store.CreateRun(filepath.Join(root, ".agentflow"), model.Workflow{Version: "3", Name: "demo", Entry: "build", Nodes: []model.Node{{ID: "build", Executor: "process", Config: map[string]any{"command": []string{"true"}}}}, Edges: []model.Transition{{From: "build", To: "$end"}}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := store.CreateRun(filepath.Join(root, ".agentflow"), model.Workflow{Version: "3", Name: "demo", Entry: "build", Nodes: []model.Node{{ID: "build", Executor: "process", Config: map[string]any{"command": []string{"true"}}}}, Edges: []model.Transition{{From: "build", To: "$end"}}}, nil, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	baseline.State.Status = "succeeded"
	baseline.State.CreatedAt = "2026-10-23T00:00:00Z"
	baseline.State.StartedAt = baseline.State.CreatedAt
	baseline.State.FinishedAt = "2026-10-23T00:00:01Z"
	baseline.State.Evaluation = &model.Evaluation{EvaluatedAt: baseline.State.FinishedAt, Measurements: map[string]model.Measurement{"quality": {Value: 0.5, Direction: "higher"}}}
	if err := store.SaveState(baseline); err != nil {
		t.Fatal(err)
	}
	run.State.Status = "succeeded"
	run.State.CreatedAt = "2026-10-24T00:00:00Z"
	run.State.StartedAt = run.State.CreatedAt
	run.State.FinishedAt = "2026-10-24T00:00:01Z"
	run.State.Evaluation = &model.Evaluation{EvaluatedAt: run.State.FinishedAt, Measurements: map[string]model.Measurement{"quality": {Value: 0.8, Direction: "higher"}}}
	if err := store.SaveState(run); err != nil {
		t.Fatal(err)
	}
	runServer := NewServer(Options{Root: filepath.Join(root, ".agentflow"), RunID: run.RunID, Port: -1})
	runResponse := httptest.NewRecorder()
	runServer.Handler().ServeHTTP(runResponse, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if runResponse.Code != http.StatusOK {
		t.Fatalf("run state status = %d", runResponse.Code)
	}
	var runViewPayload map[string]any
	if err := json.Unmarshal(runResponse.Body.Bytes(), &runViewPayload); err != nil {
		t.Fatal(err)
	}
	if runViewPayload["mode"] != "run" {
		t.Fatalf("run mode = %v", runViewPayload["mode"])
	}
	if _, ok := runViewPayload["metrics"]; ok {
		t.Fatal("metrics should be nested under run")
	}
	runPayload, ok := runViewPayload["run"].(map[string]any)
	if !ok || runPayload["run_id"] != run.RunID {
		t.Fatalf("unexpected run payload: %#v", runViewPayload["run"])
	}
	if _, ok := runViewPayload["graph"].(map[string]any); !ok {
		t.Fatalf("run graph missing: %#v", runViewPayload["graph"])
	}
	if _, ok := runViewPayload["comparison"].(map[string]any); !ok {
		t.Fatalf("run comparison missing: %#v", runViewPayload["comparison"])
	}
}
