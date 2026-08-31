package linker

import (
	"context"
	"os"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"
)

// Ruby tree-sitter parse cache.
//
// Every ruby_* linker pass used to re-read and re-parse each .rb file in the
// workspace: LinkRubyTypeRelations, LinkRubyClassMethodCalls,
// LinkRubyReceiverTypeCalls, LinkRubyAssociations, LinkRailsFilters,
// LinkRailsDeviseDefaultRoutes and the Ruby half of import_edges all walk the
// same source tree independently. On a Rails repo (~2.3k .rb files) that is
// ~7 full tree-sitter passes and dominated the cold-index link phase.
//
// EnableRubyTreeCache turns on process-wide memoization for the duration of a
// link phase; rubyParse then parses each file exactly once and hands every
// caller the same *sitter.Node. DisableRubyTreeCache frees every tree — it is
// the single owner of their lifetime while the cache is active.
//
// When the cache is inactive (the default — every linker unit test that calls
// a pass directly), rubyParse parses fresh and its release callback closes the
// tree, exactly as the old inline `defer tree.Close()` did.

type parsedRuby struct {
	src  []byte
	root *sitter.Node
	tree *sitter.Tree
}

var (
	rubyCacheMu sync.Mutex
	// rubyCache is nil unless a link phase has called EnableRubyTreeCache.
	// A present-but-nil map value means "parse failed" — cached so a broken
	// file isn't retried once per pass.
	rubyCache map[string]*parsedRuby
)

// EnableRubyTreeCache starts memoizing Ruby parses. Call once at the start of
// a link phase; pair with DisableRubyTreeCache (safe via defer). Not
// re-entrant — one link phase at a time.
func EnableRubyTreeCache() {
	rubyCacheMu.Lock()
	defer rubyCacheMu.Unlock()
	rubyCache = make(map[string]*parsedRuby)
}

// DisableRubyTreeCache closes every cached tree and clears the cache. After
// this call no *sitter.Node handed out by rubyParse during the phase is valid.
func DisableRubyTreeCache() {
	rubyCacheMu.Lock()
	defer rubyCacheMu.Unlock()
	for _, pr := range rubyCache {
		if pr != nil && pr.tree != nil {
			pr.tree.Close()
		}
	}
	rubyCache = nil
}

// rubyParse returns the parsed tree for an absolute file path. ok is false
// when the file can't be read or parsed — callers must handle that the same
// way they handled a read/parse error before. release must always be called
// when the caller is done with root/src (defer it); it closes the tree when
// the cache is inactive and is a no-op when the cache owns the tree.
func rubyParse(file string) (src []byte, root *sitter.Node, release func(), ok bool) {
	noop := func() {}

	rubyCacheMu.Lock()
	cache := rubyCache
	if cache != nil {
		if pr, seen := cache[file]; seen {
			rubyCacheMu.Unlock()
			if pr == nil {
				return nil, nil, noop, false
			}
			return pr.src, pr.root, noop, true
		}
	}
	rubyCacheMu.Unlock()

	pr := parseRubyFile(file)

	if cache == nil {
		// No cache: hand ownership to the caller.
		if pr == nil {
			return nil, nil, noop, false
		}
		return pr.src, pr.root, func() { pr.tree.Close() }, true
	}

	rubyCacheMu.Lock()
	// Another pass may have parsed it while we were unlocked (link passes are
	// sequential today, but keep this correct if that ever changes).
	if existing, seen := cache[file]; seen {
		rubyCacheMu.Unlock()
		if pr != nil && pr.tree != nil {
			pr.tree.Close()
		}
		if existing == nil {
			return nil, nil, noop, false
		}
		return existing.src, existing.root, noop, true
	}
	cache[file] = pr
	rubyCacheMu.Unlock()
	if pr == nil {
		return nil, nil, noop, false
	}
	return pr.src, pr.root, noop, true
}

func parseRubyFile(file string) *parsedRuby {
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
