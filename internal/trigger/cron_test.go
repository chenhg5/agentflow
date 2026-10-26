package trigger

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/engine"
	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func shanghai(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value+"+08:00")
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func cronFixture(t *testing.T, policy string) (string, *CronRunner, *Runtime, *time.Time) {
	t.Helper()
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	flow := model.Workflow{Version: "3", Name: "scheduled-normalization", Entry: "normalize", Nodes: []model.Node{{ID: "normalize", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "process", Config: map[string]any{"command": []string{executable, "-test.run=^TestCronNormalizationProcess$"}}}}}}}, Edges: []model.Transition{{From: "normalize", To: "$end"}}}
	if err := store.WriteAtomicJSON(filepath.Join(root, "workflow.json"), flow); err != nil {
		t.Fatal(err)
	}
	definition := Definition{ID: "daily", Enabled: true, Source: SourceConfig{Kind: "cron", Expression: "15 2 * * *", Timezone: "Asia/Shanghai"}, Target: TargetConfig{Workflow: "workflow.json"}, Policy: Policy{Misfire: policy}}
	definition.Bindings = map[string]Binding{}
	for _, name := range []string{"scheduled_at", "received_at", "trigger_id", "delivery_id", "schedule_timezone", "catch_up"} {
		definition.Bindings[name] = Binding{From: "event", Path: name}
	}
	config := Config{Triggers: []Definition{definition}}
	if err := store.WriteAtomicJSON(filepath.Join(root, "triggers.json"), config); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(root, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	at := shanghai(t, "2026-10-24T03:00:00")
	runner := &CronRunner{Dispatcher: &Dispatcher{Root: root, Config: config, Starter: runtime}, Now: func() time.Time { return at }}
	t.Cleanup(func() {
		if destination := os.Getenv("AF07_EVIDENCE_DIR"); destination != "" {
			cursors, _ := CronStatus(root)
			receipts, _ := DeliveryStatus(root)
			ids, _ := store.RunIDs(root)
			runs := []*model.Run{}
			for _, id := range ids {
				if run, err := store.LoadRun(root, id); err == nil {
					runs = append(runs, run)
				}
			}
			if err := store.WriteAtomicJSON(filepath.Join(destination, strings.ReplaceAll(t.Name(), "/", "_")+".json"), map[string]any{"clock": at, "cursors": cursors, "deliveries": receipts, "runs": runs, "real_provider_verified": false}); err != nil {
				t.Error(err)
			}
		}
	})
	return root, runner, runtime, &at
}

// This deterministic standalone program owns business date normalization. The
// scheduler never subtracts days or knows a report_date field.
func TestCronNormalizationProcess(t *testing.T) {
	result := os.Getenv("AGENTFLOW_RESULT_FILE")
	if result == "" {
		return
	}
	var inputs map[string]string
	if err := store.ReadAtomicJSON(os.Getenv("AGENTFLOW_INPUT_FILE"), &inputs); err != nil {
		t.Fatal(err)
	}
	planned, err := time.Parse(time.RFC3339Nano, inputs["scheduled_at"])
	if err != nil {
		t.Fatal(err)
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteAtomicJSON(result, map[string]string{"report_date": planned.In(location).AddDate(0, 0, -1).Format("2006-01-02")}); err != nil {
		t.Fatal(err)
	}
}

func TestCronStableContextAndDelayedNormalization(t *testing.T) {
	root, runner, runtime, at := cronFixture(t, "fire-once")
	ctx := context.Background()
	*at = shanghai(t, "2026-10-26T02:14:00")
	if err := runner.Tick(ctx, true); err != nil {
		t.Fatal(err)
	}
	*at = shanghai(t, "2026-10-26T02:15:00")
	if err := runner.Tick(ctx, false); err != nil {
		t.Fatal(err)
	}
	ids, _ := store.RunIDs(root)
	if len(ids) != 1 {
		t.Fatal("normal occurrence count", ids)
	}
	run, err := store.LoadRun(root, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	want := "2026-10-25T18:15:00Z"
	if run.State.Inputs["scheduled_at"] != want || run.State.Inputs["schedule_timezone"] != "Asia/Shanghai" || run.State.Inputs["catch_up"] != "false" {
		t.Fatal(run.State.Inputs)
	}
	// Re-delivery on the next day retains the original received time and run.
	planned := *at
	*at = shanghai(t, "2026-10-27T12:00:00")
	duplicate, err := runner.Dispatcher.Dispatch(ctx, Event{TriggerID: "daily", SourceKind: "cron", ScheduledAt: &planned, ReceivedAt: *at})
	if err != nil || !duplicate.Duplicate || duplicate.Run.RunID != run.RunID {
		t.Fatal(duplicate, err)
	}
	if err := engine.Execute(run, runtime.Executors); err != nil {
		t.Fatal(err)
	}
	result, _ := run.State.Nodes["normalize"].Result.(map[string]any)
	if run.State.Status != "succeeded" || result["report_date"] != "2026-10-25" {
		t.Fatal(run.State, result)
	}
	loaded, _ := store.LoadRun(root, run.RunID)
	if loaded.State.Inputs["received_at"] != want {
		t.Fatal("receipt time changed", loaded.State.Inputs)
	}
}

func TestCronFireOnceSkipAndForbid(t *testing.T) {
	for _, policy := range []string{"skip", "fire-once"} {
		t.Run(policy, func(t *testing.T) {
			root, runner, _, at := cronFixture(t, policy)
			if err := runner.Tick(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			*at = shanghai(t, "2026-10-26T03:00:00")
			if err := runner.Tick(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			ids, _ := store.RunIDs(root)
			cursors, _ := CronStatus(root)
			count := 2
			if policy == "fire-once" {
				count = 1
				if len(ids) != 1 {
					t.Fatal("catch-up must create latest only", ids)
				}
				run, _ := store.LoadRun(root, ids[0])
				if run.State.Inputs["scheduled_at"] != "2026-10-25T18:15:00Z" || run.State.Inputs["catch_up"] != "true" {
					t.Fatal(run.State.Inputs)
				}
				// Existing queued run blocks the next day's catch-up, visibly.
				*at = shanghai(t, "2026-10-27T03:00:00")
				if err := runner.Tick(context.Background(), true); err != nil {
					t.Fatal(err)
				}
				receipts, _ := DeliveryStatus(root)
				found := false
				for _, receipt := range receipts {
					if receipt.Status == "skipped" && receipt.Reason == "active_run" {
						found = true
					}
				}
				if !found {
					t.Fatal("forbid decision missing", receipts)
				}
			} else if len(ids) != 0 {
				t.Fatal("skip replayed downtime", ids)
			}
			if len(cursors[0].Skipped) != 1 || cursors[0].Skipped[0].Count != count {
				t.Fatal(cursors)
			}
		})
	}
}

func TestCronFirstEnableEditAndReenableBoundaries(t *testing.T) {
	root, runner, _, at := cronFixture(t, "fire-once")
	if err := runner.Tick(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	*at = shanghai(t, "2026-10-26T03:00:00")
	runner.Dispatcher.Config.Triggers[0].Source.Expression = "30 3 * * *"
	if err := runner.Tick(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	ids, _ := store.RunIDs(root)
	if len(ids) != 0 {
		t.Fatal("schedule edit flooded history")
	}
	cursors, _ := CronStatus(root)
	if len(cursors[0].Baselines) != 2 || cursors[0].Baselines[1].Reason != "definition_changed" {
		t.Fatal(cursors)
	}
	runner.Dispatcher.Config.Triggers[0].Enabled = false
	if err := runner.Tick(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	*at = shanghai(t, "2026-10-28T04:00:00")
	runner.Dispatcher.Config.Triggers[0].Enabled = true
	if err := runner.Tick(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	ids, _ = store.RunIDs(root)
	if len(ids) != 0 {
		t.Fatal("reenable flooded disabled period")
	}
	cursors, _ = CronStatus(root)
	if cursors[0].Baselines[3].Reason != "reenabled" {
		t.Fatal(cursors)
	}
}

func TestCronCrashChild(t *testing.T) {
	root := os.Getenv("AF07_CRASH_ROOT")
	if root == "" {
		return
	}
	config, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(root, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	triggerCheckpoint = func(stage string) {
		if stage == os.Getenv("AF07_CRASH_STAGE") {
			os.Exit(94)
		}
	}
	runner := &CronRunner{Dispatcher: &Dispatcher{Root: root, Config: config, Starter: runtime}, Now: func() time.Time { return shanghai(t, "2026-10-26T03:00:00") }}
	if err := runner.Tick(context.Background(), true); err != nil {
		t.Fatal(err)
	}
}

func TestCronProcessInterruptionRecoversSameOccurrence(t *testing.T) {
	for _, stage := range []string{"plan_persisted", "receipt_pending", "run_created", "receipt_saved", "cursor_advanced"} {
		t.Run(stage, func(t *testing.T) {
			root, runner, _, at := cronFixture(t, "fire-once")
			if err := runner.Tick(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			executable, _ := os.Executable()
			child := exec.Command(executable, "-test.run=^TestCronCrashChild$")
			child.Env = append(os.Environ(), "AF07_CRASH_ROOT="+root, "AF07_CRASH_STAGE="+stage)
			output, err := child.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 94 {
				t.Fatalf("child did not interrupt at %s: %v %s", stage, err, output)
			}
			if stage == "receipt_pending" || stage == "run_created" || stage == "receipt_saved" {
				var changed model.Workflow
				_ = store.ReadAtomicJSON(filepath.Join(root, "workflow.json"), &changed)
				changed.Name = "edited-while-creation-uncertain"
				if err := store.WriteAtomicJSON(filepath.Join(root, "workflow.json"), changed); err != nil {
					t.Fatal(err)
				}
			}
			*at = shanghai(t, "2026-10-26T03:00:00")
			if err := runner.Tick(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if err := runner.Tick(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			ids, _ := store.RunIDs(root)
			if len(ids) != 1 {
				t.Fatal("lost/duplicate run", ids)
			}
			run, _ := store.LoadRun(root, ids[0])
			if run.Workflow.Name != "scheduled-normalization" {
				t.Fatal("prepared snapshot changed", run.Workflow.Name)
			}
			receipts, _ := DeliveryStatus(root)
			if len(receipts) != 1 || receipts[0].Status != "queued" {
				t.Fatal("forbid blocked own recovery", receipts)
			}
			cursors, _ := CronStatus(root)
			if cursors[0].Pending != nil || !cursors[0].Through.Equal(*at) {
				t.Fatal("watermark did not finish", cursors)
			}
		})
	}
}

func TestEventFactsCannotBeOverriddenByAttributes(t *testing.T) {
	planned := shanghai(t, "2026-10-26T02:15:00")
	definition := Definition{Bindings: map[string]Binding{"time": {From: "event", Path: "scheduled_at"}}}
	inputs, err := ResolveInputs(definition, Event{ScheduledAt: &planned, Attributes: map[string]string{"scheduled_at": "evil"}})
	if err != nil || inputs["time"] != "2026-10-25T18:15:00Z" {
		t.Fatal(inputs, err)
	}
}

func TestConcurrentCronRunnersPublishOneOccurrence(t *testing.T) {
	root, runner, _, at := cronFixture(t, "fire-once")
	if err := runner.Tick(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	*at = shanghai(t, "2026-10-26T03:00:00")
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- runner.Tick(context.Background(), true) }()
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	ids, err := store.RunIDs(root)
	if err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
}

func TestPersistedCronPlanFinishesBeforeDefinitionEditBoundary(t *testing.T) {
	root, runner, _, at := cronFixture(t, "fire-once")
	if err := runner.Tick(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	child := exec.Command(executable, "-test.run=^TestCronCrashChild$")
	child.Env = append(os.Environ(), "AF07_CRASH_ROOT="+root, "AF07_CRASH_STAGE=plan_persisted")
	output, err := child.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 94 {
		t.Fatalf("failed to interrupt plan: %v %s", err, output)
	}
	runner.Dispatcher.Config.Triggers[0].Source.Expression = "0 4 * * *"
	*at = shanghai(t, "2026-10-26T03:00:00")
	if err := runner.Tick(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	ids, _ := store.RunIDs(root)
	if len(ids) != 1 {
		t.Fatal("persisted plan was dropped", ids)
	}
	run, _ := store.LoadRun(root, ids[0])
	if run.State.Inputs["scheduled_at"] != "2026-10-25T18:15:00Z" {
		t.Fatal(run.State.Inputs)
	}
	cursors, _ := CronStatus(root)
	if len(cursors[0].Baselines) != 2 || cursors[0].Baselines[1].Reason != "definition_changed" {
		t.Fatal(cursors)
	}
}
