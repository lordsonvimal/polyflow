package contract

import sitter "github.com/smacker/go-tree-sitter"

// erbKeyWalker is a no-op walker for ERB files. ERB nav_link nodes carry either
// a literal path (resolved at parse time) or a Rails helper name (resolved by the
// linker pass before the contract engine runs), so dynamic key enumeration is
// never needed for this language.
type erbKeyWalker struct{}

func (w *erbKeyWalker) Language() string { return "erb" }

func (w *erbKeyWalker) WalkKey(_ *sitter.Node, _ []byte, _ ConstResolver) ([]string, bool) {
	return nil, true // treat all ERB keys as static (no dynamic branching)
}

func init() {
	RegisterKeyWalker(&erbKeyWalker{})
}
