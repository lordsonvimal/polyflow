package factpipe

import (
	sitter "github.com/smacker/go-tree-sitter"
)

// XM.14 (docs/factpipe-cross-framework-matching-plan.md): rdReadAndParseRuby
// re-read + re-parsed a file from scratch on every call, with no
// memoization. Real cedar profile: runtime.cgocall was 46% of total index
// CPU, tree-sitter ParseCtx 15.56% cum.
//
// JS/TS had the same problem (js_hoc/js_client_routes/js_mobx/
// pusher_js_consumer/react_prop_urls all re-parsed independently) but XM.14
// gave it its own cache here (pjcParseJS) rather than reusing
// internal/jsast's — jsast didn't exist as a neutral shared package yet.
// XM.17 (2026-09-16) deleted that duplicate cache once jsast did exist:
// every JS/TS parse in this package now goes through jsast.Parse, sharing
// internal/linker's cache instead of maintaining a second, independent one —
// see internal/indexer's EnableJSTreeCache/DisableJSTreeCache, which already
// wraps every call path that reaches these hubs (verified: every
// pipeline.Run call site lives inside internal/indexer's link-pass loop),
// so jsast's enable/disable-per-Run teardown gives the same one-Run
// freshness guarantee the old (path, size, mtime) key gave, without a
// second cache to keep in sync.
//
// XM.19 (2026-09-16) did the same for Ruby: rdReadAndParseRuby (hub_rails_
// devise.go) used to go through this file's own rubyParseCache/rubyParseSF,
// independent of internal/linker's ruby_tree_cache.go — the same
// duplication XM.17 fixed for JS, just not caught at the time because
// internal/rubyast didn't exist yet either. rdReadAndParseRuby now calls
// internal/rubyast.Parse directly, sharing internal/linker's cache.
//
// WarmParseTree stays here: internal/factpipe/pipeline.go (the shared
// cross-framework tree-sitter matcher, XM.5) calls it directly and cannot
// import internal/jsast/internal/rubyast's cache owners without pulling in
// their Enable/DisableCache lifecycle, which pipeline.go doesn't manage —
// it warms whatever *sitter.Node it's just parsed itself, independent of
// which cache (if any) owns the tree.

// WarmParseTree touches every node in root exactly once, single-threaded,
// immediately after parsing and before a cached tree/root is ever handed to
// more than one goroutine (XM.5, docs/factpipe-cross-framework-matching-
// plan.md). go-tree-sitter's *sitter.Tree lazily memoizes each C node's Go
// *Node wrapper the first time any navigation method (Child/NamedChild/
// Parent/NextSibling/...) reaches it (bindings.go's cachedNode: a plain,
// unsynchronized map read-or-insert) — with XM.5's frameworks now running
// concurrently, two frameworks whose matches/hub walks both first-touch the
// same file's shared tree race on that map. Walking via Child (not
// NamedChild) covers every node, named or anonymous, so every subsequent
// navigation from any goroutine — via any method, not just the one used
// here — is a pure map read against an already-fully-populated cache: safe
// for unlimited concurrent readers with no further writes ever occurring.
func WarmParseTree(root *sitter.Node) {
	if root == nil {
		return
	}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		cc := int(n.ChildCount())
		for i := 0; i < cc; i++ {
			if c := n.Child(i); c != nil {
				walk(c)
			}
		}
	}
	walk(root)
}
