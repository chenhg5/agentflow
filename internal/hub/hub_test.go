package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/hub"
	"github.com/chenhg5/agentflow/internal/hub/store/memory"
	"github.com/chenhg5/agentflow/internal/hubapi"
)

func server(t *testing.T, options hub.Options, loopback bool) *httptest.Server {
	t.Helper()
	handler, err := hub.Handler(memory.New(), options, loopback)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func post(t *testing.T, url, token string, batch hubapi.Batch) *http.Response {
	t.Helper()
	body, _ := json.Marshal(batch)
	request, _ := http.NewRequest(http.MethodPost, url+hubapi.IngestPath, bytes.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { response.Body.Close() })
	return response
}

func get(t *testing.T, url string, auth func(*http.Request)) (int, string) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, url, nil)
	if auth != nil {
		auth(request)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

func sample() hubapi.Batch {
	started := time.Now().Add(-time.Minute).UTC()
	return hubapi.Batch{Reports: []hubapi.RunReport{{
		Protocol: 1, Source: hubapi.Source{Machine: "laptop", Version: "v1"}, RunID: "run_1", Workflow: "nightly",
		Status: "failed", Seq: 1, StartedAt: started.Format(time.RFC3339Nano), FinishedAt: started.Add(30 * time.Second).Format(time.RFC3339Nano),
		DurationMs: 30000, Error: "probe: exit 3", Inputs: map[string]string{"issue": "ABC-1"},
		Steps: []hubapi.StepReport{
			{ID: "fetch", Type: "shell", Status: "succeeded", Attempts: 1, StartedAt: started.Format(time.RFC3339Nano), FinishedAt: started.Add(10 * time.Second).Format(time.RFC3339Nano), DurationMs: 10000, Summary: "拉到 42 条"},
			{ID: "probe", Type: "agent", Provider: "claude", Status: "failed", Attempts: 2, StartedAt: started.Add(10 * time.Second).Format(time.RFC3339Nano), FinishedAt: started.Add(30 * time.Second).Format(time.RFC3339Nano), DurationMs: 20000, Error: "exit 3", Reason: "<script>alert(1)</script>"},
		},
	}}}
}

func TestIngestNeedsTheToken(t *testing.T) {
	srv := server(t, hub.Options{IngestToken: "secret"}, true)
	if response := post(t, srv.URL, "", sample()); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", response.StatusCode)
	}
	if response := post(t, srv.URL, "wrong", sample()); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", response.StatusCode)
	}
	response := post(t, srv.URL, "secret", sample())
	var result hubapi.IngestResult
	json.NewDecoder(response.Body).Decode(&result)
	if response.StatusCode != http.StatusOK || result.Accepted != 1 {
		t.Fatalf("status %d result %+v", response.StatusCode, result)
	}
}

func TestIngestRefusesOversizedBatchAndBadJSON(t *testing.T) {
	srv := server(t, hub.Options{}, true)
	batch := hubapi.Batch{Reports: make([]hubapi.RunReport, hubapi.MaxBatch+1)}
	if response := post(t, srv.URL, "", batch); response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized batch: %d", response.StatusCode)
	}
	response, _ := http.Post(srv.URL+hubapi.IngestPath, "application/json", strings.NewReader("{not json"))
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad json: %d", response.StatusCode)
	}
}

func TestPagesAndReadAPI(t *testing.T) {
	srv := server(t, hub.Options{Version: "v-test"}, true)
	post(t, srv.URL, "", sample())
	for path, want := range map[string][]string{
		"/":                             {"总览", "nightly", "最近失败", "<svg class=\"chart\"", "v-test"},
		"/runs":                         {"run_1", "probe: exit 3"},
		"/runs?status=failed":           {"run_1"},
		"/runs?status=succeeded":        {"没有符合条件的运行"},
		"/runs/laptop/run_1":            {"失败在 <span class=\"mono\">probe</span>", "拉到 42 条", "ABC-1", "&lt;script&gt;alert(1)&lt;/script&gt;", "×2"},
		"/workflows":                    {"nightly"},
		"/machines":                     {"laptop"},
		"/api/v1/overview?days=7":       {`"days":7`, `"failed":1`},
		"/api/v1/runs?workflow=nightly": {`"total":1`, `"run_id":"run_1"`},
		"/api/v1/runs/laptop/run_1":     {`"steps"`, `"summary":"拉到 42 条"`},
		"/api/v1/workflows":             {`"name":"nightly"`},
		"/api/v1/machines":              {`"id":"laptop"`},
		"/healthz":                      {`"ok":true`},
		"/static/hub.css":               {"--background"},
	} {
		status, body := get(t, srv.URL+path, nil)
		if status != http.StatusOK {
			t.Errorf("%s: status %d: %s", path, status, body)
			continue
		}
		for _, fragment := range want {
			if !strings.Contains(body, fragment) {
				t.Errorf("%s: missing %q", path, fragment)
			}
		}
		if strings.Contains(body, "<script>alert(1)") {
			t.Errorf("%s: step text was not escaped", path)
		}
	}
	if status, _ := get(t, srv.URL+"/runs/laptop/missing", nil); status != http.StatusNotFound {
		t.Errorf("missing run page: %d", status)
	}
	if status, _ := get(t, srv.URL+"/api/v1/runs/laptop/missing", nil); status != http.StatusNotFound {
		t.Errorf("missing run api: %d", status)
	}
	if status, _ := get(t, srv.URL+"/api/v1/runs?status=bogus", nil); status != http.StatusBadRequest {
		t.Errorf("bad status filter: %d", status)
	}
}

func TestViewingBeyondLoopbackNeedsAToken(t *testing.T) {
	srv := server(t, hub.Options{IngestToken: "ingest"}, false)
	if status, _ := get(t, srv.URL+"/", nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous page: %d", status)
	}
	if status, _ := get(t, srv.URL+"/", func(r *http.Request) { r.SetBasicAuth("me", "ingest") }); status != http.StatusOK {
		t.Fatalf("basic auth page: %d", status)
	}
	if status, _ := get(t, srv.URL+"/api/v1/runs", func(r *http.Request) { r.Header.Set("Authorization", "Bearer ingest") }); status != http.StatusOK {
		t.Fatalf("bearer api: %d", status)
	}
	if status, _ := get(t, srv.URL+"/healthz", nil); status != http.StatusOK {
		t.Fatalf("healthz must stay open: %d", status)
	}

	separate := server(t, hub.Options{IngestToken: "ingest", ViewToken: "view"}, false)
	if status, _ := get(t, separate.URL+"/", func(r *http.Request) { r.SetBasicAuth("", "ingest") }); status != http.StatusUnauthorized {
		t.Fatalf("ingest token must not open pages when a view token is set: %d", status)
	}
	if status, _ := get(t, separate.URL+"/", func(r *http.Request) { r.SetBasicAuth("", "view") }); status != http.StatusOK {
		t.Fatalf("view token: %d", status)
	}
}

func TestNewRefusesAnOpenPublicHub(t *testing.T) {
	if _, err := hub.New(context.Background(), hub.Options{Host: "0.0.0.0", Store: "memory:"}); err == nil {
		t.Fatal("a hub beyond loopback without a token must not start")
	}
}

func TestOpenStore(t *testing.T) {
	for _, dsn := range []string{"memory:", "sqlite:" + t.TempDir() + "/a/hub.db", t.TempDir() + "/hub.db"} {
		store, err := hub.OpenStore(context.Background(), dsn)
		if err != nil {
			t.Fatalf("%s: %v", dsn, err)
		}
		store.Close()
	}
	if _, err := hub.OpenStore(context.Background(), "postgres://x"); err == nil {
		t.Fatal("unknown store kinds must be refused")
	}
}
