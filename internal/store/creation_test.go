package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
)

func creationFixture() CreateRequest {
	return CreateRequest{Workflow: model.Workflow{Version: "3", Name: "creation", Entry: "wait", Nodes: []model.Node{{ID: "wait", Execution: model.Execution{Candidates: []model.ExecutorCandidate{{Executor: "external"}}}}}, Edges: []model.Transition{{From: "wait", To: "$end"}}}, Inputs: map[string]string{"value": "original"}, IdempotencyKey: "request", Queued: true}
}

func TestCreationCrashProcess(t *testing.T) {
	if os.Getenv("AF_CREATION_CRASH_TEST") != "1" {
		return
	}
	creationCheckpoint = func(stage string) {
		if stage == os.Getenv("AF_CREATION_CRASH_STAGE") {
			os.Exit(87)
		}
	}
	_, err := CreateOrGetRun(context.Background(), os.Getenv("AF_CREATION_TEST_ROOT"), creationFixture())
	if err != nil {
		os.Exit(88)
	}
	os.Exit(89)
}

func TestCreationRecoveryAtCommitBoundaries(t *testing.T) {
	for _, stage := range []string{"reserved", "snapshot", "published", "queued"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "-test.run=^TestCreationCrashProcess$")
			cmd.Env = append(os.Environ(), "AF_CREATION_CRASH_TEST=1", "AF_CREATION_CRASH_STAGE="+stage, "AF_CREATION_TEST_ROOT="+root)
			if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 87 {
				t.Fatalf("checkpoint %s did not interrupt: %v", stage, err)
			}
			paths, _ := filepath.Glob(filepath.Join(root, "runs", "requests", "*.json"))
			if len(paths) != 1 {
				t.Fatalf("expected one request journal, got %d", len(paths))
			}
			var reserved creationRecord
			if err := readJSON(paths[0], &reserved); err != nil {
				t.Fatal(err)
			}
			changed := creationFixture()
			changed.Inputs["value"] = "changed"
			if _, err := CreateOrGetRun(context.Background(), root, changed); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("changed interrupted request: %v", err)
			}
			if err := RecoverRunCreations(context.Background(), root); err != nil {
				t.Fatal(err)
			}
			result, err := CreateOrGetRun(context.Background(), root, creationFixture())
			if err != nil {
				t.Fatal(err)
			}
			ids, err := RunIDs(root)
			if err != nil || len(ids) != 1 || result.Run.RunID != reserved.RunID || result.Created {
				t.Fatalf("request did not recover same run: ids=%v err=%v", ids, err)
			}
			if result.Run.State.Status != "queued" || result.Run.State.Inputs["value"] != "original" {
				t.Fatal("recovery changed the initial request")
			}
			var job DispatchJob
			if err := readJSON(filepath.Join(root, "dispatch", reserved.RunID+".json"), &job); err != nil || job.RunID != reserved.RunID {
				t.Fatalf("execution outbox missing: %v", err)
			}
			events, err := ReadEvents(result.Run.RunDir)
			if err != nil || strings.Count(string(events), `"type":"run.created"`) != 1 || strings.Count(string(events), `"type":"run.queued"`) != 1 {
				t.Fatalf("creation events not unique: %v", err)
			}
		})
	}
}

func TestCreationRejectsChangedGraphAndMissingCommittedRun(t *testing.T) {
	root := t.TempDir()
	request := creationFixture()
	first, err := CreateOrGetRun(context.Background(), root, request)
	if err != nil {
		t.Fatal(err)
	}
	request.Workflow.Nodes[0].TimeoutMs = 123
	if _, err := CreateOrGetRun(context.Background(), root, request); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed graph did not conflict: %v", err)
	}
	if err := os.RemoveAll(first.Run.RunDir); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateOrGetRun(context.Background(), root, creationFixture()); err == nil || !strings.Contains(err.Error(), "refusing to recreate") {
		t.Fatalf("committed run deletion replayed request: %v", err)
	}
}

func TestCreationJournalUsesCompleteFingerprint(t *testing.T) {
	r := creationFixture()
	digest, err := creationDigest(r)
	if err != nil || len(digest) != 64 {
		t.Fatalf("invalid fingerprint: %v", err)
	}
	data, _ := json.Marshal(r.Workflow)
	var equivalent model.Workflow
	if err := json.Unmarshal(data, &equivalent); err != nil {
		t.Fatal(err)
	}
	r.Workflow = equivalent
	other, err := creationDigest(r)
	if err != nil || other != digest {
		t.Fatal("JSON-equivalent requests did not match")
	}
}
