package policy

import (
	"testing"

	"github.com/chenhg5/agentflow/internal/model"
)

func TestUsesRuntimeMessageInspectsTheParsedSelector(t *testing.T) {
	cases := map[string]bool{
		"failure.message matches '^boom'":                true,
		"failure['message'] == 'boom'":                   true,
		"failure?.message == 'boom'":                     true,
		"failure.kind == 'start_failed'":                 false,
		"failure.kind == 'some_message'":                 false,
		"'message' in failure.kind":                      false,
		"failure.code == 1 && failure.kind == 'message'": false,
		// A computed selector could resolve to "message" at runtime.
		"failure['me' + 'ssage'] == 'boom'": true,
	}
	for expression, expected := range cases {
		got := UsesRuntimeMessage(&model.FallbackSpec{When: expression})
		if got != expected {
			t.Fatalf("%s: UsesRuntimeMessage=%v want %v", expression, got, expected)
		}
	}
	if UsesRuntimeMessage(nil) || UsesRuntimeMessage(&model.FallbackSpec{}) {
		t.Fatal("empty policy reported a message dependency")
	}
}

func TestUsesRuntimeAttemptInspectsTheParsedSelector(t *testing.T) {
	for expression, expected := range map[string]bool{
		"failure.attempt == 3":           true,
		"failure['attempt'] in [1, 2]":   true,
		"failure.kind == 'start_failed'": false,
		"failure.message == 'attempt 3'": false,
		"failure['att' + 'empt'] == 3":   true,
	} {
		if got := UsesRuntimeAttempt(&model.FallbackSpec{When: expression}); got != expected {
			t.Fatalf("%s: UsesRuntimeAttempt=%v want %v", expression, got, expected)
		}
	}
}

func TestFallbackInspectionFollowsLexicalAliases(t *testing.T) {
	if !UsesRuntimeMessage(&model.FallbackSpec{When: "let f = failure; f.message == 'boom'"}) {
		t.Fatal("message dependency hidden behind an alias was not detected")
	}
	if !UsesRuntimeAttempt(&model.FallbackSpec{When: "let f = failure; let g = f; g.attempt == 2"}) {
		t.Fatal("attempt dependency hidden behind aliases was not detected")
	}
	for _, expression := range []string{"toJSON(failure) contains 'boom'", "let f = failure; toJSON(f) contains 'boom'"} {
		if !UsesRuntimeMessage(&model.FallbackSpec{When: expression}) || !UsesRuntimeAttempt(&model.FallbackSpec{When: expression}) {
			t.Fatalf("whole-object failure read was not treated conservatively: %s", expression)
		}
	}
	if UsesRuntimeMessage(&model.FallbackSpec{When: "let f = failure; f.kind == 'exit_code'"}) {
		t.Fatal("a selected failure.kind alias was mistaken for a whole-object read")
	}
}

func TestConstantFallbackResult(t *testing.T) {
	for expression, want := range map[string]bool{
		"false":                                false,
		"true":                                 true,
		"let enabled = false; enabled":         false,
		"false && failure.kind == 'exit_code'": false,
		"true || failure.kind == 'session_invalid'": true,
	} {
		matched, known := ConstantFallbackResult(&model.FallbackSpec{When: expression})
		if !known || matched != want {
			t.Fatalf("%s: matched=%v known=%v want %v,true", expression, matched, known, want)
		}
	}
	for _, spec := range []*model.FallbackSpec{nil, {}, {When: "failure.kind == 'start_failed'"}} {
		if matched, known := ConstantFallbackResult(spec); known {
			t.Fatalf("%+v: matched=%v known=%v, want unknown", spec, matched, known)
		}
	}
}

func TestFallbackMayMatchOnlyPossibleFailureClasses(t *testing.T) {
	failures := []*model.ExecutionFailure{
		{Kind: "start_failed", Started: false, Executor: "worker"},
		{Kind: "exit_code", Started: true, Executor: "worker"},
	}
	for expression, want := range map[string]bool{
		"failure.kind == 'exit_code'":            true,
		"failure.kind == 'session_incompatible'": false,
		"false && failure.kind == 'exit_code'":   false,
		"failure.started == false":               true,
	} {
		got, known := FallbackMayMatchAny(&model.FallbackSpec{When: expression}, failures)
		if !known || got != want {
			t.Fatalf("%s: mayMatch=%v known=%v, want %v,true", expression, got, known, want)
		}
	}
	for _, expression := range []string{"failure.code == 75", "toJSON(failure) contains 'boom'"} {
		if got, known := FallbackMayMatchAny(&model.FallbackSpec{When: expression}, failures); known || !got {
			t.Fatalf("%s: mayMatch=%v known=%v, want true,false", expression, got, known)
		}
	}
}
