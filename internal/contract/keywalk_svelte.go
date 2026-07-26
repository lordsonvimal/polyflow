package contract

import sitter "github.com/smacker/go-tree-sitter"

// svelteKeyWalker is a no-op walker for Svelte SFC files. Svelte nav_link and
// event nodes carry either a literal path/expression (extracted by the splitter
// using the JS walker) or a dynamic_url ledger entry, so dynamic key
// enumeration by the contract engine is not needed here.
type svelteKeyWalker struct{}

func (w *svelteKeyWalker) Language() string { return "svelte" }

func (w *svelteKeyWalker) WalkKey(_ *sitter.Node, _ []byte, _ ConstResolver) ([]string, bool) {
	return nil, true // all Svelte keys are static or already ledgered as dynamic_url
}

func init() {
	RegisterKeyWalker(&svelteKeyWalker{})
}
