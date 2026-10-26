package trigger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chenhg5/agentflow/internal/store"
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

func TestOccurrencePolicyAndEventBindingsAreValidated(t *testing.T) {
	for _, invalid := range []string{"misfire", "event_path", "event_name", "inline_timezone"} {
		t.Run(invalid, func(t *testing.T) {
			root := t.TempDir()
			definition := Definition{ID: "daily", Enabled: true, Source: SourceConfig{Kind: "cron", Expression: "15 2 * * *", Timezone: "Asia/Shanghai"}, Target: TargetConfig{Workflow: "workflow.json"}, Policy: Policy{Misfire: "fire-once"}}
			switch invalid {
			case "misfire":
				definition.Policy.Misfire = "catch-up-all"
			case "event_path":
				definition.Bindings = map[string]Binding{"x": {From: "event", Path: "unknown"}}
			case "event_name":
				definition.Bindings = map[string]Binding{"x": {From: "event", Name: "scheduled_at"}}
			case "inline_timezone":
				definition.Source.Expression = "TZ=UTC 15 2 * * *"
			}
			if err := store.WriteAtomicJSON(filepath.Join(root, "triggers.json"), Config{Triggers: []Definition{definition}}); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(root); err == nil {
				t.Fatal("invalid occurrence config accepted")
			}
		})
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
