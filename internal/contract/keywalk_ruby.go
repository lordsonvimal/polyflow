package contract

import sitter "github.com/smacker/go-tree-sitter"

// rubyKeyWalker enumerates literal alternatives for Ruby key expressions.
// Handles: string literals, ternary (depth ≤2), constant references (shape b).
type rubyKeyWalker struct{}

func (w *rubyKeyWalker) Language() string { return "ruby" }

func (w *rubyKeyWalker) WalkKey(node *sitter.Node, src []byte, consts ConstResolver) ([]string, bool) {
	if node == nil {
		return nil, true
	}
	return walkRubyExpr(node, src, consts, 0)
}

func walkRubyExpr(node *sitter.Node, src []byte, consts ConstResolver, depth int) ([]string, bool) {
	if depth > keyWalkerMaxDepth {
		return nil, true
	}
	switch node.Type() {
	case "string", "simple_string":
		text := string(src[node.StartByte():node.EndByte()])
		return []string{stripKeyLiteral(text)}, false

	case "if":
		// Ternary-style: `cond ? a : b` parses as if/else in Ruby
		thenClause := node.ChildByFieldName("consequence")
		elseClause := node.ChildByFieldName("alternative")
		if thenClause == nil || elseClause == nil {
			return nil, true
		}
		thenVals, thenDyn := walkRubyExpr(thenClause, src, consts, depth+1)
		if thenDyn {
			return nil, true
		}
		elseVals, elseDyn := walkRubyExpr(elseClause, src, consts, depth+1)
		if elseDyn {
			return nil, true
		}
		combined := append(thenVals, elseVals...)
		if len(combined) > keyWalkerMaxBranches {
			return nil, true
		}
		return combined, false

	case "constant":
		// Shape (b): Ruby constant (ALL_CAPS by convention)
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
	RegisterKeyWalker(&rubyKeyWalker{})
}
