package contract

import sitter "github.com/smacker/go-tree-sitter"

// shellKeyWalker is a no-op walker for shell (bash) files. Shell declares no
// HTTP/messaging producer/consumer keys (checklist item 4's descope — no
// request/response framework), so the shared contract.KeyWalker has nothing
// to walk. SH1's own invocation-path literal-vs-variable check is a
// narrower, separate mechanism (internal/parser/shell.go's
// shellPathIsLiteral) that does not route through this interface — see
// docs/shell-sql-language-plan.md's checklist item 7 disposition.
type shellKeyWalker struct{}

func (w *shellKeyWalker) Language() string { return "bash" }

func (w *shellKeyWalker) WalkKey(_ *sitter.Node, _ []byte, _ ConstResolver) ([]string, bool) {
	return nil, true
}

func init() {
	RegisterNoOpKeyWalker(&shellKeyWalker{})
}
