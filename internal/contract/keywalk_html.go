package contract

import sitter "github.com/smacker/go-tree-sitter"

// htmlKeyWalker is a no-op: HTML attribute values are always static literals
// captured as string fragments by the tree-sitter HTML grammar. Dynamic
// attributes (template variables) are in a separate template language layer
// and are not visible to polyflow's static analysis. Explicitly registered to
// distinguish "considered, not needed" from "forgotten" in doctor output.
type htmlKeyWalker struct{}

func (w *htmlKeyWalker) Language() string { return "html" }

func (w *htmlKeyWalker) WalkKey(_ *sitter.Node, _ []byte, _ ConstResolver) ([]string, bool) {
	// HTML attributes are always static; no enumeration or dynamic detection needed.
	return nil, false
}

func init() {
	RegisterNoOpKeyWalker(&htmlKeyWalker{})
}
