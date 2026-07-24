package contract

import sitter "github.com/smacker/go-tree-sitter"

// vueKeyWalker is a no-op walker for Vue SFC files. Vue nav_link and event
// nodes carry either a literal path/expression (extracted by the splitter) or
// a dynamic_url ledger entry, so dynamic key enumeration is not needed here.
type vueKeyWalker struct{}

func (w *vueKeyWalker) Language() string { return "vue" }

func (w *vueKeyWalker) WalkKey(_ *sitter.Node, _ []byte, _ ConstResolver) ([]string, bool) {
	return nil, true // all Vue keys are static or already ledgered as dynamic_url
}

func init() {
	RegisterKeyWalker(&vueKeyWalker{})
}
