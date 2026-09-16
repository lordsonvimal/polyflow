package factpipe

import (
	"os"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
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

	src, root, ok = parse(file)
	if statOK {
		jsParseMu.Lock()
		jsParseCache[file] = &jsParseEntry{key: key, src: src, root: root, ok: ok}
		jsParseMu.Unlock()
	}
	return src, root, ok
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

	var tree *sitter.Tree
	src, root, tree, ok = parse(file)
	if !statOK {
		// Couldn't stat (race, or the file vanished between the caller
		// resolving this path and us reaching it) — don't cache an entry we
		// could never validate later; caller owns this tree's lifetime as
		// rdReadAndParseRuby always did before this cache existed.
		if tree != nil {
			return src, root, func() { tree.Close() }, ok
		}
		return src, root, func() {}, ok
	}

	rubyParseMu.Lock()
	if old, found := rubyParseCache[file]; found && old.tree != nil {
		old.tree.Close()
	}
	rubyParseCache[file] = &rubyParseEntry{key: key, src: src, root: root, tree: tree, ok: ok}
	rubyParseMu.Unlock()
	return src, root, func() {}, ok
}
