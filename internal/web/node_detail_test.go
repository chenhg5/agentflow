package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestNodeDetailVisitsAndValues(t *testing.T) {
	root := t.TempDir()
	run, err := store.CreateRun(root, model.Workflow{Version: "3", Name: "inspect", Entry: "step", Nodes: []model.Node{{ID: "step"}}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	run.State.Visits["step"] = 2
	run.State.Nodes["step"] = &model.NodeState{Status: "running", Input: map[string]any{}, SessionID: "session-2", Artifacts: map[string]string{}}
	run.State.History = []model.NodeVisit{{NodeID: "step", Visit: 1, State: model.NodeState{Status: "succeeded", Input: []any{}, Result: false, SessionID: "session-1", Artifacts: map[string]string{"stdoutLog": "artifacts/first.log"}}}}
	if err := store.WriteArtifact(run.RunDir, "first.log", []byte("first visit log")); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveState(run); err != nil {
		t.Fatal(err)
	}
	server := NewServer(Options{Root: root})
	get := func(visit string) map[string]any {
		t.Helper()
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/api/node?run="+run.RunID+"&node=step&visit="+visit, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("visit %s: %d %s", visit, response.Code, response.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	current := get("current")
	input := current["input"].(map[string]any)
	if !input["available"].(bool) || len(input["value"].(map[string]any)) != 0 || current["output"].(map[string]any)["available"] != false {
		t.Fatalf("current visit should retain {} and distinguish pending output: %#v", current)
	}
	first := get("1")
	if first["input"].(map[string]any)["available"] != true || first["output"].(map[string]any)["value"] != false || first["state"].(map[string]any)["sessionId"] != "session-1" {
		t.Fatalf("historical input/output/session mixed with latest visit: %#v", first)
	}
	if first["logs"].(map[string]any)["stdout"].(map[string]any)["text"] != "first visit log" {
		t.Fatal("historical logs missing")
	}
	for _, query := range []string{"run=../other&node=step", "run=" + run.RunID + "&node=missing", "run=" + run.RunID + "&node=step&visit=999", "run=" + run.RunID + "&node=step&visit=0"} {
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/api/node?"+query, nil))
		if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
			t.Fatalf("unexpected status for %s: %d", query, response.Code)
		}
	}
}

func TestInspectionArtifacts(t *testing.T) {
	runDir := t.TempDir()
	for _, data := range []string{"null", "{}", "[]", "false", "0", `""`} {
		if err := os.WriteFile(filepath.Join(runDir, "result.json"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		value := inspectValue(runDir, nil, "result.json")
		encoded, _ := json.Marshal(value.Value)
		if !value.Available || string(encoded) != data {
			t.Fatalf("lost recorded JSON %s: %+v", data, value)
		}
	}
	large := strings.Repeat("x", int(logPreviewBytes)) + "last line"
	if err := os.WriteFile(filepath.Join(runDir, "stdout.log"), []byte(large), 0o600); err != nil {
		t.Fatal(err)
	}
	log := inspectLog(runDir, "stdout.log")
	if !log.Available || !log.Truncated || len(log.Text) != int(logPreviewBytes) || !strings.HasSuffix(log.Text, "last line") {
		t.Fatalf("incorrect log tail: available=%v truncated=%v size=%d", log.Available, log.Truncated, len(log.Text))
	}
	out := filepath.Join(t.TempDir(), "private.log")
	if err := os.WriteFile(out, []byte("outside-run"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(runDir, "escape.log")); err == nil {
		if escaped := inspectLog(runDir, "escape.log"); escaped.Available || escaped.Error == "" {
			t.Fatalf("symlink outside run exposed: %+v", escaped)
		}
	}
	if escaped := inspectLog(runDir, "../private.log"); escaped.Available {
		t.Fatalf("path outside run exposed: %+v", escaped)
	}
}
