package contract

import sitter "github.com/smacker/go-tree-sitter"

// goKeyWalker enumerates literal alternatives for Go key expressions.
// Go lacks inline ternaries; the main shape (b) case is package-level constants.
type goKeyWalker struct{}

func (w *goKeyWalker) Language() string { return "go" }

func (w *goKeyWalker) WalkKey(node *sitter.Node, src []byte, consts ConstResolver) ([]string, bool) {
	if node == nil {
		return nil, true
	}
	return walkGoExpr(node, src, consts, 0)
}

func walkGoExpr(node *sitter.Node, src []byte, consts ConstResolver, depth int) ([]string, bool) {
	if depth > keyWalkerMaxDepth {
		return nil, true
	}
	switch node.Type() {
	case "interpreted_string_literal":
		text := string(src[node.StartByte():node.EndByte()])
		return []string{stripKeyLiteral(text)}, false

	case "raw_string_literal":
		text := string(src[node.StartByte():node.EndByte()])
		// Raw strings use backticks
		return []string{stripKeyLiteral(text)}, false

	case "identifier":
		// Shape (b): package-level const reference
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
	RegisterKeyWalker(&goKeyWalker{})
}
