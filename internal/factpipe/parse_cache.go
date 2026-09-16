package factpipe

import (
	"os"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	"golang.org/x/sync/singleflight"
)

// XM.14 (docs/factpipe-cross-framework-matching-plan.md): pjcParseJS and
// rdReadAndParseRuby each re-read + re-parse a file from scratch on every
// call, with no memoization. js_hoc/js_client_routes/js_mobx/
// pusher_js_consumer all share pjcParseJS, and js_mobx alone calls it from 4
// separate per-file loops — every JS file in a service got parsed up to 4x
// in one hub call before caching, with more redundancy across hubs on top of
// that. Real cedar profile: runtime.cgocall was 46% of total index CPU,
// tree-sitter ParseCtx 15.56% cum, with jhcParseJS/pjcParseJS alone 7.35%.
//
// The cache key is (path, size, mtime), not path alone: these hub functions
// run inside both the one-shot `index` CLI and the long-running `serve`
// daemon, and `serve` reindexes the same paths repeatedly as files change —
// a path-only cache would silently serve a stale AST after an edit.
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

// jsParseCache memoizes pjcParseJS. go-tree-sitter's package-level ParseCtx
// (bindings.go) registers a runtime.SetFinalizer on the underlying tree, not
// an explicit Close contract — pjcParseJS never returned a release func, so
// holding onto the returned *sitter.Node here for reuse is a pure win with no
// lifetime to manage: the finalizer only runs once nothing (including this
// cache) still references the node.
type jsParseEntry struct {
	key  fileCacheKey
	src  []byte
	root *sitter.Node
	ok   bool
}

var (
	jsParseMu    sync.Mutex
	jsParseCache = map[string]*jsParseEntry{}
	// jsParseSF (XM.5) dedupes concurrent first-touches of the same file:
	// without it, two frameworks racing on a cold cache would each parse
	// independently and the second's cache-store would be a lost update
	// (benign here since JS has no Close to double-free), but the two
	// *sitter.Node results would be different Go objects sharing no
	// WarmParseTree — silently reintroducing the very race this cache
	// exists to prevent for whichever framework got the losing copy.
	jsParseSF singleflight.Group
)

func cachedParseJS(file string, parse func(string) ([]byte, *sitter.Node, bool)) (src []byte, root *sitter.Node, ok bool) {
	key, statOK := statCacheKey(file)
	if statOK {
		jsParseMu.Lock()
		if e, found := jsParseCache[file]; found && e.key == key {
			jsParseMu.Unlock()
			return e.src, e.root, e.ok
		}
		jsParseMu.Unlock()
	}

	v, _, _ := jsParseSF.Do(file, func() (any, error) {
		src, root, ok := parse(file)
		WarmParseTree(root)
		e := &jsParseEntry{key: key, src: src, root: root, ok: ok}
		if statOK {
			jsParseMu.Lock()
			jsParseCache[file] = e
			jsParseMu.Unlock()
		}
		return e, nil
	})
	e := v.(*jsParseEntry)
	return e.src, e.root, e.ok
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
	// rubyParseSF (XM.5): same concurrent-first-touch dedup as jsParseSF,
	// but load-bearing here in a way JS's cache isn't — without it, two
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
