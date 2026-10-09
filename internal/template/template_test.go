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
