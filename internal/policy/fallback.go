package policy

import (
	"encoding/json"

	"github.com/chenhg5/agentflow/internal/condition"
	"github.com/chenhg5/agentflow/internal/model"
)

// AllowsFallback reports whether the node's fallback policy selects the next
// candidate for a structured failure. Runtime fallback selection and static
// workspace reachability share this rule so they cannot disagree.
func AllowsFallback(spec *model.FallbackSpec, failure *model.ExecutionFailure) (bool, error) {
	if failure == nil {
		return false, nil
	}
	if spec == nil || spec.When == "" {
		return failure.Kind == "start_failed", nil
	}
	data, _ := json.Marshal(failure)
	var value map[string]any
	_ = json.Unmarshal(data, &value)
	return condition.Evaluate(spec.When, map[string]any{"failure": value})
}

// UsesRuntimeMessage reports whether the fallback expression reads
// failure.message (dot or index syntax), whose exact runtime text static
// validation cannot reproduce. It inspects the parsed expression instead of
// treating any identifier or string literal containing "message" as that
// field. Computed member selectors are treated conservatively because they
// could resolve to "message" at runtime.
func UsesRuntimeMessage(spec *model.FallbackSpec) bool {
	return usesFailureField(spec, "message")
}

// UsesRuntimeAttempt reports whether fallback selection reads the global
// attempt sequence. That sequence continues across visits and is not generally
// knowable from a single static launch probe.
func UsesRuntimeAttempt(spec *model.FallbackSpec) bool {
	return usesFailureField(spec, "attempt")
}

// ConstantFallbackResult reports the result only when the parsed expression
// proves a constant through literals and short-circuiting. Runtime-dependent
// policies deliberately remain unknown.
func ConstantFallbackResult(spec *model.FallbackSpec) (matched bool, known bool) {
	if spec == nil || spec.When == "" {
		return false, false
	}
	matched, known, err := condition.StaticBool(spec.When)
	if err != nil {
		return false, false
	}
	return matched, known
}

// FallbackMayMatchAny determines whether a fallback can select at least one
// member of a finite set of statically possible failures. It evaluates only
// fields whose values are supplied exactly by those failures. Message, code,
// signal, attempt, whole-object, dynamic, and unrelated reads stay unknown.
func FallbackMayMatchAny(spec *model.FallbackSpec, failures []*model.ExecutionFailure) (mayMatch bool, known bool) {
	if matched, constant := ConstantFallbackResult(spec); constant {
		return matched, true
	}
	if spec == nil || spec.When == "" {
		for _, failure := range failures {
			if failure != nil && failure.Kind == "start_failed" {
				return true, true
			}
		}
		return false, true
	}
	references, err := condition.References(spec.When)
	if err != nil {
		return true, false
	}
	for _, reference := range references {
		if reference.Dynamic || len(reference.Path) != 2 || reference.Path[0] != "failure" {
			return true, false
		}
		switch reference.Path[1] {
		case "kind", "started", "timedOut", "executor":
		default:
			return true, false
		}
	}
	for _, failure := range failures {
		matched, err := AllowsFallback(spec, failure)
		if err != nil {
			return true, false
		}
		if matched {
			return true, true
		}
	}
	return false, true
}

func usesFailureField(spec *model.FallbackSpec, field string) bool {
	if spec == nil || spec.When == "" {
		return false
	}
	references, err := condition.References(spec.When)
	if err != nil {
		return false
	}
	for _, reference := range references {
		if len(reference.Path) == 0 || reference.Path[0] != "failure" {
			continue
		}
		if reference.Dynamic || len(reference.Path) == 1 || len(reference.Path) > 1 && reference.Path[1] == field {
			return true
		}
	}
	return false
}
