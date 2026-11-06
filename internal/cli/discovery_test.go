package cli

import (
	"github.com/chenhg5/agentflow/internal/model"
	"os"
	"path/filepath"
	"testing"
)

func TestSchemaDiscoveryInEmptyDirectory(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	output := os.Stdout
	sink, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = sink
	defer func() { os.Stdout = output; sink.Close() }()
	for _, name := range []string{"workflow", "executors", "trigger", "workspace"} {
		if err := Run([]string{"schema", name, "--format", "json"}); err != nil {
			t.Fatalf("%s schema unavailable without repository files: %v", name, err)
		}
	}
}

func TestRunPayloadHasArrayHistoryBeforeFirstSuccess(t *testing.T) {
	run := &model.Run{}
	payload := runPayload(run)
	history, ok := payload["history"].([]model.NodeVisit)
	if !ok || history == nil {
		t.Fatalf("history must be [] rather than null: %#v", payload["history"])
	}
}

func TestInvalidTriggerConfigurationIsNotRetryable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "triggers.json"), []byte(`{"triggers":[],"typo":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	err := triggerCommand([]string{"validate", "--format", "json"}, root, root)
	commandError, ok := err.(*CommandError)
	if !ok || commandError.Code != ExitUsageError || commandError.Retryable || commandError.Kind != "invalid_trigger_config" {
		t.Fatalf("configuration errors must tell the caller to fix input rather than retry: %#v", err)
	}
}
