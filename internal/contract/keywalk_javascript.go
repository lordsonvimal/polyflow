package contract

import sitter "github.com/smacker/go-tree-sitter"

// jsKeyWalker enumerates literal alternatives for JS/TS/JSX/TSX key expressions.
// Handles: string literals, ternary expressions (depth ≤2, branches ≤8),
// template literals without interpolations, identifier constant references (shape b).
type jsKeyWalker struct{}

func (w *jsKeyWalker) Language() string { return "javascript" }

func (w *jsKeyWalker) WalkKey(node *sitter.Node, src []byte, consts ConstResolver) ([]string, bool) {
	if node == nil {
		return nil, true
	}
	return walkJSExpr(node, src, consts, 0)
}

func walkJSExpr(node *sitter.Node, src []byte, consts ConstResolver, depth int) ([]string, bool) {
	if depth > keyWalkerMaxDepth {
		return nil, true
	}
	switch node.Type() {
	case "string":
		// String literal "..." or '...': extract content
		text := string(src[node.StartByte():node.EndByte()])
		return []string{stripKeyLiteral(text)}, false

	case "template_string":
		// Template literal: static only (no interpolations)
		for i := 0; i < int(node.ChildCount()); i++ {
			if child := node.Child(i); child != nil && child.Type() == "template_substitution" {
				return nil, true
			}
		}
		text := string(src[node.StartByte():node.EndByte()])
		return []string{stripKeyLiteral(text)}, false

	case "ternary_expression":
		cons := node.ChildByFieldName("consequence")
		alt := node.ChildByFieldName("alternative")
		if cons == nil || alt == nil {
			return nil, true
		}
		consVals, consDyn := walkJSExpr(cons, src, consts, depth+1)
		if consDyn {
			return nil, true
		}
		altVals, altDyn := walkJSExpr(alt, src, consts, depth+1)
		if altDyn {
			return nil, true
		}
		combined := append(consVals, altVals...)
		if len(combined) > keyWalkerMaxBranches {
			return nil, true
		}
		return combined, false

	case "identifier":
		// Shape (b): constant reference — resolve via ConstResolver
		name := string(src[node.StartByte():node.EndByte()])
		if v, ok := consts(name); ok {
			return []string{v}, false
		}
		return nil, true

	default:
		return nil, true
	}
}

func init() {
	RegisterKeyWalker(&jsKeyWalker{})
}
