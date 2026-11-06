package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowDecoderRejectsRemovedNodeExecutionFields(t *testing.T) {
	for name, node := range map[string]string{
		"executor": `{"id":"step","executor":"process","execution":{"candidates":[{"executor":"process"}]}}`,
		"config":   `{"id":"step","config":{},"execution":{"candidates":[{"executor":"process"}]}}`,
		"session":  `{"id":"step","execution":{"candidates":[{"executor":"process"}],"session":{"mode":"new"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.json")
			content := `{"version":"3","name":"old","entry":"step","nodes":[` + node + `],"edges":[{"from":"step","to":"$end"}]}`
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := readWorkflow(path, "json"); err == nil {
				t.Fatalf("removed %s field was accepted", name)
			}
		})
	}
}

func TestRunStartRejectsUnregisteredCandidateBeforeCreatingRun(t *testing.T) {
	root := t.TempDir()
	_, restore := chdirForTest(t, root)
	defer restore()

	workflow := `{
  "version": "3",
  "name": "invalid-candidate",
  "entry": "work",
  "nodes": [{
    "id": "work",
    "execution": {
      "candidates": [
        {"executor": "typo"},
        {"executor": "process", "config": {"command": ["sh", "-c", "printf '{\"ok\":true}' > \"$AGENTFLOW_RESULT_FILE\""]}}
      ],
      "fallback": {"when": "failure.kind == 'configuration_failed'"}
    }
  }],
  "edges": [{"from": "work", "to": "$end"}]
}`
	path := filepath.Join(root, "workflow.json")
	if err := os.WriteFile(path, []byte(workflow), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := captureRunOutput(t, []string{"run", "start", path, "--format", "json"})
	commandErr, ok := err.(*CommandError)
	if !ok || commandErr.Kind != "unregistered_executor" || !strings.Contains(commandErr.Message, `candidate 1 references unregistered executor "typo"`) {
		t.Fatalf("expected unregistered executor usage error, got %#v", err)
	}
	entries, readErr := os.ReadDir(filepath.Join(root, "runs"))
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid workflow created %d run(s)", len(entries))
	}
}
