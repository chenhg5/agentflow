package cli

import (
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestRunCancelRejectsCompletedRuns(t *testing.T) {
	for _, test := range []struct {
		status  string
		outcome string
	}{{status: "succeeded", outcome: "completed"}, {status: "failed", outcome: "error"}} {
		t.Run(test.status, func(t *testing.T) {
			root := t.TempDir()
			workflow := model.Workflow{Version: "3", Name: "cancel", Entry: "step", Nodes: []model.Node{{ID: "step", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "process"}}}}}}
			run, err := store.CreateRun(root, workflow, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			finished := time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano)
			run.State.Status = test.status
			run.State.Outcome = test.outcome
			run.State.FinishedAt = finished
			if err := store.SaveState(run); err != nil {
				t.Fatal(err)
			}
			err = runCancel(parsedArgs{Positionals: []string{run.RunID}, Format: "json"}, root, root)
			commandErr, ok := err.(*CommandError)
			if !ok || commandErr.Kind != "conflict" {
				t.Fatalf("late cancel error = %#v", err)
			}
			if err := store.ReloadState(run); err != nil {
				t.Fatal(err)
			}
			if run.State.Status != test.status || run.State.Outcome != test.outcome || run.State.FinishedAt != finished {
				t.Fatalf("CLI late cancel rewrote state: %+v", run.State)
			}
		})
	}
}
