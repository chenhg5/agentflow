package condition

import (
	"strings"
	"testing"
)

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

func TestReferencesUsesParsedMemberPaths(t *testing.T) {
	references, err := References("input.loop == inputs.loop && nodes.work.input['loop'] == failure[kind]")
	if err != nil {
		t.Fatal(err)
	}
	want := []Reference{
		{Path: []string{"input", "loop"}},
		{Path: []string{"inputs", "loop"}},
		{Path: []string{"nodes", "work", "input", "loop"}},
		{Path: []string{"failure"}, Dynamic: true},
		{Path: []string{"kind"}},
	}
	if len(references) != len(want) {
		t.Fatalf("References returned %#v, want %#v", references, want)
	}
	for index := range want {
		if references[index].Dynamic != want[index].Dynamic || len(references[index].Path) != len(want[index].Path) {
			t.Fatalf("reference %d = %#v, want %#v", index, references[index], want[index])
		}
		for part := range want[index].Path {
			if references[index].Path[part] != want[index].Path[part] {
				t.Fatalf("reference %d = %#v, want %#v", index, references[index], want[index])
			}
		}
	}
}

func TestReferencesResolvesLexicalAliases(t *testing.T) {
	references, err := References("let f = failure; let g = f; g.message == 'boom' && f.attempt == 2")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, reference := range references {
		found[strings.Join(reference.Path, ".")] = true
	}
	for _, path := range []string{"failure.message", "failure.attempt"} {
		if !found[path] {
			t.Fatalf("References did not resolve %s through lexical aliases: %#v", path, references)
		}
	}
}

func TestReferencesDistinguishesWholeAliasFromSelectedField(t *testing.T) {
	selected, err := References("let f = failure; f.kind == 'start_failed'")
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || strings.Join(selected[0].Path, ".") != "failure.kind" {
		t.Fatalf("selected alias reference = %#v, want only failure.kind", selected)
	}
	whole, err := References("let f = failure; toJSON(f) contains 'boom'")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, reference := range whole {
		if len(reference.Path) == 1 && reference.Path[0] == "failure" {
			found = true
		}
	}
	if !found {
		t.Fatalf("whole-object alias provenance was lost: %#v", whole)
	}
}

func TestStaticBoolUsesParsedShortCircuitSemantics(t *testing.T) {
	for expression, want := range map[string]bool{
		"false && failure.kind == 'exit_code'": false,
		"true || failure.kind == 'exit_code'":  true,
		"let enabled = false; enabled":         false,
		"!false":                               true,
	} {
		got, known, err := StaticBool(expression)
		if err != nil || !known || got != want {
			t.Fatalf("%s: value=%v known=%v err=%v, want %v,true,nil", expression, got, known, err, want)
		}
	}
	for _, expression := range []string{"failure.kind == 'exit_code'", "failure == nil", "let f = failure; false"} {
		if got, known, err := StaticBool(expression); err != nil || known {
			t.Fatalf("%s: value=%v known=%v err=%v, want unknown", expression, got, known, err)
		}
	}
}
