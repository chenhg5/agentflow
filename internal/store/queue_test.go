package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
)

func queueFlow() model.Workflow {
	return model.Workflow{Version: "3", Name: "queue", Entry: "work", Nodes: []model.Node{{ID: "work"}}, Edges: []model.Transition{{From: "work", To: "$end"}}}
}
func queued(t *testing.T, root string) *model.Run {
	t.Helper()
	result, err := CreateOrGetRun(context.Background(), root, CreateRequest{Workflow: queueFlow(), Queued: true})
	if err != nil {
		t.Fatal(err)
	}
	return result.Run
}

func TestDispatchWakeSurvivesFinishingClaim(t *testing.T) {
	root := t.TempDir()
	run := queued(t, root)
	ctx := context.Background()
	claim, err := ClaimDispatch(ctx, root, "owner", nil, "")
	if err != nil || claim == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := WakeRun(ctx, root, run.RunID, "submit"); err != nil {
		t.Fatal(err)
	}
	if err := FinishDispatch(ctx, root, *claim, "waiting_input", ""); err != nil {
		t.Fatal(err)
	}
	job, _ := LoadDispatch(root, run.RunID)
	if job.Generation != 2 || job.Completed != 1 || job.Status != "queued" {
		t.Fatalf("wake lost: %+v", job)
	}
	next, err := ClaimDispatch(ctx, root, "owner", nil, "")
	if err != nil || next == nil || next.Claimed != 2 {
		t.Fatalf("next claim: %+v %v", next, err)
	}
	if err := FinishDispatch(ctx, root, *next, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	next, err = ClaimDispatch(ctx, root, "owner", nil, "")
	if err != nil || next != nil {
		t.Fatal("acknowledged job replayed", err)
	}
}

func TestControlPublishedBeforeWakeIsRecovered(t *testing.T) {
	root := t.TempDir()
	run := queued(t, root)
	ctx := context.Background()
	claim, _ := ClaimDispatch(ctx, root, "owner", nil, "")
	if err := FinishDispatch(ctx, root, *claim, "waiting_input", ""); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueControl(run.RunDir, model.Control{Kind: "cancel"}); err != nil {
		t.Fatal(err)
	}
	// Simulates death after inbox fsync, before wake fsync. Even a live service
	// must notice this; it cannot depend on a restart or an in-memory notification.
	claim, err := ClaimDispatch(ctx, root, "new-owner", nil, "")
	if err != nil || claim == nil || claim.Claimed != 2 || claim.Reason != "recovered_control" {
		t.Fatalf("lost control: %+v %v", claim, err)
	}
}

func TestControlWithoutDispatchProjectionIsRecovered(t *testing.T) {
	root := t.TempDir()
	run, err := CreateRun(root, queueFlow(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := EnqueueControl(run.RunDir, model.Control{Kind: "cancel"}); err != nil {
		t.Fatal(err)
	}
	claim, err := ClaimDispatch(context.Background(), root, "owner", nil, "")
	if err != nil || claim == nil || claim.RunID != run.RunID {
		t.Fatalf("control has no wake: %+v %v", claim, err)
	}
}

func TestDispatchOwnerRecoveryAndExclusiveLock(t *testing.T) {
	root := t.TempDir()
	run := queued(t, root)
	ctx := context.Background()
	release, err := AcquireWorkspaceLock(ctx, root, "worker.lock", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireWorkspaceLock(ctx, root, "worker.lock", false); !errors.Is(err, ErrWorkspaceBusy) {
		t.Fatal("duplicate owner", err)
	}
	old, _ := ClaimDispatch(ctx, root, "dead", nil, "")
	release()
	fresh, err := ClaimDispatch(ctx, root, "new", nil, "")
	if err != nil || fresh == nil || fresh.RunID != run.RunID || fresh.Reason != "recovered_worker" {
		t.Fatalf("recovery: %+v %v", fresh, err)
	}
	if err := FinishDispatch(ctx, root, *old, "succeeded", ""); err == nil {
		t.Fatal("stale owner overwrote new claim")
	}
}

func TestParentIdentityAndQueries(t *testing.T) {
	root := t.TempDir()
	parent := queued(t, root)
	ctx := context.Background()
	request := CreateRequest{Workflow: queueFlow(), Queued: true, Inputs: map[string]string{"n": "1"}, IdempotencyKey: "child", ParentRunID: parent.RunID, ParentNodeID: "work", Correlation: map[string]string{"any-label": "any-value"}}
	first, err := CreateOrGetRun(ctx, root, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := CreateOrGetRun(ctx, root, request)
	if err != nil || replay.Run.RunID != first.Run.RunID {
		t.Fatal("replay", err)
	}
	other := queued(t, root)
	for _, mutate := range []func(*CreateRequest){func(r *CreateRequest) { r.ParentRunID = other.RunID }, func(r *CreateRequest) { r.ParentNodeID = "" }, func(r *CreateRequest) { r.Correlation = map[string]string{"any-label": "different"} }} {
		changed := request
		mutate(&changed)
		if _, err := CreateOrGetRun(ctx, root, changed); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatal("relationship identity not enforced", err)
		}
	}
	for _, bad := range []CreateRequest{{ParentNodeID: "work"}, {ParentRunID: "../outside"}, {ParentRunID: "run_missing"}, {ParentRunID: parent.RunID, ParentNodeID: "missing"}} {
		bad.Workflow = queueFlow()
		if _, err := CreateOrGetRun(ctx, root, bad); err == nil {
			t.Fatal("invalid parent accepted", bad)
		}
	}
	children, counts, err := Children(root, parent.RunID)
	if err != nil || len(children) != 1 || counts["queued"] != 1 {
		t.Fatalf("children: %d %+v %v", len(children), counts, err)
	}
	// A malformed persisted ancestor must fail rather than hang on a cycle.
	parent.State.ParentRunID = parent.RunID
	if err := SaveState(parent); err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey = "other"
	if _, err := CreateOrGetRun(ctx, root, request); err == nil {
		t.Fatal("cyclic ancestry accepted")
	}
}

func TestManagedQueueFailsClosedOnInvalidConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "queue.json"), []byte(`{"version":1,"concurrency":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := QueueManaged(root); err == nil {
		t.Fatal("invalid config silently disabled admission")
	}
}

func TestNewOwnerDoesNotCountOldClaimsAsActive(t *testing.T) {
	root := t.TempDir()
	queued(t, root)
	ctx := context.Background()
	if _, err := ClaimDispatch(ctx, root, "old", nil, ""); err != nil {
		t.Fatal(err)
	}
	release, err := AcquireWorkspaceLock(ctx, root, "worker.lock", false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := SaveWorkerInfo(root, WorkerInfo{Owner: "new", Concurrency: 1}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := QueueSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	counts := snapshot["counts"].(map[string]int)
	if counts["active"] != 0 || counts["queued"] != 1 {
		t.Fatalf("orphan counted as new owner capacity: %+v", counts)
	}
}
