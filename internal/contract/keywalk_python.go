package contract

import sitter "github.com/smacker/go-tree-sitter"

// pythonKeyWalker is a no-op walker for Python files. Dynamic key enumeration
// (ternary/if-else URL expressions) is deferred to a future phase; all Python
// keys are treated as needing the dynamic ledger when non-literal.
type pythonKeyWalker struct{}

func (w *pythonKeyWalker) Language() string { return "python" }

func (w *pythonKeyWalker) WalkKey(_ *sitter.Node, _ []byte, _ ConstResolver) ([]string, bool) {
	return nil, true // dynamic — no branch enumeration implemented yet
}

func init() {
	RegisterKeyWalker(&pythonKeyWalker{})
}
