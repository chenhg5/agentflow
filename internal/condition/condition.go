package condition

import (
	"fmt"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
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

// StaticBool reports boolean expressions whose result is fixed by literals
// and lexical boolean bindings. It deliberately implements only a small,
// parsed subset, including left-to-right short-circuiting, so callers can
// prove constants without inventing values for runtime references.
func StaticBool(source string) (value bool, known bool, err error) {
	tree, err := parser.Parse(source)
	if err != nil {
		return false, false, err
	}
	value, known = staticBoolNode(tree.Node, map[string]bool{})
	return value, known, nil
}

func staticBoolNode(node ast.Node, bindings map[string]bool) (bool, bool) {
	switch value := node.(type) {
	case *ast.BoolNode:
		return value.Value, true
	case *ast.IdentifierNode:
		resolved, ok := bindings[value.Value]
		return resolved, ok
	case *ast.ChainNode:
		return staticBoolNode(value.Node, bindings)
	case *ast.UnaryNode:
		if value.Operator != "!" && value.Operator != "not" {
			return false, false
		}
		resolved, known := staticBoolNode(value.Node, bindings)
		return !resolved, known
	case *ast.BinaryNode:
		left, leftKnown := staticBoolNode(value.Left, bindings)
		switch value.Operator {
		case "&&", "and":
			if leftKnown && !left {
				return false, true
			}
			if leftKnown && left {
				return staticBoolNode(value.Right, bindings)
			}
		case "||", "or":
			if leftKnown && left {
				return true, true
			}
			if leftKnown && !left {
				return staticBoolNode(value.Right, bindings)
			}
		}
	case *ast.ConditionalNode:
		condition, conditionKnown := staticBoolNode(value.Cond, bindings)
		if !conditionKnown {
			return false, false
		}
		if condition {
			return staticBoolNode(value.Exp1, bindings)
		}
		return staticBoolNode(value.Exp2, bindings)
	case *ast.VariableDeclaratorNode:
		bound, boundKnown := staticBoolNode(value.Value, bindings)
		if !boundKnown {
			return false, false
		}
		scoped := make(map[string]bool, len(bindings)+1)
		for name, resolved := range bindings {
			scoped[name] = resolved
		}
		scoped[value.Name] = bound
		return staticBoolNode(value.Expr, scoped)
	}
	return false, false
}
