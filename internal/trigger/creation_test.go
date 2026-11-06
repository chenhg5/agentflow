package trigger

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestWebhookReplayChecksChangedRequest(t *testing.T) {
	root := t.TempDir()
	workflow := model.Workflow{Version: "3", Name: "replay", Entry: "wait", Nodes: []model.Node{{ID: "wait", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "external"}}}}}, Edges: []model.Transition{{From: "wait", To: "$end"}}}
	writeWorkflow := func() {
		t.Helper()
		data, _ := json.Marshal(workflow)
		if err := os.WriteFile(filepath.Join(root, "workflow.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeWorkflow()
	runner, err := NewRuntime(root, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	definition := Definition{ID: "hook", Enabled: true, Source: SourceConfig{Kind: "webhook", Path: "/hook", Method: "POST", DeliveryIDHeader: "X-Delivery"}, Target: TargetConfig{Workflow: "workflow.json"}, Bindings: map[string]Binding{"value": {From: "json", Path: "$.value"}}}
	dispatcher := &Dispatcher{Root: root, Config: Config{Triggers: []Definition{definition}}, Starter: runner}
	handler := (WebhookSource{Definition: definition}).Handler(dispatcher)
	request := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewBufferString(body))
		req.Header.Set("X-Delivery", "same")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response.Code
	}
	if code := request(`{"value":"original"}`); code != http.StatusAccepted {
		t.Fatalf("first status=%d", code)
	}
	if code := request(`{"value":"original"}`); code != http.StatusAccepted {
		t.Fatalf("replay status=%d", code)
	}
	if code := request(`{"value":"changed"}`); code != http.StatusConflict {
		t.Fatalf("changed input status=%d", code)
	}
	workflow.Nodes[0].TimeoutMs = 500
	writeWorkflow()
	if code := request(`{"value":"original"}`); code != http.StatusConflict {
		t.Fatalf("changed graph status=%d", code)
	}
	ids, err := store.RunIDs(root)
	if err != nil || len(ids) != 1 {
		t.Fatalf("replay created runs=%d err=%v", len(ids), err)
	}
}
