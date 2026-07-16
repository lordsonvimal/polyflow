package contract

import sitter "github.com/smacker/go-tree-sitter"

// templKeyWalker enumerates literal alternatives for templ key expressions.
// Templ uses Go-like expressions in attribute positions, so this delegates
// to walkGoExpr. Registered separately so the doctor walker row can show
// "templ: yes" independently of "go: yes".
type templKeyWalker struct{}

func (w *templKeyWalker) Language() string { return "templ" }

func (w *templKeyWalker) WalkKey(node *sitter.Node, src []byte, consts ConstResolver) ([]string, bool) {
	if node == nil {
		return nil, true
	}
	// templ attribute expressions are Go expressions
	return walkGoExpr(node, src, consts, 0)
}

func init() {
	RegisterKeyWalker(&templKeyWalker{})
}
