package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const labelledWorkflow = `{"version":"3","name":"pr-daily","title":"每日 PR 汇总","description":"每天把合进 main 的 PR 整理成一段话",
	"tags":["github","daily"],"group":"cc-connect","entry":"note",
	"nodes":[{"id":"note","execution":{"candidates":[{"executor":"process","config":{"command":["sh","-c","echo '{}' > \"$AGENTFLOW_RESULT_FILE\""]}}]}}],
	"edges":[{"from":"note","to":"$end"}]}`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func decodeOutput(t *testing.T, args ...string) map[string]any {
	data, err := captureRunOutput(t, args)
	t.Helper()
	if err != nil {
		t.Fatalf("command failed: %v (%s)", err, data)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("output %q: %v", data, err)
	}
	return out
}

func TestWorkflowListAndRunLabels(t *testing.T) {
	root := t.TempDir()
	_, restore := chdirForTest(t, root)
	defer restore()
	writeFile(t, filepath.Join(root, "workflow.json"), labelledWorkflow)
	writeFile(t, filepath.Join(root, "workflows", "plain.json"), strings.NewReplacer(`"pr-daily"`, `"plain"`, `"title":"每日 PR 汇总",`, "", `"tags":["github","daily"],"group":"cc-connect",`, "").Replace(labelledWorkflow))
	writeFile(t, filepath.Join(root, "workflows", "broken.json"), `{"version":"3","name":"broken","tags":["x","x"],"nodes":[]}`)

	list := decodeOutput(t, "workflow", "list", "--format", "json")
	items := list["workflows"].([]any)
	if len(items) != 3 {
		t.Fatalf("workflows = %v", items)
	}
	first := items[0].(map[string]any)
	if first["file"] != "workflow.json" || first["title"] != "每日 PR 汇总" || first["group"] != "cc-connect" || first["description"] == "" || len(first["tags"].([]any)) != 2 {
		t.Fatalf("labelled workflow = %v", first)
	}
	byFile := map[string]map[string]any{}
	for _, item := range items {
		entry := item.(map[string]any)
		byFile[entry["file"].(string)] = entry
	}
	if plain := byFile["workflows/plain.json"]; plain["title"] != "plain" || plain["valid"] != true {
		t.Fatalf("a workflow without a title shows its name: %v", plain)
	}
	if broken := byFile["workflows/broken.json"]; broken["valid"] != false || broken["error"] == "" {
		t.Fatalf("an invalid file is listed with its problem: %v", broken)
	}
	tagged := decodeOutput(t, "workflow", "list", "--tag", "github", "--tag", "daily", "--format", "json")
	if items := tagged["workflows"].([]any); len(items) != 1 || items[0].(map[string]any)["name"] != "pr-daily" {
		t.Fatalf("--tag = %v", items)
	}
	if out := decodeOutput(t, "workflow", "list", "--group", "nope", "--format", "json"); len(out["workflows"].([]any)) != 0 {
		t.Fatal("--group filters")
	}
	valid := decodeOutput(t, "workflow", "validate", "workflow.json", "--format", "json")
	if valid["title"] != "每日 PR 汇总" || valid["group"] != "cc-connect" {
		t.Fatalf("validate = %v", valid)
	}

	for _, file := range []string{"workflow.json", "workflows/plain.json"} {
		if _, err := captureRunOutput(t, []string{"run", "start", file, "--format", "json"}); err != nil {
			t.Fatalf("run %s: %v", file, err)
		}
	}
	runs := decodeOutput(t, "run", "list", "--tag", "github", "--format", "json")["runs"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["workflow"] != "pr-daily" || runs[0].(map[string]any)["title"] != "每日 PR 汇总" {
		t.Fatalf("run list --tag = %v", runs)
	}
	if runs := decodeOutput(t, "run", "list", "--group", "cc-connect", "--format", "json")["runs"].([]any); len(runs) != 1 {
		t.Fatalf("run list --group = %v", runs)
	}
	if runs := decodeOutput(t, "run", "list", "--format", "json")["runs"].([]any); len(runs) != 2 {
		t.Fatalf("run list = %v", runs)
	}
}

func TestCodeRunLabels(t *testing.T) {
	root := t.TempDir()
	_, restore := chdirForTest(t, root)
	defer restore()
	started := decodeOutput(t, "code", "start", "nightly", "--title", "夜间巡检", "--tag", "ops", "--group", "infra", "--description", "每晚检查", "--format", "json")
	runs := decodeOutput(t, "run", "list", "--tag", "ops", "--format", "json")["runs"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["run_id"] != started["run_id"] || runs[0].(map[string]any)["title"] != "夜间巡检" || runs[0].(map[string]any)["group"] != "infra" {
		t.Fatalf("code run labels = %v", runs)
	}
	var commandErr *CommandError
	if _, err := captureRunOutput(t, []string{"code", "start", "x", "--tag", "a", "--tag", "a"}); !errors.As(err, &commandErr) || commandErr.Kind != "invalid_labels" {
		t.Fatalf("repeated tag: %v", err)
	}
}

func TestHubWorkflowsByTag(t *testing.T) {
	hubFixture(t)
	_, restore := chdirForTest(t, t.TempDir())
	defer restore()
	all, err := hubJSON(t, "hub", "workflows")
	if err != nil || len(all["workflows"].([]any)) != 1 {
		t.Fatalf("workflows = %v, %v", all, err)
	}
	none, err := hubJSON(t, "hub", "workflows", "--tag", "nope")
	if err != nil || len(none["workflows"].([]any)) != 0 {
		t.Fatalf("--tag filters on the Hub: %v, %v", none, err)
	}
	runs, err := hubJSON(t, "hub", "runs", "--group", "nope")
	if err != nil || runs["total"] != 0.0 {
		t.Fatalf("--group filters runs on the Hub: %v, %v", runs, err)
	}
}
