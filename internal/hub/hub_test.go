package hub_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
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
	request.Header.Set("Content-Type", "application/json")
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
		DurationMs: 30000, Error: "probe: exit 3", Inputs: map[string]string{"issue": "ABC-1"}, WorkflowDigest: "d1",
		Graph: &hubapi.Graph{Entry: "fetch", Nodes: []hubapi.GraphNode{{ID: "fetch", Executors: []string{"process"}}, {ID: "probe", Executors: []string{"claude-code"}}},
			Edges: []hubapi.GraphEdge{{Source: "fetch", Target: "probe", Condition: "x < 1 && y > \"<b>\""}, {Source: "probe", Target: "$end"}}},
		Steps: []hubapi.StepReport{
			{ID: "fetch", Type: "shell", Status: "succeeded", Attempts: 1, StartedAt: started.Format(time.RFC3339Nano), FinishedAt: started.Add(10 * time.Second).Format(time.RFC3339Nano), DurationMs: 10000, Summary: "拉到 42 条"},
			{ID: "probe", Type: "agent", Provider: "claude", Status: "failed", Attempts: 2, StderrTail: "…<b>fatal</b>: boom", Result: `{"why":"nope"}`,
				Artifacts: []hubapi.Artifact{{Name: "stderrLog", Bytes: 3000}}, StartedAt: started.Add(10 * time.Second).Format(time.RFC3339Nano), FinishedAt: started.Add(30 * time.Second).Format(time.RFC3339Nano), DurationMs: 20000, Error: "exit 3", Reason: "<script>alert(1)</script>"},
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
	body, _ := json.Marshal(sample())
	response, _ = http.Post(srv.URL+hubapi.IngestPath, "text/plain", bytes.NewReader(body))
	response.Body.Close()
	if response.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("a form or text/plain post (what a web page can send cross-origin) must be refused: %d", response.StatusCode)
	}
}

func TestIngestTranslatesTheProtocol(t *testing.T) {
	srv := server(t, hub.Options{}, true)
	batch := sample()
	code := batch.Reports[0]
	code.RunID, code.Orchestration = "run_code", hubapi.OrchestrationCode
	future := batch.Reports[0]
	future.RunID, future.Protocol = "run_future", 99
	batch.Reports = append(batch.Reports, code, future)
	var result hubapi.IngestResult
	json.NewDecoder(post(t, srv.URL, "", batch).Body).Decode(&result)
	if result.Accepted != 2 || len(result.Rejected) != 1 || !strings.Contains(result.Rejected[0], "run_future") {
		t.Fatalf("result = %+v", result)
	}
	_, body := get(t, srv.URL+"/api/v1/runs/laptop/run_code", nil)
	if !strings.Contains(body, `"orchestration":"code"`) {
		t.Fatalf("code orchestration lost: %s", body)
	}
	_, body = get(t, srv.URL+"/api/v1/runs/laptop/run_1", nil)
	if !strings.Contains(body, `"orchestration":"json"`) {
		t.Fatalf("json orchestration default lost: %s", body)
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
		"/runs/laptop/run_1":            {"失败在 <span class=\"mono\">probe</span>", "拉到 42 条", "ABC-1", "&lt;script&gt;alert(1)&lt;/script&gt;", "×2", "流程图", "data-flow=", "/static/flow.js", "结果和日志 · 1 个文件", "&lt;b&gt;fatal&lt;/b&gt;: boom", "stderrLog · 2.9 KB"},
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
	if _, body := get(t, srv.URL+"/runs/laptop/run_1", nil); strings.Contains(body, `"<b>`) || !strings.Contains(body, `\u003cb\u003e`) {
		t.Error("graph data in the page must stay HTML-escaped")
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

func TestRunPageWithoutGraphHasNoCanvas(t *testing.T) {
	srv := server(t, hub.Options{}, true)
	batch := sample()
	batch.Reports[0].Graph = nil
	post(t, srv.URL, "", batch)
	_, body := get(t, srv.URL+"/runs/laptop/run_1", nil)
	if strings.Contains(body, "data-flow") {
		t.Fatal("a run reported without a graph shows no canvas")
	}
}

func waitingBatch(capable bool) hubapi.Batch {
	started := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	source := hubapi.Source{Machine: "laptop", Version: "v1"}
	if capable {
		source.Capabilities = []string{hubapi.CapabilitySubmit}
	}
	return hubapi.Batch{Reports: []hubapi.RunReport{{
		Protocol: 1, Source: source, RunID: "run_w", Workflow: "review", WorkflowDigest: "d2", Status: "waiting_input", Seq: 1, StartedAt: started,
		Steps: []hubapi.StepReport{{ID: "decide", Type: "external", Status: "waiting_input", StartedAt: started}},
		Graph: &hubapi.Graph{Entry: "decide", Nodes: []hubapi.GraphNode{{ID: "decide", Executors: []string{"external"},
			OutputSchema: map[string]any{"type": "object", "required": []any{"decision"}, "properties": map[string]any{"decision": map[string]any{"type": "string", "enum": []any{"approve", "hold"}}}}}},
			Edges: []hubapi.GraphEdge{{Source: "decide", Target: "$end"}}},
	}}}
}

func TestSubmitInputFromThePage(t *testing.T) {
	srv := server(t, hub.Options{IngestToken: "tok"}, true)
	post(t, srv.URL, "tok", waitingBatch(true))
	_, page := get(t, srv.URL+"/runs/laptop/run_w", nil)
	if !strings.Contains(page, `name="f.decision" value="approve"`) || !strings.Contains(page, `name="csrf"`) {
		t.Fatal("the run page offers a form for the waiting step")
	}
	csrf := regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`).FindStringSubmatch(page)[1]
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(form url.Values, origin string) *http.Response {
		request, _ := http.NewRequest(http.MethodPost, srv.URL+"/runs/laptop/run_w/submit", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response, err := noRedirect.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response
	}
	if response := send(url.Values{"csrf": {csrf}, "node": {"decide"}, "f.decision": {"approve"}}, "https://evil.example"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site post: %d", response.StatusCode)
	}
	if response := send(url.Values{"csrf": {"0000"}, "node": {"decide"}, "f.decision": {"approve"}}, ""); response.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong csrf token: %d", response.StatusCode)
	}
	if response := send(url.Values{"csrf": {csrf}, "node": {"decide"}, "f.decision": {"drop tables"}}, srv.URL); response.StatusCode != http.StatusSeeOther || !strings.Contains(response.Header.Get("Location"), "problem=") {
		t.Fatalf("invalid choice: %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	if response := send(url.Values{"csrf": {csrf}, "node": {"decide"}, "f.decision": {"approve"}}, srv.URL); response.StatusCode != http.StatusSeeOther || !strings.Contains(response.Header.Get("Location"), "notice=") {
		t.Fatalf("valid submission: %d %s", response.StatusCode, response.Header.Get("Location"))
	}

	// the runner pulls it with its token, and only its own machine's commands
	pull := func(machine, token string) (int, hubapi.CommandList) {
		request, _ := http.NewRequest(http.MethodGet, srv.URL+hubapi.CommandsPath+"?machine="+machine, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var list hubapi.CommandList
		json.NewDecoder(response.Body).Decode(&list)
		return response.StatusCode, list
	}
	if status, _ := pull("laptop", "wrong"); status != http.StatusUnauthorized {
		t.Fatalf("pull without the token: %d", status)
	}
	_, list := pull("laptop", "tok")
	if len(list.Commands) != 1 || list.Commands[0].NodeID != "decide" || list.Commands[0].Output["decision"] != "approve" || list.Commands[0].StepStartedAt == "" || list.Commands[0].By != "web" {
		t.Fatalf("pulled = %+v", list)
	}
	if _, other := pull("server", "tok"); len(other.Commands) != 0 {
		t.Fatal("another machine sees nothing")
	}
	body := strings.NewReader(`{"status":"applied"}`)
	request, _ := http.NewRequest(http.MethodPost, srv.URL+hubapi.CommandsPath+"/"+list.Commands[0].ID+"?machine=laptop", body)
	request.Header.Set("Authorization", "Bearer tok")
	request.Header.Set("Content-Type", "application/json")
	response, _ := http.DefaultClient.Do(request)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("answer: %d", response.StatusCode)
	}
	if _, again := pull("laptop", "tok"); len(again.Commands) != 0 {
		t.Fatal("an answered command is not pulled again")
	}
	if _, page := get(t, srv.URL+"/runs/laptop/run_w", nil); !strings.Contains(page, "已执行") {
		t.Fatal("the run page shows the outcome")
	}
}

func TestNoFormForAMachineThatDidNotOptIn(t *testing.T) {
	srv := server(t, hub.Options{}, true)
	post(t, srv.URL, "", waitingBatch(false))
	_, page := get(t, srv.URL+"/runs/laptop/run_w", nil)
	if strings.Contains(page, `name="csrf"`) || !strings.Contains(page, "accept_commands") {
		t.Fatal("without the capability the page explains how to enable it instead of offering a form")
	}
	request, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/runs/laptop/run_w/submit", strings.NewReader(`{"node_id":"decide","output":{"decision":"approve"}}`))
	request.Header.Set("Content-Type", "application/json")
	response, _ := http.DefaultClient.Do(request)
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("api submit to a machine that did not opt in: %d", response.StatusCode)
	}
}

func TestWorkflowLabelsOnPagesAndAPI(t *testing.T) {
	srv := server(t, hub.Options{}, true)
	batch := sample()
	labelled := batch.Reports[0]
	labelled.WorkflowTitle, labelled.WorkflowDescription = "夜间巡检", "每晚检查 <b>服务</b> 是否正常"
	labelled.Tags, labelled.Group = []string{"ops", "nightly", "ops"}, "infra"
	other := batch.Reports[0]
	other.RunID, other.Workflow, other.WorkflowDigest, other.Status, other.Error = "run_2", "pr-daily", "d2", "succeeded", ""
	other.Tags = []string{"github"}
	post(t, srv.URL, "", hubapi.Batch{Reports: []hubapi.RunReport{labelled, other}})

	for path, want := range map[string][]string{
		"/workflows":                    {"infra", "未分组", "夜间巡检", "nightly", "#ops", "#github", "&lt;b&gt;服务&lt;/b&gt;"},
		"/workflows?tag=github":         {"pr-daily", "未分组"},
		"/workflows?group=infra":        {"夜间巡检", "#ops"},
		"/runs":                         {"夜间巡检", "全部分组", "全部标签"},
		"/runs?group=infra":             {"run_1", "分组 infra"},
		"/runs?workflow=nightly":        {"夜间巡检", "每晚检查"},
		"/runs/laptop/run_1":            {"夜间巡检", "nightly · run_1", "#ops", ">infra<"},
		"/api/v1/runs?tag=ops":          {`"total":1`, `"title":"夜间巡检"`, `"tags":["ops","nightly"]`, `"group":"infra"`},
		"/api/v1/runs?tag=ops,github":   {`"total":0`},
		"/api/v1/runs?group=infra":      {`"total":1`},
		"/api/v1/workflows":             {`"title":"夜间巡检"`, `"title":"pr-daily"`, `"groups":["infra"]`, `"tag":"github"`},
		"/api/v1/workflows?group=infra": {`"name":"nightly"`},
		"/api/v1/runs/laptop/run_1":     {`"description":"每晚检查 ` + "\\u003cb\\u003e" + `服务`},
	} {
		status, body := get(t, srv.URL+path, nil)
		if status != http.StatusOK {
			t.Errorf("%s: status %d", path, status)
			continue
		}
		for _, fragment := range want {
			if !strings.Contains(body, fragment) {
				t.Errorf("%s: missing %q", path, fragment)
			}
		}
	}
	for path, absent := range map[string]string{
		"/workflows?tag=github":         "夜间巡检",
		"/workflows?group=infra":        "pr-daily",
		"/api/v1/workflows?group=infra": `"name":"pr-daily"`,
	} {
		if _, body := get(t, srv.URL+path, nil); strings.Contains(body, absent) {
			t.Errorf("%s: should not list %q", path, absent)
		}
	}
	if _, body := get(t, srv.URL+"/workflows", nil); strings.Contains(body, "<b>服务") {
		t.Error("descriptions must be escaped")
	}
}
