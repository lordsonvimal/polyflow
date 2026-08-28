package contract

import sitter "github.com/smacker/go-tree-sitter"

// sqlKeyWalker is a no-op walker for SQL files. SQL declares no
// HTTP/messaging producer/consumer keys (checklist item 4's descope — SQL
// has no request/response framework), so the shared contract.KeyWalker has
// nothing to walk. See docs/shell-sql-language-plan.md's checklist item 7
// disposition.
type sqlKeyWalker struct{}

func (w *sqlKeyWalker) Language() string { return "sql" }

func (w *sqlKeyWalker) WalkKey(_ *sitter.Node, _ []byte, _ ConstResolver) ([]string, bool) {
	return nil, true
}

func init() {
	RegisterNoOpKeyWalker(&sqlKeyWalker{})
}
