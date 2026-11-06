package telemetry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/hubapi"
	"github.com/chenhg5/agentflow/internal/model"
)

func TestRedact(t *testing.T) {
	for _, secret := range []string{
		"sk-abcdefghijklmnopqrstuvwxyz123456", "ghp_abcdefghijklmnopqrstuvwxyz0123", "github_pat_abcdefghijklmnopqrstuvwxyz",
		"xoxb-1234567890-abcdef", "AKIAABCDEFGHIJKLMNOP", "lin_api_abcdefghijklmnop1234", "vck_abcdefghijklmnop1234",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghijklmnop",
		"glpat-abcdefghijklmnopqrst", "sk_live_abcdefghijklmnop1234", "npm_abcdefghijklmnopqrstuvwxyz0123456789",
		"AIzaSyA1234567890abcdefghijklmnopqrstuv",
	} {
		if got := Redact("leaked " + secret + " here"); strings.Contains(got, secret) || !strings.Contains(got, "***") {
			t.Errorf("%s not masked: %q", secret, got)
		}
	}
	for input, want := range map[string]string{
		"curl -H 'X: Bearer abcdefghijklmnop'": "curl -H 'X: Bearer ***'",
		"curl --data token=hunter2hunter2":     "curl --data token=***",
		`{"password": "correct-horse"}`:        `{"password": "***"}`,
		"exit status 1: tests failed":          "exit status 1: tests failed",
	} {
		if got := Redact(input); got != want {
			t.Errorf("Redact(%q) = %q, want %q", input, got, want)
		}
	}
	if got := Redact("Authorization: Bearer abcdefghijklmnop"); strings.Contains(got, "abcdefghijklmnop") {
		t.Errorf("authorization header not masked: %q", got)
	}
	if got := redactValue("GITHUB_TOKEN", "anything"); got != "***" {
		t.Errorf("secret-named input not masked: %q", got)
	}
	if got := redactValue("DATABASE_URL", "postgres://app:pw@db/main"); got != "postgres://***@db/main" {
		t.Errorf("DSN input not masked: %q", got)
	}
	if got := redactValue("issue", "ABC-1"); got != "ABC-1" {
		t.Errorf("plain input changed: %q", got)
	}
	if got := clip("一二三四五", 3); got != "一二三…" {
		t.Errorf("clip = %q", got)
	}
}

func sampleRun() *model.Run {
	start := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	at := func(seconds int) string {
		return start.Add(time.Duration(seconds) * time.Second).Format(time.RFC3339Nano)
	}
	return &model.Run{
		RunID: "run_1",
		Workflow: model.Workflow{Name: "nightly", Nodes: []model.Node{
			{ID: "fetch", Type: "shell"}, {ID: "review", Type: "agent", Provider: "claude"}, {ID: "post", Type: "shell"},
		}},
		State: model.RunState{
			RunID: "run_1", WorkflowName: "nightly", WorkflowDigest: "abc", Status: "failed",
			Inputs:    map[string]string{"issue": "ABC-1", "api_key": "s3cr3t"},
			CreatedAt: at(0), StartedAt: at(0), FinishedAt: at(30), UpdatedAt: at(30),
			Trigger: &model.TriggerInfo{ID: "nightly", Kind: "cron"},
			Nodes: map[string]*model.NodeState{
				"post":   {Status: "skipped", Reason: "upstream failed"},
				"fetch":  {Status: "succeeded", Attempts: 1, StartedAt: at(0), FinishedAt: at(10), Result: map[string]any{"summary": "42 issues", "reason": "since yesterday"}},
				"review": {Status: "failed", Attempts: 2, StartedAt: at(10), FinishedAt: at(30), Error: "401 for token=abcdefghijkl", StdoutBytes: 10, StderrBytes: 5},
			},
		},
	}
}

func TestFromRun(t *testing.T) {
	run := sampleRun()
	report := FromRun(run, hubapi.Source{Machine: "m1"}, time.Now())
	if report.Protocol != hubapi.ProtocolVersion || report.Orchestration != hubapi.OrchestrationJSON || report.Workflow != "nightly" || report.Status != "failed" {
		t.Fatalf("report header = %+v", report)
	}
	updated, _ := time.Parse(time.RFC3339Nano, run.State.UpdatedAt)
	if report.Seq != updated.UnixNano() {
		t.Fatalf("seq = %d", report.Seq)
	}
	if report.Trigger == nil || report.Trigger.Kind != "cron" {
		t.Fatalf("trigger = %+v", report.Trigger)
	}
	if len(report.Steps) != 3 || report.Steps[0].ID != "fetch" || report.Steps[1].ID != "review" || report.Steps[2].ID != "post" {
		t.Fatalf("steps must follow workflow order: %+v", report.Steps)
	}
	if report.Steps[0].Summary != "42 issues" || report.Steps[0].Reason != "since yesterday" || report.Steps[0].DurationMs != 10000 {
		t.Fatalf("fetch = %+v", report.Steps[0])
	}
	review := report.Steps[1]
	if strings.Contains(review.Error, "abcdefghijkl") || review.Attempts != 2 || review.Provider != "claude" || review.LogBytes != 15 {
		t.Fatalf("review = %+v", review)
	}
	if report.Steps[2].SkipReason != "upstream failed" {
		t.Fatalf("post = %+v", report.Steps[2])
	}
	if !strings.HasPrefix(report.Error, "review: 401") || strings.Contains(report.Error, "abcdefghijkl") {
		t.Fatalf("run error = %q", report.Error)
	}
	if report.Inputs["issue"] != "ABC-1" || report.Inputs["api_key"] != "***" {
		t.Fatalf("inputs = %+v", report.Inputs)
	}
}

func TestOutboxKeepsTheNewestReport(t *testing.T) {
	outbox := NewOutbox(t.TempDir())
	if pending, err := outbox.Pending(); err != nil || len(pending) != 0 {
		t.Fatalf("missing outbox: %v %v", pending, err)
	}
	outbox.Put(hubapi.RunReport{RunID: "b", Seq: 5})
	outbox.Put(hubapi.RunReport{RunID: "a", Seq: 9, Status: "running"})
	outbox.Put(hubapi.RunReport{RunID: "a", Seq: 3, Status: "pending"}) // older: ignored
	pending, err := outbox.Pending()
	if err != nil || len(pending) != 2 || pending[0].RunID != "b" || pending[1].Status != "running" {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	sent := pending[1]
	outbox.Put(hubapi.RunReport{RunID: "a", Seq: 12, Status: "succeeded"}) // arrives while sending
	outbox.Done(sent)
	outbox.Done(pending[0])
	pending, _ = outbox.Pending()
	if len(pending) != 1 || pending[0].Status != "succeeded" {
		t.Fatalf("a newer report must survive Done of an older one: %+v", pending)
	}
}

type fakeSender struct {
	err   error
	calls int
	got   []hubapi.RunReport
}

func (f *fakeSender) Send(_ context.Context, reports []hubapi.RunReport) (hubapi.IngestResult, error) {
	f.calls++
	if f.err != nil {
		return hubapi.IngestResult{}, f.err
	}
	f.got = append(f.got, reports...)
	return hubapi.IngestResult{Accepted: len(reports)}, nil
}

func TestFlushKeepsReportsWhenTheHubIsDown(t *testing.T) {
	outbox := NewOutbox(t.TempDir())
	sender := &fakeSender{err: errors.New("connection refused")}
	reporter := NewReporter(outbox, sender, hubapi.Source{Machine: "m1"})
	if err := reporter.Record(sampleRun()); err != nil {
		t.Fatal(err)
	}
	result, err := reporter.Flush(context.Background())
	if err == nil || result.Pending != 1 {
		t.Fatalf("flush = %+v, %v", result, err)
	}
	status, _ := LastStatus(outbox)
	if status == nil || !strings.Contains(status.Error, "refused") {
		t.Fatalf("status = %+v", status)
	}
	sender.err = nil
	result, err = reporter.Flush(context.Background())
	if err != nil || result.Sent != 1 || len(sender.got) != 1 || sender.got[0].Source.Machine != "m1" {
		t.Fatalf("retry = %+v, %v", result, err)
	}
	if pending, _ := outbox.Pending(); len(pending) != 0 {
		t.Fatalf("delivered report left behind: %+v", pending)
	}
	calls := sender.calls
	reporter.Flush(context.Background())
	status, _ = LastStatus(outbox)
	if sender.calls != calls || status.Result.Sent != 1 {
		t.Fatalf("an empty flush must not call the hub or overwrite the last status: calls %d status %+v", sender.calls, status)
	}
}

func TestFlushBatches(t *testing.T) {
	outbox := NewOutbox(t.TempDir())
	for index := 0; index < hubapi.MaxBatch+5; index++ {
		outbox.Put(hubapi.RunReport{RunID: "r" + string(rune('A'+index%26)) + string(rune('a'+index/26)), Seq: int64(index + 1)})
	}
	sender := &fakeSender{}
	result, err := NewReporter(outbox, sender, hubapi.Source{}).Flush(context.Background())
	if err != nil || sender.calls != 2 || result.Sent != hubapi.MaxBatch+5 {
		t.Fatalf("calls %d result %+v err %v", sender.calls, result, err)
	}
}

func TestLoadConfigLayers(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENTFLOW_HUB_URL", "")
	t.Setenv("AGENTFLOW_HUB_TOKEN", "")
	t.Setenv("AGENTFLOW_MACHINE", "")
	if config, err := LoadConfig(root); err != nil || config.Enabled() {
		t.Fatalf("no config: %+v %v", config, err)
	}
	os.MkdirAll(filepath.Join(home, ".agentflow"), 0o755)
	os.WriteFile(filepath.Join(home, ".agentflow", ConfigFile), []byte(`{"url":"http://home/","token":"t1","machine":"laptop"}`), 0o600)
	os.WriteFile(filepath.Join(root, ConfigFile), []byte(`{"url":"http://project"}`), 0o600)
	config, err := LoadConfig(root)
	if err != nil || config.URL != "http://project" || config.Token != "" || config.Machine != "laptop" {
		t.Fatalf("a project URL must not receive the user's token: %+v %v", config, err)
	}
	t.Setenv("AGENTFLOW_HUB_TOKEN", "t2")
	if config, _ := LoadConfig(root); config.Token != "" {
		t.Fatalf("nor the token from the environment: %+v", config)
	}
	os.WriteFile(filepath.Join(root, ConfigFile), []byte(`{"url":"http://project","token":"p1"}`), 0o600)
	if config, _ := LoadConfig(root); config.Token != "p1" {
		t.Fatalf("a project token goes with the project URL: %+v", config)
	}
	os.WriteFile(filepath.Join(root, ConfigFile), []byte(`{"machine":"build"}`), 0o600)
	if config, _ := LoadConfig(root); config.URL != "http://home" || config.Token != "t2" || config.Machine != "build" {
		t.Fatalf("user URL keeps the user token: %+v", config)
	}
	t.Setenv("AGENTFLOW_HUB_URL", "http://env")
	os.WriteFile(filepath.Join(root, ConfigFile), []byte(`{"url":"http://project"}`), 0o600)
	if config, _ := LoadConfig(root); config.URL != "http://env" || config.Token != "t2" {
		t.Fatalf("env URL wins and keeps the env token: %+v", config)
	}
	t.Setenv("AGENTFLOW_HUB_URL", "")
	os.WriteFile(filepath.Join(root, ConfigFile), []byte(`{`), 0o600)
	if _, err := LoadConfig(root); err == nil {
		t.Fatal("a broken config file must be reported")
	}
}

// The engine saves while a background flush is delivering: the newest snapshot
// must survive, whatever the interleaving.
func TestOutboxNeverDropsANewerSnapshotUnderConcurrency(t *testing.T) {
	for trial := 0; trial < 300; trial++ {
		outbox := NewOutbox(t.TempDir())
		outbox.Put(hubapi.RunReport{RunID: "r", Seq: 1})
		sent, _ := outbox.Pending()
		done := make(chan struct{})
		go func() {
			outbox.Done(sent[0])
			close(done)
		}()
		outbox.Put(hubapi.RunReport{RunID: "r", Seq: 2, Status: "succeeded"})
		<-done
		pending, _ := outbox.Pending()
		if len(pending) != 1 || pending[0].Seq != 2 {
			t.Fatalf("trial %d: pending = %+v", trial, pending)
		}
	}
}

func TestOutboxConcurrentPutsKeepTheHighestSeq(t *testing.T) {
	outbox := NewOutbox(t.TempDir())
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for seq := 1; seq <= 200; seq++ {
		wg.Add(1)
		go func(seq int64) {
			defer wg.Done()
			if err := outbox.Put(hubapi.RunReport{RunID: "r", Seq: seq}); err != nil {
				errs <- err
			}
		}(int64(seq))
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("put: %v", err)
	}
	pending, _ := outbox.Pending()
	if len(pending) != 1 || pending[0].Seq != 200 {
		t.Fatalf("pending = %+v", pending)
	}
}
