// Package rubyast is the shared Ruby tree-sitter parsing substrate used by
// both internal/linker's hand-written link passes and internal/factpipe's
// hub providers.
//
// It exists to break the same import cycle internal/jsast breaks for
// JS/TS (see jsast's package doc): internal/linker imports
// internal/patterns, and internal/patterns imports internal/factpipe, so
// internal/factpipe cannot import internal/linker without creating
// factpipe -> linker -> patterns -> factpipe. rubyast has no dependency on
// any of the three, so both sides import it instead of one duplicating the
// other's parsing logic — this mirrors XM.17's JS fix
// (docs/factpipe-cross-framework-matching-plan.md), applied to the same
// duplication on the Ruby side (XM.19): internal/linker's ruby_tree_cache.go
// and internal/factpipe/parse_cache.go's rubyParseCache were two independent
// caches, so most .rb files parsed twice per index run.
//
// Unlike jsast.Parse, Parse here returns an explicit release func: a Ruby
// *sitter.Tree needs tree.Close() once nothing references its root anymore
// (go-tree-sitter's top-level ParseCtx, which jsast uses, discards the
// *Tree and keeps only the *Node, relying on a GC finalizer to eventually
// free it — fine for JS's per-Run-bounded lifetime, but this package keeps
// Ruby's original deterministic-Close behavior rather than changing its
// memory lifecycle as a side effect of unifying the cache).
package rubyast

import (
	"context"
	"os"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"
)

type parsedRuby struct {
	src  []byte
	root *sitter.Node
	tree *sitter.Tree
}

var (
	cacheMu sync.Mutex
	// cache is nil unless a link phase has called EnableCache. A
	// present-but-nil map value means "parse failed" — cached so a broken
	// file isn't retried once per pass.
	cache map[string]*parsedRuby
)

// EnableCache starts memoizing Ruby parses. Call once at the start of a link
// phase; pair with DisableCache (safe via defer). Not re-entrant — one link
// phase at a time.
func EnableCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cache = make(map[string]*parsedRuby)
}

// DisableCache closes every cached tree and clears the cache. After this
// call no *sitter.Node handed out by Parse during the phase is valid.
func DisableCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	for _, pr := range cache {
		if pr != nil && pr.tree != nil {
			pr.tree.Close()
		}
	}
	cache = nil
}

// Parse returns the parsed tree for an absolute file path. ok is false when
// the file can't be read or parsed — callers must handle that the same way
// they handled a read/parse error before. release must always be called
// when the caller is done with root/src (defer it); it closes the tree when
// the cache is inactive and is a no-op when the cache owns the tree.
func Parse(file string) (src []byte, root *sitter.Node, release func(), ok bool) {
	noop := func() {}

	cacheMu.Lock()
	c := cache
	if c != nil {
		if pr, seen := c[file]; seen {
			cacheMu.Unlock()
			if pr == nil {
				return nil, nil, noop, false
			}
			return pr.src, pr.root, noop, true
		}
	}
	cacheMu.Unlock()

	pr := parseFile(file)

	if c == nil {
		// No cache: hand ownership to the caller.
		if pr == nil {
			return nil, nil, noop, false
		}
		return pr.src, pr.root, func() { pr.tree.Close() }, true
	}

	cacheMu.Lock()
	// Another caller may have parsed it while we were unlocked (link passes
	// are sequential today, but keep this correct if that ever changes).
	if existing, seen := c[file]; seen {
		cacheMu.Unlock()
		if pr != nil && pr.tree != nil {
			pr.tree.Close()
		}
		if existing == nil {
			return nil, nil, noop, false
		}
		return existing.src, existing.root, noop, true
	}
	c[file] = pr
	cacheMu.Unlock()
	if pr == nil {
		return nil, nil, noop, false
	}
	return pr.src, pr.root, noop, true
}

func parseFile(file string) *parsedRuby {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil
	}
	return &parsedRuby{src: src, root: tree.RootNode(), tree: tree}
}
