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
	workflowData := []byte(`{"version":"1","name":"demo","nodes":[{"id":"build","type":"shell","command":["true"]}]}`)
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

	run, err := store.CreateRun(filepath.Join(root, ".agentflow"), model.Workflow{Version: "1", Name: "demo", Nodes: []model.Node{{ID: "build", Type: "shell", Command: []string{"true"}}}}, nil, "")
	if err != nil {
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
}
