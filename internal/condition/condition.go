package condition

import (
	"fmt"

	"github.com/expr-lang/expr"
)

// Validate checks that a transition expression is a boolean expression.
func Validate(source string) error {
	if source == "" {
		return nil
	}
	_, err := expr.Compile(source, expr.AsBool(), expr.AllowUndefinedVariables())
	return err
}

// Evaluate runs an expression against the current input and node results.
// Empty conditions are default edges and always match.
func Evaluate(source string, environment map[string]any) (bool, error) {
	if source == "" {
		return true, nil
	}
	program, err := expr.Compile(source, expr.AsBool(), expr.AllowUndefinedVariables())
	if err != nil {
		return false, err
	}
	value, err := expr.Run(program, environment)
	if err != nil {
		return false, fmt.Errorf("condition %q: %w", source, err)
	}
	matched, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("condition %q did not return a boolean", source)
	}
	return matched, nil
}
