package trigger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnknownTriggerFieldsAreNotSilentlyDiscarded(t *testing.T) {
	root := t.TempDir()
	data := `{"triggers":[{"id":"daily","enabled":true,"source":{"kind":"cron","expression":"0 9 * * *"},"target":{"workflow":"workflow.json","inputs":{"topic":"silently lost before this fix"}}}]}`
	if err := os.WriteFile(filepath.Join(root, "triggers.json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(root)
	if err == nil || !strings.Contains(err.Error(), `unknown field "inputs"`) {
		t.Fatalf("misplaced inputs must produce an actionable error, got %v", err)
	}
}

func TestWebhookRoutesMustBeUnambiguous(t *testing.T) {
	root := t.TempDir()
	data := `{"triggers":[{"id":"first","enabled":true,"source":{"kind":"webhook","path":"/hooks/shared"},"target":{"workflow":"first.json"}},{"id":"second","enabled":true,"source":{"kind":"webhook","path":"/hooks/shared/"},"target":{"workflow":"second.json"}}]}`
	if err := os.WriteFile(filepath.Join(root, "triggers.json"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(root)
	if err == nil || !strings.Contains(err.Error(), "share source.path") {
		t.Fatalf("duplicate webhook path accepted: %v", err)
	}
}
