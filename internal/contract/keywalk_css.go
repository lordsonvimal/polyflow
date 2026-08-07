package contract

import sitter "github.com/smacker/go-tree-sitter"

// cssKeyWalker is a no-op: a stylesheet declares no message keys, routing keys
// or queue names, so there is nothing for contract key resolution to walk. The
// Tier K.5 reader (internal/parser/scss.go) mints only selectors and
// `@font-face` sources, and it hands the parser no tree-sitter tree at all —
// the extraction is a hand-written scanner in internal/css.
//
// Registered explicitly so doctor output distinguishes "considered, not needed"
// from "forgotten", which is what the walker-coverage guard checks.
type cssKeyWalker struct{}

func (w *cssKeyWalker) Language() string { return "css" }

func (w *cssKeyWalker) WalkKey(_ *sitter.Node, _ []byte, _ ConstResolver) ([]string, bool) {
	return nil, false
}

func init() {
	RegisterNoOpKeyWalker(&cssKeyWalker{})
}
