package trigger

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenhg5/agentflow/internal/model"
	"github.com/chenhg5/agentflow/internal/store"
)

func TestCronServicesAreIsolatedWithoutHTTPPorts(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	roots := []string{t.TempDir(), t.TempDir()}
	services := []chan error{}
	for _, root := range roots {
		config := `{"triggers":[{"id":"daily","enabled":true,"source":{"kind":"cron","expression":"0 0 1 1 *"},"target":{"workflow":"workflow.json"}}]}`
		if err := os.WriteFile(filepath.Join(root, "triggers.json"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		services = append(services, done)
		go func() { done <- Serve(ctx, root, root, ServeOptions{Host: "127.0.0.1", Port: port, Workers: 1}) }()
		waitForServiceLock(t, root, done)
	}
	if err := Serve(ctx, roots[0], roots[0], ServeOptions{}); !errors.Is(err, ErrServiceBusy) {
		t.Fatalf("duplicate service accepted: %v", err)
	}
	cancel()
	for _, done := range services {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("service failed to stop")
		}
	}
}

func waitForServiceLock(t *testing.T, root string, done <-chan error) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case err := <-done:
			t.Fatalf("service exited before ready: %v", err)
		case <-timeout.C:
			t.Fatal("service did not acquire its workspace lock")
		case <-ticker.C:
			release, err := acquireWorkspaceLock(context.Background(), root, "scheduler.lock", false)
			if errors.Is(err, errWorkspaceBusy) {
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			release()
		}
	}
}

func TestWebhookPortFailureDoesNotStartQueuedWork(t *testing.T) {
	root := t.TempDir()
	config := `{"triggers":[{"id":"hook","enabled":true,"source":{"kind":"webhook","path":"/hook"},"target":{"workflow":"workflow.json"}}]}`
	if err := os.WriteFile(filepath.Join(root, "triggers.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateRun(root, model.Workflow{Version: "3", Name: "queued", Entry: "wait", Nodes: []model.Node{{ID: "wait", Executor: "external"}}, Edges: []model.Transition{{From: "wait", To: "$end"}}}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	run.State.Status = "queued"
	if err := store.SaveState(run); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(root, root, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.saveJob(dispatchJob{RunID: run.RunID, Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := Serve(context.Background(), root, root, ServeOptions{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port}); err == nil {
		t.Fatal("occupied webhook port accepted")
	}
	loaded, err := store.LoadRun(root, run.RunID)
	if err != nil || loaded.State.Status != "queued" {
		t.Fatalf("failed service started queued work: %+v, %v", loaded, err)
	}
	release, err := acquireWorkspaceLock(context.Background(), root, "scheduler.lock", false)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestWorkspaceLockAcrossProcessesAndExit(t *testing.T) {
	root := t.TempDir()
	release, err := acquireWorkspaceLock(context.Background(), root, "scheduler.lock", false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	child := func() error {
		command := exec.Command(os.Args[0], "-test.run=^TestWorkspaceLockHelper$")
		command.Env = append(os.Environ(), "AGENTFLOW_LOCK_HELPER_ROOT="+root)
		return command.Run()
	}
	var exitError *exec.ExitError
	if err := child(); !errors.As(err, &exitError) || exitError.ExitCode() != 23 {
		t.Fatalf("another process acquired the lock: %v", err)
	}
	release()
	if err := child(); err != nil {
		t.Fatal(err)
	}
	// The child exits without cleanup; the OS must release its lock.
	again, err := acquireWorkspaceLock(context.Background(), root, "scheduler.lock", false)
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestWorkspaceLockHelper(t *testing.T) {
	root := os.Getenv("AGENTFLOW_LOCK_HELPER_ROOT")
	if root == "" {
		return
	}
	_, err := acquireWorkspaceLock(context.Background(), root, "scheduler.lock", false)
	if errors.Is(err, errWorkspaceBusy) {
		os.Exit(23)
	}
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
