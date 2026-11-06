package store

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
)

func resourceRoot(t *testing.T) (string, *model.Run) {
	t.Helper()
	root := t.TempDir()
	if err := writeCreationJSON(filepath.Join(root, "resources.json"), ResourceConfig{Resources: map[string]ResourceDefinition{"A": {Capacity: 1}, "B": {Capacity: 1}, "pool": {Capacity: 2}}}); err != nil {
		t.Fatal(err)
	}
	run, err := CreateRun(root, queueFlow(), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return root, run
}
func resourceRequest(run *model.Run, keys ...string) ResourceClaim {
	claim := ResourceClaim{RunID: run.RunID, NodeID: "work", Attempt: 1, Token: NewToken()}
	for _, key := range keys {
		claim.Requests = append(claim.Requests, model.ResourceRequest{Key: key, Units: 1})
	}
	return claim
}
func TestResourceAllOrNoneAndValidation(t *testing.T) {
	root, run := resourceRoot(t)
	ctx := context.Background()
	a, err := AcquireResources(ctx, root, resourceRequest(run, "A", "B"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireResources(ctx, root, resourceRequest(run, "B", "A")); !errors.Is(err, ErrResourceUnavailable) {
		t.Fatal(err)
	}
	status, err := ResourceStatus(ctx, root)
	if err != nil || len(status.Claims) != 1 || status.Used["A"] != 1 || status.Used["B"] != 1 {
		t.Fatalf("partial or duplicated allocation: %+v %v", status, err)
	}
	if err := ResolveResource(ctx, root, a.Claim.ID, "attempt still running", true); err == nil {
		t.Fatal("live reservation was released")
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	b, err := AcquireResources(ctx, root, resourceRequest(run, "B", "A"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Release(); err != nil {
		t.Fatal(err)
	}
	for _, requests := range [][]model.ResourceRequest{{{Key: "unknown", Units: 1}}, {{Key: "A", Units: 0}}, {{Key: "A", Units: 2}}, {{Key: "A", Units: 1}, {Key: "A", Units: 1}}} {
		claim := resourceRequest(run)
		claim.Requests = requests
		if _, err := AcquireResources(ctx, root, claim); err == nil {
			t.Fatal("invalid request admitted", requests)
		}
	}
}
func TestResourceHelperProcess(t *testing.T) {
	if os.Getenv("AF05_RESOURCE_HELPER") != "1" {
		return
	}
	for {
		time.Sleep(50 * time.Millisecond)
	}
}
func TestLostResourceOwnerIsQuarantinedUntilExplicitCleanup(t *testing.T) {
	root, run := resourceRoot(t)
	ctx := context.Background()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(binary, "-test.run=^TestResourceHelperProcess$")
	child.Env = append(os.Environ(), "AF05_RESOURCE_HELPER=1")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	owner, err := AcquireResources(ctx, root, resourceRequest(run, "A"))
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.Started(child.Process.Pid); err != nil {
		t.Fatal(err)
	}
	// Releasing only the OS owner lock simulates the exact state after owner
	// death, while its real child process remains alive.
	owner.releaseOwner()
	owner.releaseOwner = nil
	snapshot, err := ResourceStatus(ctx, root)
	if err != nil || snapshot.Claims[0].Status != "orphaned" {
		t.Fatalf("missing quarantine: %+v %v", snapshot, err)
	}
	if _, err := AcquireResources(ctx, root, resourceRequest(run, "A")); !errors.Is(err, ErrResourceUnavailable) {
		t.Fatal("orphan quota stolen", err)
	}
	if err := ResolveResource(ctx, root, owner.Claim.ID, "not stopped", true); err == nil {
		t.Fatal("live child was ignored")
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	if err := ResolveResource(ctx, root, owner.Claim.ID, "synthetic child terminated and waited", true); err != nil {
		t.Fatal(err)
	}
	next, err := AcquireResources(ctx, root, resourceRequest(run, "A"))
	if err != nil {
		t.Fatal(err)
	}
	if err := next.Release(); err != nil {
		t.Fatal(err)
	}
}
func TestResourceConfigRejectsTrailingAndUnknownFields(t *testing.T) {
	for _, data := range []string{`{"resources":{}} {}`, `{"resources":{"A":{"capacity":1,"unknown":true}}}`, `{"resources":{"A":{"capacity":0}}}`, `{"resources":null}`} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "resources.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadResources(root); err == nil {
			t.Fatal("invalid config accepted", data)
		}
	}
}

func TestResourceAllocatorChild(t *testing.T) {
	root := os.Getenv("AF05_ALLOCATOR_ROOT")
	if root == "" {
		return
	}
	claim, err := AcquireResources(context.Background(), root, ResourceClaim{RunID: os.Getenv("AF05_ALLOCATOR_RUN"), NodeID: "child", Attempt: 1, Token: NewToken(), Requests: []model.ResourceRequest{{Key: "pool", Units: 1}}})
	if errors.Is(err, ErrResourceUnavailable) {
		os.Exit(42)
	}
	if err != nil {
		os.Exit(90)
	}
	if err := writeCreationJSON(filepath.Join(root, "admitted", claim.Claim.ID+".json"), claim.Claim); err != nil {
		os.Exit(91)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "allocator-release")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := claim.Release(); err != nil {
		os.Exit(92)
	}
	os.Exit(0)
}

func TestResourceAllocationAcrossIndependentProcesses(t *testing.T) {
	root, run := resourceRoot(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	codes := make(chan int, 10)
	children := []*exec.Cmd{}
	defer func() {
		_ = os.WriteFile(filepath.Join(root, "allocator-release"), nil, 0600)
		for _, child := range children {
			_ = child.Process.Kill()
		}
	}()
	for i := 0; i < 10; i++ {
		child := exec.Command(binary, "-test.run=^TestResourceAllocatorChild$")
		child.Env = append(os.Environ(), "AF05_ALLOCATOR_ROOT="+root, "AF05_ALLOCATOR_RUN="+run.RunID)
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		children = append(children, child)
		go func() {
			err := child.Wait()
			if err == nil {
				codes <- 0
				return
			}
			if exit, ok := err.(*exec.ExitError); ok {
				codes <- exit.ExitCode()
				return
			}
			codes <- 99
		}()
	}
	rejected := 0
	deadline := time.After(10 * time.Second)
	for rejected < 8 {
		select {
		case code := <-codes:
			if code != 42 {
				t.Fatal("unexpected allocator result", code)
			}
			rejected++
		case <-deadline:
			t.Fatal("process allocation did not settle")
		}
	}
	// A rejected process may exit before an admitted peer writes its marker.
	// Wait for both holders instead of assuming an order across processes.
	for {
		paths, err := filepath.Glob(filepath.Join(root, "admitted", "*.json"))
		if err != nil || len(paths) > 2 {
			t.Fatal("wrong admitted process count", len(paths), err)
		}
		if len(paths) == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("admitted processes did not publish their markers")
		case <-time.After(10 * time.Millisecond):
		}
	}
	status, err := ResourceStatus(context.Background(), root)
	if err != nil || status.Used["pool"] != 2 {
		t.Fatal("quota exceeded or underallocated", status, err)
	}
	if err := os.WriteFile(filepath.Join(root, "allocator-release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case code := <-codes:
			if code != 0 {
				t.Fatal(code)
			}
		case <-deadline:
			t.Fatal("allocator did not release")
		}
	}
}
