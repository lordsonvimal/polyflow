package linker

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/rubyast"
)

// Ruby tree-sitter parse cache — delegates to internal/rubyast, which owns
// the actual cache so internal/factpipe's hub providers can share it too
// (see internal/rubyast/parse.go's package doc for why factpipe can't just
// import this package directly). XM.19 (docs/factpipe-cross-framework-
// matching-plan.md): this used to be its own independent cache, duplicating
// internal/factpipe/parse_cache.go's rubyParseCache — most .rb files parsed
// twice per index run.

// EnableRubyTreeCache starts memoizing Ruby parses. Call once at the start
// of a link phase; pair with DisableRubyTreeCache (safe via defer). Not
// re-entrant — one link phase at a time.
func EnableRubyTreeCache() { rubyast.EnableCache() }

// DisableRubyTreeCache closes every cached tree and clears the cache. After
// this call no *sitter.Node handed out by rubyParse during the phase is
// valid.
func DisableRubyTreeCache() { rubyast.DisableCache() }

// rubyParse returns the parsed tree for an absolute file path. ok is false
// when the file can't be read or parsed — callers must handle that the same
// way they handled a read/parse error before. release must always be called
// when the caller is done with root/src (defer it); it closes the tree when
// the cache is inactive and is a no-op when the cache owns the tree.
func rubyParse(file string) (src []byte, root *sitter.Node, release func(), ok bool) {
	return rubyast.Parse(file)
}
