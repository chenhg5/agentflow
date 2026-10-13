package template

import "testing"

func TestRenderAndEvaluateWhen(t *testing.T) {
	context := map[string]any{
		"inputs": map[string]string{"name": "Codex"},
		"nodes":  map[string]any{"review": map[string]any{"result": map[string]any{"decision": "PASS"}}},
	}
	if got := Render("hello {{ inputs.name }}", context); got != "hello Codex" {
		t.Fatalf("render = %q", got)
	}
	if !EvaluateWhen(`nodes.review.result.decision contains "PASS"`, context) {
		t.Fatal("expected contains expression to pass")
	}
}

func TestRenderValuePreservesExactPlaceholderType(t *testing.T) {
	context := map[string]any{"nodes": map[string]any{"build": map[string]any{"result": map[string]any{"count": float64(3), "ok": true}}}}
	value := RenderValue(map[string]any{
		"count": "{{ nodes.build.result.count }}",
		"ok":    "prefix={{ nodes.build.result.ok }}",
	}, context).(map[string]any)
	if count, ok := value["count"].(float64); !ok || count != 3 {
		t.Fatalf("exact placeholder lost number type: %#v", value["count"])
	}
	if value["ok"] != "prefix=true" {
		t.Fatalf("embedded placeholder was not rendered as text: %#v", value["ok"])
	}
}
