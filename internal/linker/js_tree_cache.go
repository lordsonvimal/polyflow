package linker

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/jsast"
)

// JS/TS tree-sitter parse cache — delegates to internal/jsast, which owns
// the actual cache so internal/factpipe's hub providers can share it too
// (see internal/jsast/parse.go's package doc for why factpipe can't just
// import this package directly).

// EnableJSTreeCache starts memoizing JS/TS parses. Pair with
// DisableJSTreeCache (safe via defer). Not re-entrant.
func EnableJSTreeCache() { jsast.EnableCache() }

// DisableJSTreeCache clears the cache.
func DisableJSTreeCache() { jsast.DisableCache() }

// jsParse reads + parses file with its grammar, memoized for the link phase.
// ok is false when the file can't be read or parsed, or its extension has no
// grammar.
func jsParse(file string) (src []byte, root *sitter.Node, lang *sitter.Language, ok bool) {
	return jsast.Parse(file)
}
