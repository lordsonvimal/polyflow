package factpipe

import (
	"os"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	"golang.org/x/sync/singleflight"
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
// The Ruby cache below still needs its own (path, size, mtime) key: Ruby has
// no equivalent shared-package cache to fold into, and rdReadAndParseRuby's
// tree needs an explicit Close() a shared jsast-style cache doesn't have to
// deal with (see rubyParseCache's doc comment below).
type fileCacheKey struct {
	size  int64
	mtime int64
}

func statCacheKey(file string) (fileCacheKey, bool) {
	fi, err := os.Stat(file)
	if err != nil {
		return fileCacheKey{}, false
	}
	return fileCacheKey{size: fi.Size(), mtime: fi.ModTime().UnixNano()}, true
}

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

// rubyParseCache memoizes rdReadAndParseRuby. Unlike the JS path, a Ruby
// *sitter.Tree needs an explicit tree.Close() once nothing references its
// root anymore (rdReadAndParseRuby's own release func) — this cache takes
// over that ownership: a cached tree is never closed by its caller (they get
// a no-op release), only by this cache itself, when a fresher parse replaces
// a stale entry (the file changed on disk between two `serve` reindexes).
type rubyParseEntry struct {
	key  fileCacheKey
	src  []byte
	root *sitter.Node
	tree *sitter.Tree
	ok   bool
}

var (
	rubyParseMu    sync.Mutex
	rubyParseCache = map[string]*rubyParseEntry{}
	// rubyParseSF (XM.5) dedupes concurrent first-touches of the same file.
	// jsast.Parse (internal/jsast, XM.17) has no singleflight equivalent —
	// two frameworks racing a cold JS file can both parse it once before
	// either's result is cached, which is a bounded perf cost (loses the
	// race, not correctness: JS's *sitter.Node has no Close to double-free).
	// Ruby's rubyParseSF is load-bearing in a way that tolerance isn't: two
	// frameworks racing on a cold cache would each parse+store
	// independently, and the *second* store's "close the entry I'm
	// replacing" line would tree.Close() the *first* parse's tree while the
	// first framework might still be actively navigating the very Go *Node
	// objects backed by that now-freed C tree — a use-after-close, not
	// merely a lost update.
	rubyParseSF singleflight.Group
)

func cachedParseRuby(file string, parse func(string) ([]byte, *sitter.Node, *sitter.Tree, bool)) (src []byte, root *sitter.Node, release func(), ok bool) {
	key, statOK := statCacheKey(file)
	if statOK {
		rubyParseMu.Lock()
		if e, found := rubyParseCache[file]; found && e.key == key {
			rubyParseMu.Unlock()
			return e.src, e.root, func() {}, e.ok
		}
		rubyParseMu.Unlock()
	}

	if !statOK {
		// Couldn't stat (race, or the file vanished between the caller
		// resolving this path and us reaching it) — don't cache or dedupe
		// via singleflight: a shared tree handed to more than one caller
		// here would hand out more than one caller-owned release(), and the
		// first Close() would leave every other holder with a use-after-
		// close. This path can never be validated as fresh later anyway, so
		// each caller parses (and owns/closes) its own independent tree,
		// exactly as rdReadAndParseRuby always did before this cache
		// existed.
		src, root, tree, ok := parse(file)
		if tree != nil {
			return src, root, func() { tree.Close() }, ok
		}
		return src, root, func() {}, ok
	}

	v, _, _ := rubyParseSF.Do(file, func() (any, error) {
		src, root, tree, ok := parse(file)
		WarmParseTree(root)
		rubyParseMu.Lock()
		if old, found := rubyParseCache[file]; found && old.tree != nil {
			old.tree.Close()
		}
		e := &rubyParseEntry{key: key, src: src, root: root, tree: tree, ok: ok}
		rubyParseCache[file] = e
		rubyParseMu.Unlock()
		return e, nil
	})
	e := v.(*rubyParseEntry)
	return e.src, e.root, func() {}, e.ok
}
