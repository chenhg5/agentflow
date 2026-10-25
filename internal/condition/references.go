package condition

import (
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/parser"
)

// Reference is a top-level runtime value read by a parsed Expr expression.
// Path contains literal member selectors (for example, nodes.step.input).
// Dynamic is true when a computed selector prevents the full path from being
// known statically.
type Reference struct {
	Path    []string
	Dynamic bool
}

// References parses an Expr expression and returns its external value reads.
// Keeping this in the condition package lets runtime policy inspection and
// workspace validation reason about the same parsed expression shape.
func References(source string) ([]Reference, error) {
	tree, err := parser.Parse(source)
	if err != nil {
		return nil, err
	}
	references := []Reference{}
	collectReferences(tree.Node, &references, map[string][]Reference{})
	return references, nil
}

func collectReferences(node ast.Node, references *[]Reference, aliases map[string][]Reference) {
	switch value := node.(type) {
	case *ast.IdentifierNode:
		if resolved, ok := aliases[value.Value]; ok {
			*references = append(*references, cloneReferences(resolved)...)
			return
		}
		*references = append(*references, Reference{Path: []string{value.Value}})
	case *ast.MemberNode:
		if path, dynamic, ok := memberReference(value); ok {
			*references = append(*references, expandReference(path, dynamic, aliases)...)
			if dynamic {
				collectReferences(value.Property, references, aliases)
			}
			return
		}
		collectReferences(value.Node, references, aliases)
		collectReferences(value.Property, references, aliases)
	case *ast.ChainNode:
		collectReferences(value.Node, references, aliases)
	case *ast.UnaryNode:
		collectReferences(value.Node, references, aliases)
	case *ast.BinaryNode:
		collectReferences(value.Left, references, aliases)
		collectReferences(value.Right, references, aliases)
	case *ast.SliceNode:
		collectReferences(value.Node, references, aliases)
		if value.From != nil {
			collectReferences(value.From, references, aliases)
		}
		if value.To != nil {
			collectReferences(value.To, references, aliases)
		}
	case *ast.CallNode:
		collectReferences(value.Callee, references, aliases)
		for _, argument := range value.Arguments {
			collectReferences(argument, references, aliases)
		}
	case *ast.BuiltinNode:
		for _, argument := range value.Arguments {
			collectReferences(argument, references, aliases)
		}
	case *ast.PredicateNode:
		collectReferences(value.Node, references, aliases)
	case *ast.VariableDeclaratorNode:
		valueReferences := []Reference{}
		collectReferences(value.Value, &valueReferences, aliases)
		scoped := cloneAliases(aliases)
		scoped[value.Name] = cloneReferences(valueReferences)
		collectReferences(value.Expr, references, scoped)
	case *ast.SequenceNode:
		for _, expression := range value.Nodes {
			collectReferences(expression, references, aliases)
		}
	case *ast.ConditionalNode:
		collectReferences(value.Cond, references, aliases)
		collectReferences(value.Exp1, references, aliases)
		collectReferences(value.Exp2, references, aliases)
	case *ast.ArrayNode:
		for _, item := range value.Nodes {
			collectReferences(item, references, aliases)
		}
	case *ast.MapNode:
		for _, pair := range value.Pairs {
			collectReferences(pair, references, aliases)
		}
	case *ast.PairNode:
		collectReferences(value.Key, references, aliases)
		collectReferences(value.Value, references, aliases)
	}
}

func expandReference(path []string, dynamic bool, aliases map[string][]Reference) []Reference {
	if len(path) == 0 {
		return nil
	}
	resolved, ok := aliases[path[0]]
	if !ok {
		return []Reference{{Path: append([]string(nil), path...), Dynamic: dynamic}}
	}
	expanded := make([]Reference, 0, len(resolved))
	for _, reference := range resolved {
		expanded = append(expanded, Reference{
			Path:    append(append([]string(nil), reference.Path...), path[1:]...),
			Dynamic: dynamic || reference.Dynamic,
		})
	}
	return expanded
}

func cloneAliases(aliases map[string][]Reference) map[string][]Reference {
	cloned := make(map[string][]Reference, len(aliases)+1)
	for name, references := range aliases {
		cloned[name] = cloneReferences(references)
	}
	return cloned
}

func cloneReferences(references []Reference) []Reference {
	cloned := make([]Reference, len(references))
	for index, reference := range references {
		cloned[index] = Reference{Path: append([]string(nil), reference.Path...), Dynamic: reference.Dynamic}
	}
	return cloned
}

func memberReference(node ast.Node) ([]string, bool, bool) {
	switch value := node.(type) {
	case *ast.IdentifierNode:
		return []string{value.Value}, false, true
	case *ast.ChainNode:
		return memberReference(value.Node)
	case *ast.MemberNode:
		path, dynamic, ok := memberReference(value.Node)
		if !ok {
			return nil, false, false
		}
		property, literal := value.Property.(*ast.StringNode)
		if !literal {
			return path, true, true
		}
		return append(path, property.Value), dynamic, true
	default:
		return nil, false, false
	}
}
