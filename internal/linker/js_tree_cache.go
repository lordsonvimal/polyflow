package linker

import (
	"context"
	"os"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
)

// JS/TS tree-sitter parse cache.
//
// The JS link passes each re-read and re-parse every .js/.ts/.tsx file in the
// workspace: resolveImportCalls (js_link), resolveJSTypeRelations +
// findDefaultExportClassName (js_type_relations), resolveJSReceiverTypeCalls,
// scanJSWrapperCallSites + discoverJSTransitiveWrappers (js_api_wrapper_calls),
// the js_lazy_import_calls loop, parseJSHostFile (js_http_hosts) and
// parseJSImportSources (js_import_edges) — 8+ full grammar parses of the same
// tree on a frontend-heavy repo.
//
// EnableJSTreeCache turns on process-wide memoization for a link phase;
// jsParse then parses each file once. Unlike rubyParse there is nothing to
// release: sitter.ParseCtx returns a *Node whose tree is pinned by a
// finalizer, so DisableJSTreeCache just drops the map and lets GC reclaim it.

type parsedJS struct {
	src  []byte
	root *sitter.Node
	lang *sitter.Language
}

var (
	jsCacheMu sync.Mutex
	// jsCache is nil unless a link phase called EnableJSTreeCache. A
	// present-but-nil value means "parse failed" — cached so a broken file
	// isn't retried once per pass.
	jsCache map[string]*parsedJS
)

// EnableJSTreeCache starts memoizing JS/TS parses. Pair with
// DisableJSTreeCache (safe via defer). Not re-entrant.
func EnableJSTreeCache() {
	jsCacheMu.Lock()
	defer jsCacheMu.Unlock()
	jsCache = make(map[string]*parsedJS)
}

// DisableJSTreeCache clears the cache.
func DisableJSTreeCache() {
	jsCacheMu.Lock()
	defer jsCacheMu.Unlock()
	jsCache = nil
}

// jsParse reads + parses file with its grammar (grammarLangForFile), memoized
// for the link phase. ok is false when the file can't be read or parsed, or
// its extension has no grammar.
func jsParse(file string) (src []byte, root *sitter.Node, lang *sitter.Language, ok bool) {
	jsCacheMu.Lock()
	cache := jsCache
	if cache != nil {
		if pj, seen := cache[file]; seen {
			jsCacheMu.Unlock()
			if pj == nil {
				return nil, nil, nil, false
			}
			return pj.src, pj.root, pj.lang, true
		}
	}
	jsCacheMu.Unlock()

	pj := parseJSFile(file)

	if cache != nil {
		jsCacheMu.Lock()
		if existing, seen := cache[file]; seen {
			jsCacheMu.Unlock()
			pj = existing
		} else {
			cache[file] = pj
			jsCacheMu.Unlock()
		}
	}
	if pj == nil {
		return nil, nil, nil, false
	}
	return pj.src, pj.root, pj.lang, true
}

func parseJSFile(file string) *parsedJS {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	lang := grammarLangForFile(file)
	if lang == nil {
		return nil
	}
	root, err := sitter.ParseCtx(context.Background(), src, lang)
	if err != nil || root == nil {
		return nil
	}
	return &parsedJS{src: src, root: root, lang: lang}
}
