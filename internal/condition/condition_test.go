package condition

import "testing"

func TestGeneralConditions(t *testing.T) {
	env := map[string]any{"nodes": map[string]any{"review": map[string]any{"result": map[string]any{"score": float64(8), "decision": "happy", "tags": []any{"ready", "safe"}}}}}
	for _, expression := range []string{
		"nodes.review.result.score > 7",
		"nodes.review.result.score >= 8 && nodes.review.result.decision == 'happy'",
		"'safe' in nodes.review.result.tags",
		"nodes.review.result.decision matches '^h.*y$'",
	} {
		matched, err := Evaluate(expression, env)
		if err != nil || !matched {
			t.Fatalf("%s: matched=%v err=%v", expression, matched, err)
		}
	}
	if err := Validate("nodes.review.result.score >"); err == nil {
		t.Fatal("invalid expression accepted")
	}
}
