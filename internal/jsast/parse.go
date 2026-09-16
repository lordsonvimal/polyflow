// Package jsast is the shared JS/TS tree-sitter parsing substrate used by
// both internal/linker's hand-written link passes and internal/factpipe's
// hub providers.
//
// It exists to break an import cycle: internal/linker imports
// internal/patterns, and internal/patterns imports internal/factpipe (its
// extract/MatchToFacts pipeline constructs factpipe.Fact/Atom values
// directly), so internal/factpipe cannot import internal/linker without
// creating factpipe -> linker -> patterns -> factpipe. jsast has no
// dependency on any of the three, so both sides can import it instead of one
// duplicating the other's parsing/backtracking logic.
package jsast

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	tssitter "github.com/smacker/go-tree-sitter/typescript/typescript"
	tsxsitter "github.com/smacker/go-tree-sitter/typescript/tsx"
)

// IsJSFile reports whether file's extension is a JS/TS/JSX/TSX variant this
// package knows how to parse.
func IsJSFile(file string) bool {
	ext := strings.ToLower(filepath.Ext(file))
	return ext == ".ts" || ext == ".tsx" || ext == ".js" || ext == ".jsx" || ext == ".mjs" || ext == ".es6"
}

// IsTestFile reports whether rel/abs names a JS test/spec/mock file, by the
// conventional path segments a JS toolchain treats as test-only.
func IsTestFile(path string) bool {
	for _, s := range []string{"__tests__/", "__mocks__/", "/spec/", ".test.", ".spec.", "-test.", "-spec.", "/test-support/"} {
		if strings.Contains(path, s) {
			return true
		}
	}
	return false
}

// tsLang/tsxLang are process-wide singletons: GetLanguage() allocates a new Go
// wrapper around the same static C grammar on every call, but a *sitter.Query
// is compiled against the wrapper's identity, so reusing one wrapper per
// grammar is what makes any pointer-keyed query cache actually hit.
var (
	tsLang  = tssitter.GetLanguage()
	tsxLang = tsxsitter.GetLanguage()
)

// GrammarLangForFile returns the tree-sitter grammar file's extension parses
// with — the tsx grammar for .tsx/.jsx, the typescript grammar otherwise (a
// superset of plain JS).
func GrammarLangForFile(file string) *sitter.Language {
	ext := strings.ToLower(filepath.Ext(file))
	switch ext {
	case ".tsx", ".jsx":
		return tsxLang
	default:
		return tsLang
	}
}

// JS/TS tree-sitter parse cache.
//
// Several independent passes each re-read and re-parse every .js/.ts/.tsx
// file in the workspace. EnableCache turns on process-wide memoization for a
// link phase; Parse then parses each file once. Unlike a Ruby-side cache
// there is nothing to release: sitter.ParseCtx returns a *Node whose tree is
// pinned by a finalizer, so DisableCache just drops the map and lets GC
// reclaim it.

type parsedJS struct {
	src  []byte
	root *sitter.Node
	lang *sitter.Language
}

var (
	cacheMu sync.Mutex
	// cache is nil unless a link phase called EnableCache. A present-but-nil
	// value means "parse failed" — cached so a broken file isn't retried once
	// per pass.
	cache map[string]*parsedJS
)

// EnableCache starts memoizing JS/TS parses. Pair with DisableCache (safe via
// defer). Not re-entrant.
func EnableCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cache = make(map[string]*parsedJS)
}

// DisableCache clears the cache.
func DisableCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cache = nil
}

// Parse reads + parses file with its grammar (GrammarLangForFile), memoized
// for the link phase. ok is false when the file can't be read or parsed, or
// its extension has no grammar.
func Parse(file string) (src []byte, root *sitter.Node, lang *sitter.Language, ok bool) {
	cacheMu.Lock()
	c := cache
	if c != nil {
		if pj, seen := c[file]; seen {
			cacheMu.Unlock()
			if pj == nil {
				return nil, nil, nil, false
			}
			return pj.src, pj.root, pj.lang, true
		}
	}
	cacheMu.Unlock()

	pj := parseFile(file)

	if c != nil {
		cacheMu.Lock()
		if existing, seen := c[file]; seen {
			cacheMu.Unlock()
			pj = existing
		} else {
			c[file] = pj
			cacheMu.Unlock()
		}
	}
	if pj == nil {
		return nil, nil, nil, false
	}
	return pj.src, pj.root, pj.lang, true
}

func parseFile(file string) *parsedJS {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	lang := GrammarLangForFile(file)
	if lang == nil {
		return nil
	}
	root, err := sitter.ParseCtx(context.Background(), src, lang)
	if err != nil || root == nil {
		return nil
	}
	return &parsedJS{src: src, root: root, lang: lang}
}
