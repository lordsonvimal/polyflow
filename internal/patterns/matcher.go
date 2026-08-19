package patterns

import (
	"context"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	gositter "github.com/smacker/go-tree-sitter/golang"
	htmlsitter "github.com/smacker/go-tree-sitter/html"
	jssitter "github.com/smacker/go-tree-sitter/javascript"
	pythonsitter "github.com/smacker/go-tree-sitter/python"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"
	tsxsitter "github.com/smacker/go-tree-sitter/typescript/tsx"
	tssitter "github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// MatchResult holds a single pattern match against source code.
type MatchResult struct {
	PatternName string
	NodeID      string
	Captures    map[string]string // capture name -> matched text
	Line        int
	EndLine     int // declaration body end (from @_-prefixed span captures; 0 = unknown)
	File        string

	// Set for matches from version-gated patterns: which package the pattern
	// targets and the service's resolved version of it.
	Package         string
	ResolvedVersion string

	// IsTestDSL is true when the match's enclosing construct is a test-harness
	// call/function (X.0). Comm classifiers (http_client/publisher/subscriber)
	// consult this to avoid minting a communication node from test/scenario code.
	IsTestDSL bool

	// KeyNodes retains the tree-sitter node for a bounded allow-list of
	// producer/consumer key captures (X.1a wiring): url, path, channel,
	// exchange, routing_key, topic, queue, queue_name, key, event.
	// MatchToGraph routes these through the match's language KeyWalker
	// instead of the already-flattened capture text, so dynamic key
	// expressions (fmt.Sprintf, template interpolation, string concatenation)
	// are recognized instead of becoming unmatchable garbage keys. Bounded so
	// unrelated captures (payload, handler, method, ...) never retain nodes.
	KeyNodes map[string]*sitter.Node

	// Src is the source bytes this match was parsed from (shares the
	// caller's backing array — no copy). Required by KeyWalker.WalkKey,
	// which re-derives text from node byte offsets.
	Src []byte

	// Lang is the contract.KeyWalker registry language for this match's
	// grammar (e.g. "javascript" for js/ts/tsx/jsx), or "" when the grammar
	// has no walker family.
	Lang string
}

// keyWalkerKeyCaptureNames is the bounded allow-list of capture names whose
// tree-sitter node is retained on MatchResult.KeyNodes for X.1a WalkKey
// routing — restricted to names that are an actual contract-rule `key:`
// field somewhere in contracts/*.yaml today (verified against every
// go/javascript/ruby pattern file, 2026-07-26). "event" is deliberately
// excluded despite appearing in the phase doc's allow-list text: no contract
// rule keys on it (websocket.yaml keys on message_type; hub.yaml's key is
// `[]`, matching unconditionally), and sse_hub.yaml's hub_broadcast_call
// captures @event for an arbitrary Go value (often a composite literal, not
// a channel/topic-like key) — routing it through WalkKey marked every hub
// producer key_dynamic and wiped out hub_broadcast fan-out entirely
// (caught by TestGoldenChessleapParity). A capture name being routable here
// is necessary but not sufficient for correctness — it must also be a real
// match key for the rule that consumes it; "event" failed that bar.
//
// "key" is deliberately excluded too, despite the phase doc's allow-list
// text: it is a widespread *predicate-only* capture-name convention across
// the pattern corpus (`key: (property_identifier) @key (#eq? @key
// "baseURL")` — checking an object literal's property name, not carrying a
// producer key value) used by gorilla_websocket, kafka, axios_instance,
// fetch, producer_alias, and websocket.yaml's ws_send_typed — none of them
// declare "key" in their own captures: list. Routing ws_send_typed's stray
// @key node (a bare property_identifier, always non-literal to the walker)
// wrongly marked it key_dynamic and deleted every ws_send edge from the
// fixture corpus (caught by TestFixtureEdgeTypes_Snapshot).
var keyWalkerKeyCaptureNames = map[string]bool{
	"url": true, "path": true, "channel": true, "exchange": true,
	"routing_key": true, "topic": true, "queue": true, "queue_name": true,
}

// keyWalkerRoutedLangs restricts X.1a's live WalkKey routing to the
// languages the phase actually extends (go, javascript, ruby) rather than
// every registered contract.KeyWalker — see the routing-block comment for
// why python's placeholder walker must not be routed live.
var keyWalkerRoutedLangs = map[string]bool{
	"go": true, "javascript": true, "ruby": true,
}

// keyWalkerLangFor maps a grammar language to the contract.KeyWalker
// registry language, normalizing the JS family (javascript/typescript/tsx)
// to "javascript" the same way testDSLLangFamily does; other grammars
// (go, ruby, python, html) already register under their own name.
func keyWalkerLangFor(grammarLang string) string {
	if fam := testDSLLangFamily(grammarLang); fam != "" {
		return fam
	}
	return grammarLang
}

// compiledQuery holds a compiled tree-sitter query and the original pattern.
type compiledQuery struct {
	query   *sitter.Query
	pattern *Pattern
}

// TreeSitterMatcher runs tree-sitter queries against source files.
type TreeSitterMatcher struct {
	registry *Registry
	versions map[string]string // package -> resolved version (for match metadata)
	mu       sync.Mutex
	// compiled queries cached per language: language -> patternName -> compiledQuery
	compiled map[string][]compiledQuery

	// DatastarVariant is the toolchain RuleVariant for the resolved datastar version
	// (e.g. "datastar-v1"). Set by the indexer; read by the templ parser to select
	// the correct attribute-key vocabulary. Empty → combined/backward-compat fallback.
	DatastarVariant string
}

// NewTreeSitterMatcher creates a matcher backed by the given registry.
func NewTreeSitterMatcher(reg *Registry) *TreeSitterMatcher {
	return &TreeSitterMatcher{
		registry: reg,
		compiled: make(map[string][]compiledQuery),
	}
}

// NewTreeSitterMatcherForService creates a matcher whose pattern set is
// filtered by the service's resolved dependency versions, and whose matches
// carry package + resolved-version metadata.
func NewTreeSitterMatcherForService(reg *Registry, svcDeps []deps.Dependency) *TreeSitterMatcher {
	m := NewTreeSitterMatcher(reg.ForService(svcDeps))
	m.versions = ResolvedVersions(svcDeps)
	return m
}

// languageFor returns the tree-sitter Language for the given language string.
func languageFor(lang string) *sitter.Language {
	switch lang {
	case "go":
		return gositter.GetLanguage()
	case "javascript":
		return jssitter.GetLanguage()
	case "typescript":
		return tssitter.GetLanguage()
	case "tsx":
		return tsxsitter.GetLanguage()
	case "python":
		return pythonsitter.GetLanguage()
	case "ruby":
		return rubysitter.GetLanguage()
	case "html":
		return htmlsitter.GetLanguage()
	default:
		return nil
	}
}

// DetectJSGrammar upgrades a ".js"-family file's parsing grammar to
// "typescript" when the plain JavaScript grammar can't cleanly parse it.
//
// The plain "javascript" tree-sitter grammar (used by default for every
// .js/.mjs/.es6 file — see internal/parser/javascript.go's grammarLanguage)
// implements standard ECMAScript only: it has no notion of Flow or
// TypeScript type annotations. A Flow-typed .js file — common in codebases
// that predate first-class TS adoption, or that use Flow specifically
// instead of TS (Facebook/Meta-style tooling) — parses with real syntax
// errors around every annotated parameter and return type
// (`(url: string): Promise<Object> => ...`), and every pattern anchored on
// formal_parameters' exact child shape (arrow_func_var, the wrapper-body
// detection family, …) silently stops matching: the function registers as a
// bare `variable` node instead of `function`, and everything downstream —
// call resolution, HTTP client detection, impact/context — treats it as
// inert.
//
// defaultGrammar is grammarLanguage(file)'s result; this only ever upgrades
// away from "javascript" (a .ts/.tsx file already uses a type-aware grammar
// and is returned unchanged). The check costs one extra parse of the file,
// paid only for plain .js-family files — a one-time indexing cost, not per
// pattern — and returns "javascript" unchanged for the overwhelming common
// case (a real parse error is the signal, not a heuristic guess at Flow
// syntax, so a file that merely looks unusual but parses fine is untouched).
//
// TypeScript's grammar is the practical fallback rather than a dedicated
// Flow grammar: no tree-sitter-flow binding exists in this project's
// dependency tree, and TypeScript's syntax for ordinary parameter/return
// type annotations (`name: Type`, generics) is close enough to Flow's that
// the parser recovers cleanly for the common annotation shapes; Flow-only
// syntax (`?Type` nullable prefix, `%checks`) may still produce local error
// nodes, but tree-sitter's error recovery keeps the surrounding function
// structure — the part every pattern here actually anchors on — intact.
func DetectJSGrammar(file string, src []byte, defaultGrammar string) string {
	if defaultGrammar != "javascript" {
		return defaultGrammar
	}
	root, err := sitter.ParseCtx(context.Background(), src, jssitter.GetLanguage())
	if err != nil || root == nil {
		return defaultGrammar
	}
	if root.HasError() {
		return "typescript"
	}
	return defaultGrammar
}

// getCompiledQueries returns cached compiled queries for patternLang compiled against grammarLang.
// The cache key includes both so jsx patterns compiled against tsx grammar don't collide with
// the same patterns compiled against typescript grammar.
func (m *TreeSitterMatcher) getCompiledQueries(patternLang, grammarLang string, lang *sitter.Language) []compiledQuery {
	m.mu.Lock()
	defer m.mu.Unlock()

	cacheKey := patternLang + "@" + grammarLang
	if cqs, ok := m.compiled[cacheKey]; ok {
		return cqs
	}

	patterns := m.registry.List(patternLang)
	cqs := make([]compiledQuery, 0, len(patterns))
	for _, p := range patterns {
		if len(p.Grammars) > 0 && !slices.Contains(p.Grammars, grammarLang) {
			// Grammar-restricted pattern (e.g. a formal_parameters shape
			// that's only valid tree-sitter syntax in one grammar family) —
			// scoped out on purpose, not a compile failure worth logging.
			continue
		}
		q, err := sitter.NewQuery([]byte(p.Query), lang)
		if err != nil {
			log.Printf("patterns: failed to compile query %q for language %q against grammar %q: %v", p.Name, patternLang, grammarLang, err)
			continue
		}
		cqs = append(cqs, compiledQuery{query: q, pattern: p})
	}
	m.compiled[cacheKey] = cqs
	return cqs
}

// MatchWithGrammar runs patterns registered under patternLang but parses with the
// grammar for grammarLang. This lets TypeScript files use JavaScript patterns
// (fetch, axios) compiled against the TypeScript grammar, which is a superset.
func (m *TreeSitterMatcher) MatchWithGrammar(patternLang, grammarLang, file string, src []byte) ([]MatchResult, error) {
	lang := languageFor(grammarLang)
	if lang == nil {
		return nil, nil
	}
	cqs := m.getCompiledQueries(patternLang, grammarLang, lang)
	if len(cqs) == 0 {
		return nil, nil
	}
	root, err := sitter.ParseCtx(context.Background(), src, lang)
	if err != nil {
		return nil, fmt.Errorf("tree-sitter parse %s: %w", file, err)
	}
	return m.execQueries(cqs, root, src, file, grammarLang)
}

// Match runs registered patterns for the language against the source bytes.
func (m *TreeSitterMatcher) Match(language, file string, src []byte) ([]MatchResult, error) {
	lang := languageFor(language)
	if lang == nil {
		// unknown language: return empty results, not an error
		return nil, nil
	}

	cqs := m.getCompiledQueries(language, language, lang)
	if len(cqs) == 0 {
		return nil, nil
	}

	// Parse the source
	root, err := sitter.ParseCtx(context.Background(), src, lang)
	if err != nil {
		return nil, fmt.Errorf("tree-sitter parse %s: %w", file, err)
	}

	return m.execQueries(cqs, root, src, file, language)
}

// indexCwd is the canonicalized process working directory, resolved once —
// EvalSymlinks is a syscall, and callers must mirror internal/parser's
// canonicalPath (which resolves both sides) so paths under a symlinked cwd
// (e.g. macOS /var -> /private/var) still match.
var indexCwd = sync.OnceValue(func() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		return resolved
	}
	return cwd
})

// RelativizeToCwd converts an absolute file path to one relative to the
// indexing process's cwd, matching the convention internal/parser's
// semantic passes already use for struct/interface/variable nodes. Falls
// back to the input unchanged if it's not absolute, or resolves outside cwd.
func RelativizeToCwd(file string) string {
	cwd := indexCwd()
	if cwd == "" || !filepath.IsAbs(file) {
		return file
	}
	canon := file
	if resolved, err := filepath.EvalSymlinks(file); err == nil {
		canon = resolved
	}
	rel, err := filepath.Rel(cwd, canon)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return file
	}
	return filepath.ToSlash(rel)
}

func (m *TreeSitterMatcher) execQueries(cqs []compiledQuery, root *sitter.Node, src []byte, file, grammarLang string) ([]MatchResult, error) {
	var results []MatchResult
	testDSLFamily := testDSLLangFamily(grammarLang)
	// `file` arrives absolute (the indexer walks services from an absolute
	// root so os.ReadFile works regardless of process cwd). Every other node
	// producer (the Go semantic/SSA pass in internal/parser) stores paths
	// relative to the indexing cwd, so an unconverted absolute path here
	// desyncs this node's File/ID from the rest of the graph — and the
	// frontend's /api/tree builds folders by splitting File on "/", so an
	// absolute path mints a phantom "Users" -> "<username>" -> ... subtree
	// at the workspace root.
	file = RelativizeToCwd(file)

	for _, cq := range cqs {
		cursor := sitter.NewQueryCursor()
		cursor.Exec(cq.query, root)

		for {
			m2, ok := cursor.NextMatch()
			if !ok {
				break
			}
			// Apply predicate filtering (handles #eq? and #match? predicates)
			m2 = cursor.FilterPredicates(m2, src)
			if m2 == nil || len(m2.Captures) == 0 {
				continue
			}

			// A comment between two arguments is a named sibling, so it can bind
			// to an anchored `(_)` capture and shift every later capture by one.
			// Re-align before any capture text is read.
			matchCaps, ok2 := repairCommentCaptures(m2.Captures, cq.query.CaptureNameForId)
			if !ok2 {
				continue
			}

			// Build capture map: capture name -> text.
			// Captures whose name starts with "_" are positional only: they
			// contribute line-range information (e.g. @_def spanning a whole
			// function body) but their text is not stored, so declaration
			// bodies never leak into node meta.
			captures := make(map[string]string, len(matchCaps))
			var keyNodes map[string]*sitter.Node
			var minLine int = -1
			var minLineNamed int = -1
			var defEndLine int
			var anchor *sitter.Node
			for _, cap := range matchCaps {
				if anchor == nil {
					anchor = cap.Node
				}
				name := cq.query.CaptureNameForId(cap.Index)
				row := int(cap.Node.StartPoint().Row) + 1 // 1-indexed
				if strings.HasPrefix(name, "_") {
					// Positional-only capture: it marks the span of the whole
					// declaration, so its end row bounds the definition body.
					if endRow := int(cap.Node.EndPoint().Row) + 1; endRow > defEndLine {
						defEndLine = endRow
					}
				} else {
					captures[name] = cap.Node.Content(src)
					if keyWalkerKeyCaptureNames[name] {
						if keyNodes == nil {
							keyNodes = make(map[string]*sitter.Node, 1)
						}
						keyNodes[name] = cap.Node
					}
					// Prefer the line of a real (non-positional) capture: an
					// underscore-prefixed anchor can sit on a different line
					// (e.g. a `member do` block-opening keyword) than the
					// actual matched content (e.g. a verb call several lines
					// into the block), which would otherwise collapse every
					// match in the block onto the anchor's line and collide
					// their node IDs.
					if minLineNamed < 0 || row < minLineNamed {
						minLineNamed = row
					}
				}
				if minLine < 0 || row < minLine {
					minLine = row
				}
			}
			if minLineNamed >= 0 {
				minLine = minLineNamed
			}

			// Apply Match filters if defined
			if len(cq.pattern.Match) > 0 {
				skip := false
				for capName, allowed := range cq.pattern.Match {
					val, ok := captures[capName]
					if !ok {
						skip = true
						break
					}
					if !slices.Contains(allowed, val) {
						skip = true
						break
					}
				}
				if skip {
					continue
				}
			}

			if minLine < 0 {
				minLine = 0
			}

			// X.2: delayed_job's `.delay`/`handle_asynchronously` sites have an
			// implicit-self receiver — the join target is the enclosing class,
			// not text in the match itself. Capture it here where the AST is
			// still available (MatchToGraph only sees flat capture text).
			if grammarLang == "ruby" && anchor != nil &&
				(cq.pattern.Name == "dj_delay" || cq.pattern.Name == "dj_handle_asynchronously") {
				if cls := rubyEnclosingClassName(anchor, src); cls != "" {
					captures["dj_class"] = cls
				}
			}

			// jquery_ajax_options_typed's @method captures a string/property-key
			// value node directly (`type: "POST"`) rather than routing through
			// the KeyWalker like @url does, so it keeps its raw quoted text
			// ("POST" with the quote characters) unless stripped here — and an
			// unstripped value never matches contracts/http.yaml's bare "POST"
			// route meta, defeating the whole point of capturing it.
			if cq.pattern.Name == "jquery_ajax_options_typed" {
				if v, ok := captures["method"]; ok {
					captures["method"] = strings.ToUpper(strings.Trim(v, `"'`+"`"))
				}
			}

			// WB.4: these two patterns only capture the call site + argument
			// identifier (tree-sitter queries can't do arithmetic to derive a
			// positional index). Walk up from @arg_name to the nearest enclosing
			// function and check whether the identifier is genuinely one of its
			// own parameters — if so inject wrapper_name/param_index; if not
			// (an ordinary local variable passed to fetch/axios, not a forwarded
			// param) drop the match entirely so no bookkeeping node is created.
			if cq.pattern.Name == "wrapper_url_positional_fetch_call" || cq.pattern.Name == "wrapper_url_positional_axios_call" ||
				cq.pattern.Name == "wrapper_url_key_axios_config_call" || cq.pattern.Name == "wrapper_url_shorthand_axios_config_call" {
				var argNode *sitter.Node
				for _, cap := range matchCaps {
					if cq.query.CaptureNameForId(cap.Index) == "arg_name" {
						argNode = cap.Node
						break
					}
				}
				if argNode == nil {
					continue
				}
				wname, idx, ok := jsWrapperParamIndex(argNode, src)
				if !ok {
					continue
				}
				captures["wrapper_name"] = wname
				captures["param_index"] = strconv.Itoa(idx)
			}

			// WB.4: producer_alias_url_call no longer anchors @url to the first
			// argument (a wrapper's URL param may forward at any position), so
			// a call site with multiple string/template literal args now emits
			// one match per literal. The linker (EnrichAliases) needs each
			// candidate's positional index to pick the right one via
			// wrapperURLTable, which tree-sitter can't emit directly.
			if cq.pattern.Name == "producer_alias_url_call" {
				var urlNode *sitter.Node
				for _, cap := range matchCaps {
					if cq.query.CaptureNameForId(cap.Index) == "url" {
						urlNode = cap.Node
						break
					}
				}
				if urlNode != nil && urlNode.Parent() != nil {
					captures["arg_index"] = strconv.Itoa(namedChildIndex(urlNode.Parent(), urlNode))
				}
			}

			mr := MatchResult{
				PatternName: cq.pattern.Name,
				Captures:    captures,
				Line:        minLine,
				EndLine:     defEndLine,
				File:        file,
				IsTestDSL:   testDSLFamily != "" && anchor != nil && inTestDSLScope(anchor, src, testDSLFamily),
				KeyNodes:    keyNodes,
				Src:         src,
				Lang:        keyWalkerLangFor(grammarLang),
			}
			if cq.pattern.Package != "" {
				mr.Package = cq.pattern.Package
				mr.ResolvedVersion = m.versions[cq.pattern.Package]
			}
			results = append(results, mr)
		}
	}

	return results, nil
}

// MatchToNodes converts raw match results into typed graph nodes and edges.
func (m *TreeSitterMatcher) MatchToNodes(service string, results []MatchResult) ([]graph.Node, []graph.Edge) {
	nodes, edges, _ := MatchToGraph(service, results)
	return nodes, edges
}

// jsBuiltins are host/runtime globals that legitimately resolve to nothing:
// call refs to them are not graph blind spots and stay out of the ledger.
var jsBuiltins = map[string]bool{
	"alert": true, "atob": true, "btoa": true, "clearInterval": true,
	"clearTimeout": true, "confirm": true, "decodeURIComponent": true,
	"encodeURIComponent": true, "fetch": true, "isFinite": true, "isNaN": true,
	"parseFloat": true, "parseInt": true, "prompt": true, "queueMicrotask": true,
	"requestAnimationFrame": true, "setInterval": true, "setTimeout": true,
	"structuredClone": true,
}

// isCallRef returns true for pattern results that represent a call-site reference
// rather than a definition. These do not create nodes; instead they emit edges
// from the enclosing function to the target function by name.
func isCallRef(patternName string) bool {
	return patternName == "component_fn_call" ||
		patternName == "jsx_event_handler_ref" ||
		patternName == "goroutine_call" ||
		patternName == "cobra_run" ||
		patternName == "python_func_call"
}

// isConstantPattern returns true for pattern results that only feed the URL
// constant-propagation table and never become graph nodes: literal const
// declarations and URL-builder helpers that return a literal.
func isConstantPattern(patternName string) bool {
	return patternName == "const_string" ||
		patternName == "const_template_prefix" ||
		patternName == "fn_return_string" ||
		patternName == "fn_return_template_prefix" ||
		patternName == "const_object_member"
}

// testDSLCallers is the recognized test-harness vocabulary, per language
// family (X.0). Validated against real repos (bug-class #7): Playwright/
// Jest/Mocha, RSpec, Go testing. Go is handled structurally (see
// goInTestDSLScope) since its harness is a naming convention, not a callee.
var testDSLCallers = map[string]map[string]bool{
	"javascript": {
		"test": true, "it": true, "describe": true, "context": true,
		"beforeEach": true, "afterEach": true, "beforeAll": true, "afterAll": true,
	},
	"ruby": {
		"describe": true, "it": true, "context": true, "before": true,
		"after": true, "let": true, "subject": true, "specify": true, "scenario": true,
	},
	"go": {},
}

// testDSLWalkDepth bounds the ancestor walk in inTestDSLScope so pathological
// trees can't cause unbounded work (bug-class #2: deterministic, bounded).
const testDSLWalkDepth = 64

// goTestFuncNameRe matches Go test/benchmark/example/fuzz entry-point names.
var goTestFuncNameRe = regexp.MustCompile(`^(Test|Benchmark|Example|Fuzz)`)

// testDSLLangFamily maps a grammar language to the vocabulary/dispatch family
// used by inTestDSLScope. TypeScript/TSX share the JavaScript call_expression
// shape, so they resolve to "javascript". Languages with no test-DSL rule
// (python, html) return "" and are never scoped.
func testDSLLangFamily(grammarLang string) string {
	switch grammarLang {
	case "javascript", "typescript", "tsx":
		return "javascript"
	case "ruby":
		return "ruby"
	case "go":
		return "go"
	default:
		return ""
	}
}

// inTestDSLScope reports whether n's enclosing construct is a test-harness
// call/function. Walks up the tree-sitter ancestor chain to the nearest
// matching call/function-declaration node; depth-bounded to avoid
// pathological trees.
func inTestDSLScope(n *sitter.Node, src []byte, family string) bool {
	switch family {
	case "javascript":
		return jsInTestDSLScope(n, src)
	case "ruby":
		return rubyInTestDSLScope(n, src)
	case "go":
		return goInTestDSLScope(n, src)
	default:
		return false
	}
}

// jsInTestDSLScope walks up to the nearest enclosing call_expression whose
// callee is a recognized test-harness function (test/it/describe/…).
func jsInTestDSLScope(n *sitter.Node, src []byte) bool {
	cur := n
	for depth := 0; cur != nil && depth < testDSLWalkDepth; depth, cur = depth+1, cur.Parent() {
		if cur.Type() != "call_expression" {
			continue
		}
		fn := cur.ChildByFieldName("function")
		if fn == nil {
			continue
		}
		if testDSLCallers["javascript"][fn.Content(src)] {
			return true
		}
	}
	return false
}

// rubyInTestDSLScope walks up to the nearest enclosing `call` node whose
// method name is a recognized RSpec/Minitest DSL verb (it/describe/before/…).
func rubyInTestDSLScope(n *sitter.Node, src []byte) bool {
	cur := n
	for depth := 0; cur != nil && depth < testDSLWalkDepth; depth, cur = depth+1, cur.Parent() {
		if cur.Type() != "call" {
			continue
		}
		method := cur.ChildByFieldName("method")
		if method == nil {
			continue
		}
		if testDSLCallers["ruby"][method.Content(src)] {
			return true
		}
	}
	return false
}

// rubyEnclosingClassName walks up to the nearest enclosing class/module
// declaration and returns its name (last segment only for a namespaced
// scope_resolution name, e.g. "Admin::ReportJob" -> "ReportJob"), or "" if
// n is not nested inside one. Used by X.2 to resolve the implicit-self
// receiver of `handle_asynchronously` and `self.delay.method` call sites.
func rubyEnclosingClassName(n *sitter.Node, src []byte) string {
	cur := n
	for depth := 0; cur != nil && depth < testDSLWalkDepth; depth, cur = depth+1, cur.Parent() {
		if cur.Type() != "class" && cur.Type() != "module" {
			continue
		}
		nameNode := cur.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		if nameNode.Type() == "scope_resolution" {
			if last := nameNode.ChildByFieldName("name"); last != nil {
				return last.Content(src)
			}
			continue
		}
		return nameNode.Content(src)
	}
	return ""
}

// namedChildIndex returns target's position among parent's named children, or
// -1 if target is not a named child of parent. Tree-sitter queries can't emit
// a sibling's ordinal position directly, so callers needing an argument's or
// parameter's index (WB.4) compute it here from the already-matched node.
func namedChildIndex(parent, target *sitter.Node) int {
	for i := 0; i < int(parent.NamedChildCount()); i++ {
		if parent.NamedChild(i).StartByte() == target.StartByte() {
			return i
		}
	}
	return -1
}

// jsWrapperParamIndex walks up from a fetch/axios call's argument identifier
// to the nearest enclosing function_declaration/arrow_function/
// function_expression and reports whether that identifier is genuinely one of
// the function's own positional parameters — i.e. the call forwards a
// parameter into fetch/axios, not an ordinary local variable. Returns the
// wrapper's name and the parameter's 0-based index when so; ok is false
// otherwise (caller should not emit a wrapper fact).
func jsWrapperParamIndex(argNode *sitter.Node, src []byte) (wrapperName string, paramIndex int, ok bool) {
	argText := argNode.Content(src)
	cur := argNode.Parent()
	for depth := 0; cur != nil && depth < testDSLWalkDepth; depth, cur = depth+1, cur.Parent() {
		var params, name *sitter.Node
		switch cur.Type() {
		case "function_declaration":
			params = cur.ChildByFieldName("parameters")
			name = cur.ChildByFieldName("name")
		case "arrow_function", "function_expression":
			params = cur.ChildByFieldName("parameters")
			if decl := cur.Parent(); decl != nil && decl.Type() == "variable_declarator" {
				name = decl.ChildByFieldName("name")
			}
		default:
			continue
		}
		if params == nil || name == nil {
			return "", -1, false
		}
		for i := 0; i < int(params.NamedChildCount()); i++ {
			p := jsParamIdentifier(params.NamedChild(i))
			if p != nil && p.Content(src) == argText {
				return name.Content(src), i, true
			}
		}
		return "", -1, false
	}
	return "", -1, false
}

// jsParamIdentifier returns the plain identifier a formal_parameters child
// resolves to, unwrapping the typescript/tsx grammar's required_parameter
// (a type-annotated param, e.g. `url: string`) — which the plain javascript
// grammar has no equivalent for; a bare (identifier) is returned unchanged.
// Not handling this meant every WB.4 wrapper-call pattern silently stopped
// resolving as soon as DetectJSGrammar upgraded a Flow-typed .js file to the
// typescript grammar: the call site still matched, but this walk-up
// dropped it anyway because p.Type() was "required_parameter", never
// "identifier".
func jsParamIdentifier(p *sitter.Node) *sitter.Node {
	if p == nil {
		return nil
	}
	if p.Type() == "identifier" {
		return p
	}
	if p.Type() == "required_parameter" || p.Type() == "optional_parameter" {
		if inner := p.ChildByFieldName("pattern"); inner != nil && inner.Type() == "identifier" {
			return inner
		}
	}
	return nil
}

// goInTestDSLScope reports true when the nearest enclosing function_declaration
// name matches ^(Test|Benchmark|Example|Fuzz), or the site is an argument to a
// t.Run(...) subtest call. This is stricter than a filename check: helpers in
// _test.go files that are not test-DSL call sites are still indexed normally.
func goInTestDSLScope(n *sitter.Node, src []byte) bool {
	cur := n
	for depth := 0; cur != nil && depth < testDSLWalkDepth; depth, cur = depth+1, cur.Parent() {
		switch cur.Type() {
		case "function_declaration":
			if name := cur.ChildByFieldName("name"); name != nil && goTestFuncNameRe.MatchString(name.Content(src)) {
				return true
			}
		case "call_expression":
			fn := cur.ChildByFieldName("function")
			if fn == nil || fn.Type() != "selector_expression" {
				continue
			}
			if field := fn.ChildByFieldName("field"); field != nil && field.Content(src) == "Run" {
				return true
			}
		}
	}
	return false
}

// MatchToGraph maps match results to graph nodes and edges. The third return
// lists call references that resolved to nothing in-file — candidates for the
// unresolved-refs ledger (the JS import linker resolves some of them later;
// the indexer subtracts those before persisting).
// Node IDs follow the design doc format: service:file:type:name:line
func MatchToGraph(service string, results []MatchResult) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	nodes := make([]graph.Node, 0, len(results))
	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef

	// Separate call-reference results from definition results.
	var callRefs []MatchResult
	var defResults []MatchResult
	for _, r := range results {
		if isCallRef(r.PatternName) {
			callRefs = append(callRefs, r)
		} else {
			defResults = append(defResults, r)
		}
	}

	// Build per-file constant table from const_string / const_template_prefix
	// results, plus fn_return_* results (URL-builder helpers whose body returns
	// a literal or a template with a constant prefix, e.g. mermaidURL()).
	// file -> varName -> literalValue
	constants := make(map[string]map[string]string)
	for _, r := range defResults {
		// H.2: const_object_member carries three fields (obj/key/value), keyed
		// into the table as "obj.key" so the JS KeyWalker's member_expression
		// case can resolve `clientRoutes.home`-shaped producer keys.
		if r.PatternName == "const_object_member" {
			obj, okO := r.Captures["obj"]
			key, okK := r.Captures["key"]
			value, okV := r.Captures["value"]
			if okO && okK && okV {
				if constants[r.File] == nil {
					constants[r.File] = make(map[string]string)
				}
				constants[r.File][obj+"."+key] = stripStringLiteral(value)
			}
			continue
		}
		if !isConstantPattern(r.PatternName) {
			continue
		}
		name, okN := r.Captures["name"]
		value, okV := r.Captures["value"]
		if !okN || !okV {
			continue
		}
		if constants[r.File] == nil {
			constants[r.File] = make(map[string]string)
		}
		constants[r.File][name] = stripStringLiteral(value)
	}

	// Pass 1: build all nodes from definition results only.
	// Skip pure structural type declarations (TypeScript interfaces, type aliases, enums)
	// — they are not runtime entities and would add noise to the call graph.
	for _, r := range defResults {
		nodeType, edgeType := classifyPattern(r.PatternName)
		if nodeType == graph.NodeTypeInterface || nodeType == graph.NodeTypeTypeAlias {
			continue
		}

		// X.0: a comm-classified site (http_client/publisher/subscriber) whose
		// enclosing construct is a test-DSL call/function — OR whose file is a
		// test/spec file — is not a real communication endpoint; it's
		// test/scenario code. A `_test.go` http.NewRequest that isn't wrapped in
		// a recognised test-DSL construct (Go table tests, RSpec `it`) otherwise
		// slips through as a real producer and pollutes the cross-service
		// resolution denominator (measured: 32 of 134 unresolved-cross
		// http_call on the svc-c fleet came from `_test.go` files). Demote
		// it to an ordinary calls node (still indexed, so blast radius still
		// finds "which tests break") instead of minting a node the contract
		// engine and coverage denominators would treat as a real endpoint.
		demoteTestDSL := (r.IsTestDSL || graph.IsTestFilePath(r.File)) &&
			(nodeType == graph.NodeTypeHTTPClient ||
				nodeType == graph.NodeTypePublisher || nodeType == graph.NodeTypeSubscriber)
		if demoteTestDSL {
			nodeType = graph.NodeTypeFunction
		}
		// Constant declarations exist only to feed URL propagation (the
		// constants table above); emitting them as nodes floods the graph
		// with rootless "function" entries for every const in the codebase.
		if isConstantPattern(r.PatternName) {
			continue
		}

		// AWS client constructors (s3.NewFromConfig, bedrockruntime.NewFromConfig)
		// bind the client instance; they are not cloud calls. The operation
		// patterns match by method name + package gate, so the constructor node
		// is pure noise that shows up as a bogus PutObject/NewFromConfig cloud_call
		// edge (measured on svc-c: 6 of 22). Suppress it, like a constant.
		if isAWSClientConstructor(r.PatternName) {
			continue
		}

		// Build label from captures, preferring the most informative available field.
		label := r.PatternName
		if method, ok := r.Captures["method"]; ok {
			if url, ok2 := r.Captures["url"]; ok2 {
				label = fmt.Sprintf("%s %s", stripStringLiteral(method), stripStringLiteral(url))
			} else if path, ok2 := r.Captures["path"]; ok2 {
				label = fmt.Sprintf("%s %s", stripStringLiteral(method), stripStringLiteral(path))
			}
		} else if name, ok := r.Captures["name"]; ok {
			label = stripStringLiteral(name)
		} else if url, ok := r.Captures["url"]; ok {
			label = stripStringLiteral(url)
		} else if path, ok := r.Captures["path"]; ok {
			label = stripStringLiteral(path)
		} else if callee, ok := r.Captures["callee"]; ok {
			label = stripStringLiteral(callee)
		} else if verb, ok := r.Captures["verb"]; ok {
			// Rails member/collection route verbs (verb+action, no path
			// capture): label as "GET :action" so distinct verbs inside
			// the same block get distinct, readable node labels.
			if action, ok2 := r.Captures["action"]; ok2 {
				label = fmt.Sprintf("%s %s", strings.ToUpper(verb), stripStringLiteral(action))
			} else {
				label = strings.ToUpper(verb)
			}
		} else if fn, ok := r.Captures["fn"]; ok {
			// For goroutine fn captures: use the identifier only, not the full closure body.
			// If the captured fn spans multiple lines it's a func_literal — label it "func()".
			fnVal := r.Captures["fn"]
			if strings.ContainsAny(fnVal, "\n{") {
				label = "func()"
			} else {
				label = stripStringLiteral(fn)
			}
		} else if prop, ok := r.Captures["prop"]; ok && strings.HasPrefix(prop, "on") {
			// Event-handler assignments (es.onmessage = …, ws.onclose = …):
			// label with the property, not the internal pattern name.
			label = prop + " handler"
		} else if aliasN, ok := r.Captures["alias_name"]; ok {
			// G.7: alias/instance binding nodes — label with the variable name.
			label = stripStringLiteral(aliasN)
		} else if instN, ok := r.Captures["instance_name"]; ok {
			label = stripStringLiteral(instN)
		} else if wrapN, ok := r.Captures["wrapper_name"]; ok {
			// WB.1: wrapper-body bookkeeping nodes — label with the function name.
			label = stripStringLiteral(wrapN)
		} else if id, ok := r.Captures["id"]; ok {
			// HTML/JSX element id attribute — label as "#id" for CSS-selector readability.
			label = "#" + stripStringLiteral(id)
		} else if cls, ok := r.Captures["class"]; ok {
			// HTML/JSX element class attribute — label as ".first-class".
			label = "." + strings.SplitN(stripStringLiteral(cls), " ", 2)[0]
		}
		if r.PatternName == "goroutine_anon" {
			label = "go func()"
		}

		// ID format: service:file:type:name:line  (design doc §SQLite Schema)
		// Function/method/component nodes use the captured name so edges can target the same ID.
		// Element nodes are also named (by their id/class label) so selectors can address them.
		idName := r.PatternName
		namedTypes := nodeType == graph.NodeTypeFunction || nodeType == graph.NodeTypeMethod ||
			nodeType == graph.NodeTypeComponent || nodeType == graph.NodeTypeElement ||
			nodeType == graph.NodeTypeClass || nodeType == graph.NodeTypeStruct
		if namedTypes && label != r.PatternName {
			idName = label
		}
		nodeID := fmt.Sprintf("%s:%s:%s:%s:%d", service, r.File, string(nodeType), idName, r.Line)

		// Build meta from all captures
		meta := make(map[string]string, len(r.Captures))
		maps.Copy(meta, r.Captures)

		// Record the originating pattern so later passes (datastore linking,
		// boundary classification) can reason about the match without
		// re-deriving it.
		meta["pattern"] = r.PatternName

		// X.2: delayed_job wraps an existing method, so the job_enqueue/
		// job_perform join target is a qualified <Type>#<method> key, not a
		// job class. dj_target is left unset (and the contract rule falls
		// through to its unmatched:ledger policy) when the receiver type
		// can't be honestly determined — never guessed.
		if djTarget := delayedJobTarget(r); djTarget != "" {
			meta["dj_target"] = djTarget
		}

		if demoteTestDSL {
			meta[graph.MetaIsTest] = "true"
		}

		// External-service call sites: record which cloud service (derived
		// from the pattern-name prefix, e.g. s3_operation_v1 → s3).
		if nodeType == graph.NodeTypeExternalService {
			name := r.PatternName
			if i := strings.Index(name, "_"); i > 0 {
				meta["cloud_service"] = name[:i]
			}
		}

		// Datastore call sites: record whether this is a read or a write so
		// the linker can emit queries/persists edges to the service store node.
		if nodeType == graph.NodeTypeDatastore {
			meta["kind"] = "call"
			switch _, et := classifyPattern(r.PatternName); et {
			case graph.EdgeTypeQueries:
				meta["op"] = "query"
			case graph.EdgeTypePersists:
				meta["op"] = "persist"
			default:
				meta["op"] = "open"
			}
		}

		// Event-listener nodes (HTML onclick attrs, addEventListener,
		// el.onclick = …): stamp the bare event name so the dom_listen edge
		// and the UI can label the binding (Phase U.3).
		if nodeType == graph.NodeTypeDOMTarget {
			if _, et := classifyPattern(r.PatternName); et == graph.EdgeTypeDOMListen {
				if ev := eventNameFromCaptures(r.Captures); ev != "" {
					meta["event"] = ev
				}
			}
		}

		// SSE clients open a plain GET stream; stamp the method so the
		// cross-service linker can match the server's SSE endpoint.
		if r.PatternName == "eventsource_connect" {
			meta["method"] = "GET"
			meta["transport"] = "sse"
		}

		// H.1: `new WebSocketServer({server, ...})` / `{noServer: true}` have
		// no path in the constructor at all (the server attaches to an
		// existing HTTP server or handles the upgrade manually) — stamp
		// key_dynamic so the gap is visible rather than a silent keyless
		// node. NodeTypeSubscriber is a KeyWalker-routed type (X.1a), but the
		// walker block below only fires when a key capture exists in
		// r.KeyNodes; these two patterns capture no path/url/channel field,
		// so it never would otherwise.
		if r.PatternName == "ws_server_attached" || r.PatternName == "ws_server_noserver" {
			meta["key_dynamic"] = "true"
			meta["key_dynamic_raw"] = "(attached)"
		}

		// Navigation links (href/action in JSX or HTML): mark as nav_link so
		// the cross-service linker emits navigates_to instead of http_call.
		// Forms with method="post" (and data-method="delete" spoofing) carry
		// their verb; everything else defaults to GET (anchor navigation,
		// form default method).
		if strings.HasPrefix(r.PatternName, "nav_link") {
			meta["nav_link"] = "true"
			if m := stripStringLiteral(meta["method"]); m == "" {
				meta["method"] = "GET"
			} else {
				meta["method"] = strings.ToUpper(m)
				if p := stripStringLiteral(meta["path"]); p != "" {
					label = meta["method"] + " " + p
				}
			}
			// nav_link nodes with a helper reference (no literal path) must skip
			// the nav-path dedup (which keys on meta["path"]); mark as dynamic so
			// each call site is kept independently and resolved by the linker pass.
			if meta["helper"] != "" && meta["path"] == "" {
				meta["key_dynamic"] = "true"
			}
		}

		// X.1a: route producer/consumer key fields through the match's
		// language KeyWalker instead of raw capture text. Replaces the
		// JSX-only @branch_N/@key_expr convention (G.6) — which stamped
		// key_candidates/key_dynamic only for nav_link_jsx_ternary/
		// nav_link_jsx_dynamic — with the general mechanism so every
		// language/producer kind benefits, not just JSX nav links. A nil
		// walker (unregistered grammar) or a no-op walker declining a
		// genuinely-static capture leaves meta untouched — today's raw-text
		// behavior, no regression. Multiple dynamic key fields on one node
		// still yield a single whole-node key_dynamic (matching the engine's
		// per-node, not per-field, ledger check).
		//
		// Scoped to go/javascript/ruby (X.1's Files list) rather than "any
		// registered walker": the python walker is a placeholder that always
		// returns dynamic regardless of whether the node is a plain literal
		// (unlike go/js/ruby, which classify literals first) — routing
		// python through it here would wrongly dynamic-ledger every literal
		// requests.get("/path") call. Extending python is future scope.
		if keyWalkerRoutedLangs[r.Lang] && isKeyWalkerNode(nodeType) && len(r.KeyNodes) > 0 {
			if walker := contract.KeyWalkerFor(r.Lang); walker != nil {
				consts := constResolverFor(constants[r.File])
				dynamicHit := false
				candidateHit := false
				// Sorted, not map order: a node with two dynamic key fields
				// (an amqp_publish whose exchange *and* routing key are both
				// expressions) records only the first one in key_dynamic_raw,
				// so ranging the map made that meta value differ between
				// otherwise-identical indexes. Same for key_candidates, which
				// the last multi-candidate field wins.
				for _, field := range slices.Sorted(maps.Keys(r.KeyNodes)) {
					node := r.KeyNodes[field]
					cands, dynamic := walker.WalkKey(node, r.Src, consts)
					switch {
					case dynamic:
						meta[field] = ""
						if !dynamicHit {
							dynamicHit = true
							meta["key_dynamic"] = "true"
							meta["key_dynamic_raw"] = node.Content(r.Src)
						}
					case len(cands) == 1:
						meta[field] = cands[0]
					case len(cands) >= 2:
						meta["key_candidates"] = contract.MarshalKeyCandidates(cands)
						meta[field] = ""
						candidateHit = true
					}
				}
				switch {
				case dynamicHit:
					label = "dynamic"
				case candidateHit:
					label = "branch_enum"
				}
			}
		}

		// Tier 3 AMQP handshake fields: normalize the captured registration
		// field symbol (strip the leading `:` of a simple_symbol and the
		// trailing `:` of a hash_key_symbol) and surface it as the node label so
		// InferLinks can group producer/consumer sides on the shared token.
		if strings.HasPrefix(r.PatternName, "amqp_field") {
			field := strings.Trim(meta["broker_field"], ":")
			meta["broker_field"] = field
			if field != "" {
				label = field
			}
		}

		// RW.2: strip the leading `:` off the simple_symbol
		// wrapper_url_key_hash_index_ruby captures for `payload[:url]`'s hash
		// key, so internal/linker/ruby_wrapper_url_forward.go can compare it
		// directly against a `pair`'s hash_key_symbol text (which never
		// carries a colon) without every caller re-trimming it.
		if r.PatternName == "wrapper_url_key_hash_index_ruby" {
			meta["url_key"] = strings.TrimPrefix(meta["url_key"], ":")
		}

		// Version-gated patterns stamp which package version they matched
		// against, so the graph/UI can show e.g. "this call uses SDK v1".
		if r.Package != "" {
			meta["package"] = r.Package
			if r.ResolvedVersion != "" {
				meta["resolved_version"] = r.ResolvedVersion
			}
		}

		// Strip surrounding quotes from path, url, method, route-group prefix,
		// G.7 base-URL captures, and L.W2 selector/element captures. Selector
		// captures arrive as raw source (`'"#save-btn"'`); id and class values
		// from HTML/JSX attribute patterns similarly carry surrounding quotes.
		for _, key := range []string{"path", "url", "method", "prefix", "instance_base_url", "alias_base_url", "selector", "id", "class", "broker_field"} {
			if v, ok := meta[key]; ok {
				meta[key] = stripStringLiteral(v)
			}
		}

		// Declaration span: patterns that capture the whole definition (@_def,
		// @_body) record where the body ends. Persisted for all node types so
		// the G.3 route-group enrichment pass can read chi_route_group end lines.
		if r.EndLine >= r.Line {
			meta["end_line"] = fmt.Sprintf("%d", r.EndLine)
		}

		// PR.1: resolve constant/symbol HTTP verbs (`http.MethodGet`, `:get`)
		// to a bare upper-cased method. The capture is raw source text, and
		// the contract engine matches producer method against handler method
		// after case_fold only — so an unresolved `http.MethodGet` is not a
		// near miss, it is a guaranteed miss that also evades
		// method_fallback (which fires only on an *empty* method) and emits a
		// junk edge to the synthetic `unresolved` node. Scoped to http_client
		// because that is the only node type whose method is a matched key;
		// route/handler patterns capture the verb as the DSL function name
		// (`r.GET`), which is already bare.
		if nodeType == graph.NodeTypeHTTPClient {
			if raw := meta["method"]; raw != "" {
				if v, ok := normalizeHTTPVerb(raw); ok && v != raw {
					// The label was minted above as `method + " " + url` from
					// the *raw* captures, so rewrite only that leading token
					// and leave the URL half exactly as the label builder left
					// it — a prefix match, not a reconstruction.
					if strings.HasPrefix(label, raw+" ") {
						label = v + strings.TrimPrefix(label, raw)
					}
					meta["method"] = v
				}
			}
		}

		// URL constant propagation: resolve variable references in http_client URL/path captures.
		if nodeType == graph.NodeTypeHTTPClient {
			for _, key := range []string{"url", "path"} {
				if raw, ok := meta[key]; ok {
					if resolved, conf := resolveURL(raw, r.File, constants); resolved != raw {
						meta[key] = resolved
						meta["url_confidence"] = conf
						// Rebuild label if it was derived from the URL.
						if label == raw {
							label = resolved
						}
					}
				}
			}
		}

		// X.1a: instance/alias base URLs get the same constant-propagation
		// pass as http_client url/path. Without it, `axios.create({baseURL:
		// BASE_V1})` with `const BASE_V1 = "/api/v1"` looks non-literal to
		// EnrichAliases' isLiteralURL check, gets blanked, and the whole
		// "/api/v1" prefix silently vanishes from every call through that
		// instance. These binding nodes classify as NodeTypeVariable (not
		// HTTPClient), so this runs unconditionally rather than gated on
		// nodeType like the block above.
		for _, key := range []string{"instance_base_url", "alias_base_url"} {
			if raw, ok := meta[key]; ok && raw != "" {
				if resolved, _ := resolveURL(raw, r.File, constants); resolved != raw {
					meta[key] = resolved
				}
			}
		}

		// Handle Go 1.22 ServeMux "METHOD /path" route format: split into method+path.
		if path, ok := meta["path"]; ok {
			if i := strings.Index(path, " "); i > 0 {
				meta["method"] = path[:i]
				meta["path"] = path[i+1:]
				label = meta["method"] + " " + meta["path"]
			}
		}

		// HTTP-client precision + external-boundary classification. The generic
		// .Get(...)/.Post(...) queries are un-gated, so they capture non-HTTP
		// calls (url.Values.Get("user_id"), http.Header.Get("email"),
		// cache.Get(k)) and relative asset strings (static/js/x.js) as bogus
		// http_client producers — measured on svc-c these are 84% of the
		// "unresolved cross-service" count, poisoning yield denominators and
		// blast radius. Dynamic-ledgered sites (X.1 key_dynamic/key_candidates)
		// are legitimately kept.
		if nodeType == graph.NodeTypeHTTPClient && edgeType == graph.EdgeTypeHTTPCall &&
			meta["key_dynamic"] != "true" && meta["key_candidates"] == "" {
			endpoint := meta["url"]
			if endpoint == "" {
				endpoint = meta["path"]
			}
			switch {
			case endpoint == "":
				// No endpoint captured (degenerate/synthetic node) — cannot
				// judge; leave it. Real http clients always capture a url/path.
			case externalHTTPHost(endpoint) != "":
				host := externalHTTPHost(endpoint)
				// A literal third-party URL (https://pypi.org/…) is a real
				// external boundary: type it as external_service so it counts
				// resolved-external, not unresolved.
				nodeType = graph.NodeTypeExternalService
				meta["cloud_service"] = host
				meta["external_url"] = endpoint
				label = host
			case !looksLikeHTTPEndpoint(endpoint):
				// Not a URL at all — suppress the comm classification
				// (bug-class #8: drop the bogus producer, keep the file indexed).
				continue
			}
		}

		node := graph.Node{
			ID:      nodeID,
			Type:    nodeType,
			Label:   label,
			Service: service,
			File:    r.File,
			Line:    r.Line,
			Meta:    meta,
		}
		if r.EndLine >= r.Line {
			node.EndLine = r.EndLine
		}
		nodes = append(nodes, node)
	}

	// Pass 1b: deduplicate http_client nodes at file+line positions that already have
	// an http_handler node. When a more-specific route pattern (chi_get, http_verb_route)
	// and a generic client pattern (resty_get, http_get, faraday_http_verb) both match
	// the same call site, the handler node wins and the client duplicate is dropped.
	handlerLines := make(map[string]bool) // "file:line" → true
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeHTTPHandler {
			handlerLines[fmt.Sprintf("%s:%d", n.File, n.Line)] = true
		}
	}
	// Also deduplicate http_client nodes: multiple patterns can match the same call site
	// (e.g. resty_get + http_get for a plain .Get("/url") call). Keep the first match only.
	//
	// Nav links additionally dedupe by (file, path): a form matches both the
	// method-aware pair pattern and the generic action pattern — possibly at
	// different lines when the attributes span lines. The method-aware
	// pattern is registered first, so it wins.
	seenClientLines := make(map[string]string) // "file:line" → winning pattern name
	seenNavPaths := make(map[string]bool)      // "file\x00path" → true
	filtered := nodes[:0]
	for i := range nodes {
		n := nodes[i]
		// X.0: a test-DSL-demoted site (Meta is_test=true) may be produced by
		// several patterns matching the same physical call (e.g. fetch_call +
		// producer_alias_url_call on the same fetch(...)); dedupe it the same
		// way as an undemoted http_client, or the demotion itself becomes a
		// new source of duplicate nodes.
		// A reclassified-external node (external_url set by the #4 boundary rule)
		// began life as an http_client, so it shares the same
		// multiple-patterns-match-one-.Get() duplication and dedups the same way.
		reclassedExternal := n.Type == graph.NodeTypeExternalService && n.Meta["external_url"] != ""
		// WB.2: producer_alias_obj_call intentionally emits one candidate node
		// per object key at the same call site (`{ url, uri, method }` → up to
		// three matches, same file+line). They must all survive to WB.3's
		// linker-level grouping in EnrichAliases, which collapses them to one —
		// the generic same-line dedup below would otherwise silently keep only
		// the first-registered key and defeat WB.2 before WB.3 ever runs.
		// WB.4: producer_alias_url_call is the same shape — dropping @url's
		// first-position anchor means a call site with multiple string/
		// template literal args now emits one match per literal, which must
		// likewise all survive to EnrichAliases's positional-index grouping.
		isObjCallCandidate := n.Meta["pattern"] == "producer_alias_obj_call" || n.Meta["pattern"] == "producer_alias_url_call"
		if n.Type == graph.NodeTypeHTTPClient || n.Meta[graph.MetaIsTest] == "true" || reclassedExternal {
			key := fmt.Sprintf("%s:%d", n.File, n.Line)
			if handlerLines[key] {
				continue // drop: a handler pattern already owns this call site
			}
			if won, seen := seenClientLines[key]; seen {
				// A same-pattern multi-candidate group (WB.2/WB.4) stacks against
				// its own earlier members; anything else colliding on this line
				// (a rival pattern, or a multi-candidate pattern arriving after a
				// different pattern already claimed the line) is dropped exactly
				// as before.
				if !(isObjCallCandidate && won == n.Meta["pattern"]) {
					continue // drop: already have an http_client node for this line
				}
			}
			if n.Meta["nav_link"] == "true" {
				// Dynamic/multi-candidate nav links have no literal path; skip
				// path-based dedup for them (they already dedup by file+line).
				if n.Meta["key_candidates"] == "" && n.Meta["key_dynamic"] != "true" {
					navKey := n.File + "\x00" + n.Meta["path"]
					if seenNavPaths[navKey] {
						continue // drop: same link target already captured (method-aware node won)
					}
					seenNavPaths[navKey] = true
				}
			}
			if _, seen := seenClientLines[key]; !seen {
				seenClientLines[key] = n.Meta["pattern"]
			}
		}
		filtered = append(filtered, n)
	}
	nodes = filtered

	// Pass 2: emit caller→callee edges by locating the enclosing function.
	// For each non-function node, find the innermost function/method node in
	// the same file whose declaration span contains this node's line. Functions
	// whose patterns don't record an end line (no @_def capture) are treated as
	// open-ended, which degrades to the older nearest-preceding behaviour.
	//
	// Build a per-file list of (line, end, nodeID) for function/method nodes.
	// Also build a per-file name→nodeID index for Pass 3 call-ref resolution.
	type lineID struct {
		line int
		end  int // 0 = unknown (open-ended)
		id   string
	}
	funcsByFile := make(map[string][]lineID)
	nameByFileAndName := make(map[string]string) // "file\x00name" -> nodeID
	for i := range nodes {
		n := &nodes[i]
		// X.0: a test-DSL-demoted comm site (Type=function, is_test=true) is a
		// leaf call site, not a real scope — it must not become a false
		// enclosing function for whatever code follows it in the test file.
		if n.Meta[graph.MetaIsTest] == "true" {
			continue
		}
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeMethod:
			end := 0
			if v, ok := n.Meta["end_line"]; ok {
				fmt.Sscanf(v, "%d", &end)
			}
			// Same reasoning as the is_test guard above, generalised: a pattern
			// that matches a *declaration* captures a body and stamps end_line;
			// a pattern that matches a *call* cannot, so a pattern-derived
			// function node with no end_line is a call site, not a scope. It has
			// no body to contain anything, and treating it as unbounded lets it
			// swallow every later line in the file.
			//
			// `before_action :ensure_valid_token` is the case that surfaced this:
			// a class-body callback registration was the only scope candidate
			// Pass 2 could see in a Rails controller (real Ruby methods are
			// structural and appear after MatchToGraph), so the four queue-name
			// declarations inside `registration_json` were attributed to the
			// auth filter fifteen lines above them.
			//
			// Only the scope span is withheld — the node still registers its name,
			// so callee resolution by name is unaffected.
			if n.Meta["pattern"] == "" || end > 0 {
				funcsByFile[n.File] = append(funcsByFile[n.File], lineID{n.Line, end, n.ID})
			}
			nameByFileAndName[n.File+"\x00"+n.Label] = n.ID
		case graph.NodeTypeWorker:
			// Goroutine bodies are enclosing scopes too: calls inside
			// go func(){…} must attribute to the worker node, not the outer
			// function, so the worker has outgoing flow. Workers are unnamed,
			// so they never enter nameByFileAndName.
			if v, ok := n.Meta["end_line"]; ok {
				end := 0
				fmt.Sscanf(v, "%d", &end)
				funcsByFile[n.File] = append(funcsByFile[n.File], lineID{n.Line, end, n.ID})
			}
		}
	}

	// enclosingFunc returns the innermost function containing line, skipping
	// the node with skipID (a callee must not enclose its own reference).
	enclosingFunc := func(file string, line int, skipID string) *lineID {
		funcs := funcsByFile[file]
		var best *lineID
		for j := range funcs {
			f := &funcs[j]
			if f.line > line || f.id == skipID {
				continue
			}
			if f.end > 0 && line > f.end {
				continue // line falls outside this function's body
			}
			if best == nil || f.line > best.line {
				best = f
			}
		}
		return best
	}

	// moduleNodeFor lazily creates a synthetic per-file module node so that
	// module-level statements (a root render(<App/>) call, a top-level
	// EventSource) still get a caller edge instead of being silently dropped.
	// Only JS/TS modules execute top-level statements; other languages return
	// "" and the node keeps no caller edge, as before.
	moduleNodes := make(map[string]*graph.Node)
	moduleNodeFor := func(file string) string {
		if !isJSModuleFile(file) {
			return ""
		}
		if n, ok := moduleNodes[file]; ok {
			return n.ID
		}
		id := fmt.Sprintf("%s:%s:function:(module):0", service, file)
		moduleNodes[file] = &graph.Node{
			ID:      id,
			Type:    graph.NodeTypeFunction,
			Label:   "(module)",
			Service: service,
			File:    file,
			Line:    0,
			Meta:    map[string]string{"scope": "module"},
		}
		return id
	}

	// htmlDocNodeFor lazily creates a synthetic per-file document node so native
	// HTML on* event listeners have an enclosing scope to hang a dom_listen edge
	// on — the parity counterpart to a templ component (templ.go:addEventAttr)
	// or a JS module node. Static .html has no function/component to own the
	// binding, so without this the dom_target is an orphan and the
	// document→handler chain has no entry point (Z.4). Only .html/.htm files get
	// one; other languages return "". Shares moduleNodes so it materialises via
	// the same append pass; keys never collide (moduleNodeFor skips non-JS).
	htmlDocNodeFor := func(file string) string {
		if !isHTMLFile(file) {
			return ""
		}
		if n, ok := moduleNodes[file]; ok {
			return n.ID
		}
		id := fmt.Sprintf("%s:%s:component:(document):0", service, file)
		moduleNodes[file] = &graph.Node{
			ID:       id,
			Type:     graph.NodeTypeComponent,
			Label:    "(document)",
			Service:  service,
			File:     file,
			Line:     0,
			Language: "html",
			Meta:     map[string]string{"scope": "document"},
		}
		return id
	}

	for i := range nodes {
		n := &nodes[i]
		// X.0: a test-DSL-demoted comm site keeps Type=function (an "ordinary
		// calls node") but is a leaf call site, so — unlike a real function
		// declaration — it still needs a caller→it edge below.
		if (n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod) && n.Meta[graph.MetaIsTest] != "true" {
			continue
		}
		// Type declarations don't need caller→callee edges.
		if n.Type == graph.NodeTypeInterface || n.Type == graph.NodeTypeTypeAlias {
			continue
		}
		// G.7: alias/instance binding markers are not call sites; they only
		// contribute to EnrichAliases's alias table and must not emit calls edges.
		// X.9: gin route-group registrar bookkeeping nodes are the same shape —
		// they feed EnrichRouteGroups and would otherwise emit a self-edge from
		// their enclosing function (the func node sits on the same line).
		if n.Type == graph.NodeTypeVariable && (n.Meta["alias_name"] != "" || n.Meta["instance_name"] != "" ||
			strings.HasPrefix(n.Meta["pattern"], "gin_group_registrar") ||
			strings.HasPrefix(n.Meta["pattern"], "wrapper_url_")) {
			continue
		}
		var fromID string
		// Skip the node's own scope entry: a worker node must attribute to the
		// function that spawns it, not to itself.
		if best := enclosingFunc(n.File, n.Line, n.ID); best != nil {
			fromID = best.id
		} else if fromID = moduleNodeFor(n.File); fromID == "" {
			// Native HTML on* handlers have no enclosing function or JS module
			// scope; anchor their dom_listen edge at a synthetic per-file
			// document node (Z.4 templ/html parity). Scoped to DOMTarget so
			// nav_link producers keep their existing no-caller-edge behaviour.
			if n.Type == graph.NodeTypeDOMTarget {
				fromID = htmlDocNodeFor(n.File)
			}
			if fromID == "" {
				continue
			}
		}
		edgeType := graph.EdgeTypeCalls
		switch n.Type {
		case graph.NodeTypeComponent:
			edgeType = graph.EdgeTypeRenders
		case graph.NodeTypeExternalService:
			edgeType = graph.EdgeTypeCloudCall
		case graph.NodeTypeWorker:
			edgeType = graph.EdgeTypeSpawns
		case graph.NodeTypeDOMTarget:
			// Honor the DOM edge kind the pattern classified (dom_read,
			// dom_write, dom_listen, …) instead of a generic calls edge.
			if _, et := classifyPattern(n.Meta["pattern"]); strings.HasPrefix(string(et), "dom_") {
				edgeType = et
			}
		}
		edge := graph.Edge{
			ID:   fmt.Sprintf("%s:%s->%s", string(edgeType), fromID, n.ID),
			From: fromID,
			To:   n.ID,
			Type: edgeType,
		}
		// Event bindings carry the event name so onclick/oninput listeners are
		// distinguishable from plain flow in the UI (Phase U.3).
		if edgeType == graph.EdgeTypeDOMListen {
			if ev := n.Meta["event"]; ev != "" {
				edge.Label = "on " + ev
				edge.Meta = map[string]string{"event": ev}
			}
		}
		edges = append(edges, edge)
	}

	// Pass 3: resolve call-reference results (component_fn_call).
	// For each call site, find the enclosing function and emit a calls edge to
	// the target function (resolved by name in the same file). The callee is
	// skipped during enclosure lookup: a nested function (e.g. loadSource
	// defined inside Detail) must not own an event-prop reference to itself.
	for _, r := range callRefs {
		callee, ok := r.Captures["callee"]
		if !ok {
			continue
		}
		callee = stripStringLiteral(callee)

		// Resolve callee to an existing node in the same file.
		calleeID, ok := nameByFileAndName[r.File+"\x00"+callee]
		if !ok {
			if !jsBuiltins[callee] {
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: service, File: r.File, Line: r.Line,
					Name: callee, Kind: "call_ref",
				})
			}
			continue
		}

		var fromID string
		if best := enclosingFunc(r.File, r.Line, calleeID); best != nil {
			fromID = best.id
		} else if fromID = moduleNodeFor(r.File); fromID == "" {
			// Go has no module node: top-level call refs (cobra RunE: runX in a
			// package-level composite literal) dispatch from the program entry,
			// so fall back to the file's main, then init.
			if fromID = goTopLevelScope(r.File, calleeID, nameByFileAndName); fromID == "" {
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: service, File: r.File, Line: r.Line,
					Name: callee, Kind: "call_ref",
				})
				continue
			}
		}

		edgeType := callRefEdgeType(r.PatternName)
		edgeID := fmt.Sprintf("%s:%s->%s", string(edgeType), fromID, calleeID)
		edge := graph.Edge{
			ID:   edgeID,
			From: fromID,
			To:   calleeID,
			Type: edgeType,
		}
		// JSX event-prop bindings (onClick={h}, on:click={h}): label the edge
		// with the event so an onClick binding reads differently from a plain
		// call in the UI (Phase U.3).
		if r.PatternName == "jsx_event_handler_ref" {
			if ev := eventNameFromCaptures(r.Captures); ev != "" {
				edge.Label = "on " + ev
				edge.Meta = map[string]string{"event": ev}
			}
		}
		edges = append(edges, edge)
	}

	// Materialise any module nodes synthesized above.
	for _, mn := range moduleNodes {
		nodes = append(nodes, *mn)
	}

	// Pass 3b: connect event listeners to their handlers. Listener nodes
	// (addEventListener, el.onclick = …, $(x).on(…)) capture the handler
	// expression; when it is a plain identifier declared in the same file,
	// emit a calls edge listener → handler so "what runs when this fires"
	// is traversable.
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeDOMTarget && n.Type != graph.NodeTypeSubscriber {
			continue
		}
		handler := n.Meta["handler"]
		if !isIdentifier(handler) {
			continue // inline arrows/closures: their calls already attribute to the enclosing function
		}
		handlerID, ok := nameByFileAndName[n.File+"\x00"+handler]
		if !ok || handlerID == n.ID {
			continue
		}
		edge := graph.Edge{
			ID:   fmt.Sprintf("calls:%s->%s", n.ID, handlerID),
			From: n.ID,
			To:   handlerID,
			Type: graph.EdgeTypeCalls,
		}
		// Carry the event name (stamped in Pass 1) onto the handler edge so
		// "what runs on click" is labeled as such in the UI (Phase U.3).
		if ev := n.Meta["event"]; ev != "" {
			edge.Label = "on " + ev
			edge.Meta = map[string]string{"event": ev}
		}
		edges = append(edges, edge)
	}

	// Pass 4: synthesize channel nodes for AMQP publishers and subscribers.
	// For every publisher/subscriber node that has "exchange" in its meta, create
	// a NodeTypeChannel node keyed by "service:exchange/routing_key" and emit
	// publishes/subscribes edges.
	// channelID → index in nodes, so a second site for the same exchange can
	// merge its role into the node the first one minted.
	seenChannels := make(map[string]int)
	for i := range nodes {
		n := &nodes[i]
		if n.Meta[graph.MetaIsTest] == "true" {
			continue // X.0: demoted test-DSL site — no channel node, no channel edge
		}
		exchange, hasEx := n.Meta["exchange"]
		if !hasEx || exchange == "" {
			continue
		}
		exchange = stripStringLiteral(exchange)
		routingKey := stripStringLiteral(n.Meta["routing_key"])
		channelKey := exchange + "/" + routingKey
		channelID := fmt.Sprintf("%s:channel:%s", service, channelKey)

		// The site's own node type says which side of the exchange this service
		// sits on; the channel node it mints is otherwise role-blind, which is
		// what lets the cross-service amqp contract run edges backwards along
		// the message flow. A site that is neither publisher nor subscriber
		// leaves the role unset rather than guessing.
		channelRole := ""
		switch n.Type {
		case graph.NodeTypePublisher:
			channelRole = graph.ChannelRoleProducer
		case graph.NodeTypeSubscriber:
			channelRole = graph.ChannelRoleConsumer
		}
		if idx, ok := seenChannels[channelID]; ok {
			m := nodes[idx].Meta
			m[graph.MetaChannelRole] = graph.MergeChannelRole(m[graph.MetaChannelRole], channelRole)
		} else {
			meta := map[string]string{"exchange": exchange, "routing_key": routingKey}
			if channelRole != "" {
				meta[graph.MetaChannelRole] = channelRole
			}
			seenChannels[channelID] = len(nodes)
			nodes = append(nodes, graph.Node{
				ID:      channelID,
				Type:    graph.NodeTypeChannel,
				Label:   channelKey,
				Service: service,
				Meta:    meta,
			})
		}

		if n.Type == graph.NodeTypePublisher {
			edges = append(edges, graph.Edge{
				ID:   fmt.Sprintf("publishes:%s->%s", n.ID, channelID),
				From: n.ID,
				To:   channelID,
				Type: graph.EdgeTypePublishes,
			})
		} else if n.Type == graph.NodeTypeSubscriber {
			edges = append(edges, graph.Edge{
				ID:   fmt.Sprintf("subscribes:%s->%s", channelID, n.ID),
				From: channelID,
				To:   n.ID,
				Type: graph.EdgeTypeSubscribes,
			})
		}
	}

	return nodes, edges, unresolved
}

// goTopLevelScope resolves the caller for a top-level call reference in a Go
// file. Package-level function references (cobra's `RunE: runX`) are wired at
// program start, so the edge is attributed to the same file's main function,
// falling back to init. Returns "" when neither exists (the reference is
// dropped, as before). skipID guards against self-edges (a ref to main/init
// itself).
func goTopLevelScope(file, skipID string, nameByFileAndName map[string]string) string {
	if !strings.HasSuffix(file, ".go") {
		return ""
	}
	for _, name := range []string{"main", "init"} {
		if id, ok := nameByFileAndName[file+"\x00"+name]; ok && id != skipID {
			return id
		}
	}
	return ""
}

// isJSModuleFile reports whether file is a JavaScript/TypeScript or Python
// module — languages whose top-level statements execute on load and therefore
// need a synthetic (module) caller node for top-level call-ref attribution.
func isJSModuleFile(file string) bool {
	for _, ext := range []string{".js", ".jsx", ".ts", ".tsx", ".mjs", ".es6", ".py"} {
		if strings.HasSuffix(file, ext) {
			return true
		}
	}
	return false
}

// isHTMLFile reports whether file is a static HTML document (.html/.htm).
func isHTMLFile(file string) bool {
	return strings.HasSuffix(file, ".html") || strings.HasSuffix(file, ".htm")
}

// normalizeEventName reduces an event-binding attribute or property to its bare
// event name: "onClick" → "click", "on:click"/"oncapture:click" → "click",
// "onclick" → "click", "@click"/"@submit.prevent" → "click"/"submit",
// "v-on:click" → "click".
// Vue modifiers (.prevent, .stop, .once, etc.) are stripped before the prefix.
func normalizeEventName(prop string) string {
	p := prop
	// Strip Vue event modifiers: "submit.prevent" → "submit".
	if idx := strings.IndexByte(p, '.'); idx >= 0 {
		p = p[:idx]
	}
	switch {
	case strings.HasPrefix(p, "v-on:"):
		p = p[len("v-on:"):]
	case strings.HasPrefix(p, "@"):
		p = p[len("@"):]
	case strings.HasPrefix(p, "oncapture:"):
		p = p[len("oncapture:"):]
	case strings.HasPrefix(p, "on:"):
		p = p[len("on:"):]
	case strings.HasPrefix(p, "on"):
		p = p[len("on"):]
	}
	return strings.ToLower(p)
}

// eventNameFromCaptures extracts a normalized DOM/JSX event name from a match's
// captures. addEventListener-style patterns capture a quoted string in
// event_type ("click"); attribute/property patterns capture the on-prefixed
// name in prop (onClick, on:click, onclick). Returns "" when neither is present.
func eventNameFromCaptures(caps map[string]string) string {
	if et, ok := caps["event_type"]; ok {
		return strings.ToLower(stripStringLiteral(et))
	}
	if p, ok := caps["prop"]; ok && strings.HasPrefix(strings.ToLower(p), "on") {
		return normalizeEventName(p)
	}
	return ""
}

// callRefEdgeType returns the edge type for a call-reference pattern.
func callRefEdgeType(patternName string) graph.EdgeType {
	switch patternName {
	case "goroutine_call":
		return graph.EdgeTypeSpawns
	default:
		return graph.EdgeTypeCalls
	}
}

// isAWSClientConstructor reports whether a pattern is an AWS SDK client
// constructor (s3.NewFromConfig, bedrockruntime.NewFromConfig). These bind the
// client instance and are not cloud calls, so their nodes are suppressed.
func isAWSClientConstructor(patternName string) bool {
	return strings.Contains(patternName, "_client_new")
}

// looksLikeHTTPEndpoint reports whether v is shaped like an HTTP request target
// — an absolute path ("/api/x"), a full URL ("https://…"), or an X.1
// dynamic-template reconstruction ("*/api/x") — as opposed to a bare identifier
// ("user_id") or relative string ("static/js/x.js") that an un-gated
// .Get(...)/.Post(...) query captured by accident.
func looksLikeHTTPEndpoint(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	if v[0] == '/' {
		return true
	}
	if v[0] == '*' {
		// X.1 template reconstruction yields "*/api/x" (a wildcarded host) — a
		// real endpoint. A "*" followed by anything other than "/" ("**&id2=*",
		// "*?src=*", "*=") is a query-fragment shard bled from a larger
		// interpolated JS query string, not an endpoint (X.10b) → drop it. A
		// bare "*" is a fully-dynamic reconstruction and is kept (its dynamism
		// is ledgered elsewhere, not suppressed here).
		return len(v) == 1 || v[1] == '/'
	}
	// The remaining accept path is "contains ://". A real URL never contains
	// whitespace, so a value that carries a space only *mentions* a scheme —
	// e.g. the validation message "Logo path must be a full URL (http:// or
	// https://) or a path starting with /", a string literal an un-gated
	// .Post(...) captured by accident (X.10b noise) → reject it.
	if strings.ContainsAny(v, " \t\n") {
		return false
	}
	return strings.Contains(v, "://")
}

// externalHTTPHost returns the host of an absolute third-party URL
// ("https://pypi.org/pypi/x" → "pypi.org"), or "" when the URL is relative,
// points at localhost, or uses a bare (dot-less) host — which in a workspace is
// a service name, not a public boundary. A dotted host is the signal for a
// genuine external service (CRAN, PyPI, api.anthropic.com, …).
func externalHTTPHost(u string) string {
	u = strings.TrimSpace(u)
	rest := ""
	switch {
	case strings.HasPrefix(u, "https://"):
		rest = u[len("https://"):]
	case strings.HasPrefix(u, "http://"):
		rest = u[len("http://"):]
	default:
		return ""
	}
	host := rest
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		host = rest[:i]
	}
	if i := strings.Index(host, ":"); i >= 0 { // strip port
		host = host[:i]
	}
	host = strings.TrimSpace(host)
	if host == "" || host == "localhost" || strings.HasPrefix(host, "127.") {
		return ""
	}
	if !strings.Contains(host, ".") { // bare name → workspace-internal service
		return ""
	}
	return host
}

// classifyPattern maps a pattern name to appropriate node and edge types.
// Explicit prefix/exact matches take priority; keyword heuristics handle unknown patterns.
func classifyPattern(patternName string) (graph.NodeType, graph.EdgeType) {
	lower := strings.ToLower(patternName)

	switch {
	// ── G.7: alias/instance binding markers ───────────────────────────────────
	// These nodes are consumed by EnrichAliases before Engine.Link and must
	// never be treated as producer nodes (no calls edges, no engine matching).
	case strings.HasSuffix(lower, "_alias_binding") || strings.HasSuffix(lower, "_instance_binding") ||
		lower == "axios_create_with_baseurl" || lower == "resty_new_instance" ||
		lower == "axios_destructure" || lower == "axios_method_binding":
		return graph.NodeTypeVariable, graph.EdgeTypeCalls

	// ── X.9: gin route-group cross-function registrar bookkeeping ─────────────
	// These carry the func↔param and callee↔arg bindings EnrichRouteGroups needs
	// to compose a group prefix that crosses a function boundary. They are NOT
	// call sites and MUST NOT emit edges (guarded in Pass 2 by the
	// gin_group_registrar prefix), same discipline as alias binding markers.
	case lower == "gin_group_registrar_func" || lower == "gin_group_registrar_call":
		return graph.NodeTypeVariable, graph.EdgeTypeCalls

	// ── WB.1: wrapper-body param→URL bookkeeping ──────────────────────────────
	// wrapper_url_target/wrapper_url_param_key facts feed Tier WB.3's linker
	// resolution (not yet built); same discipline as alias binding markers —
	// not call sites, must not emit edges.
	case strings.HasPrefix(lower, "wrapper_url_"):
		return graph.NodeTypeVariable, graph.EdgeTypeCalls

	// ── G.7: alias/instance call sites (calls through a named alias or instance) ──
	case strings.HasPrefix(lower, "producer_alias_"):
		return graph.NodeTypeHTTPClient, graph.EdgeTypeHTTPCall

	// ── TypeScript structural declarations (suppress from graph) ──────────────
	case lower == "interface_declaration":
		return graph.NodeTypeInterface, graph.EdgeTypeCalls
	case lower == "interface_extends":
		return graph.NodeTypeInterface, graph.EdgeTypeInherits
	case lower == "type_alias" || lower == "generic_type" || lower == "enum_declaration" || lower == "const_enum":
		return graph.NodeTypeTypeAlias, graph.EdgeTypeCalls

	// ── JSX / component ───────────────────────────────────────────────────────
	case lower == "component_decl" || lower == "component_arrow_decl":
		return graph.NodeTypeComponent, graph.EdgeTypeRenders
	case lower == "jsx_component_use" || lower == "jsx_component_self_closing":
		return graph.NodeTypeComponent, graph.EdgeTypeRenders
	case lower == "lifecycle_call" || lower == "event_handler_call":
		return graph.NodeTypeFunction, graph.EdgeTypeCalls

	// ── Explicit declarations ─────────────────────────────────────────────────
	case lower == "func_decl" || lower == "function_decl" || lower == "arrow_func_decl":
		return graph.NodeTypeFunction, graph.EdgeTypeCalls
	case lower == "method_decl":
		return graph.NodeTypeMethod, graph.EdgeTypeCalls
	case lower == "class_decl":
		return graph.NodeTypeClass, graph.EdgeTypeCalls

	// ── Python imports (no graph node; captured for future cross-file linker) ──
	case lower == "python_import" || lower == "python_from_import":
		return graph.NodeTypeTypeAlias, graph.EdgeTypeCalls

	// ── Datastar / SSE ────────────────────────────────────────────────────────
	case lower == "datastar_on_signal":
		// Client-side signal subscription (JS onSignal callback), not an HTTP action.
		return graph.NodeTypeFunction, graph.EdgeTypeDatastarBind
	case strings.HasPrefix(lower, "datastar_sse") || strings.HasPrefix(lower, "sse_"):
		return graph.NodeTypeHTTPHandler, graph.EdgeTypeSSEEndpoint
	case strings.HasPrefix(lower, "datastar_action") || strings.HasPrefix(lower, "datastar_on"):
		return graph.NodeTypeHTTPClient, graph.EdgeTypeDatastarAction
	case strings.HasPrefix(lower, "datastar_bind") || strings.HasPrefix(lower, "datastar_signal"):
		return graph.NodeTypeFunction, graph.EdgeTypeDatastarBind

	// ── Background jobs (delayed_job, solid_queue, ActiveJob, Sidekiq, Celery) ─
	case strings.HasPrefix(lower, "sidekiq_perform") || strings.Contains(lower, "perform_async") ||
		strings.Contains(lower, "perform_in") || strings.Contains(lower, "perform_at") ||
		strings.HasPrefix(lower, "dj_delay") || strings.HasPrefix(lower, "dj_enqueue") ||
		strings.HasPrefix(lower, "dj_handle_async") || strings.HasPrefix(lower, "aj_perform_later") ||
		strings.HasPrefix(lower, "celery_task_delay") || strings.HasPrefix(lower, "celery_apply_async"):
		return graph.NodeTypePublisher, graph.EdgeTypeJobEnqueue
	case strings.Contains(lower, "sidekiq_worker") || strings.Contains(lower, "sidekiq_job") ||
		strings.HasPrefix(lower, "aj_perform_method") ||
		strings.HasPrefix(lower, "celery_task_decorator"):
		return graph.NodeTypeSubscriber, graph.EdgeTypeJobPerform
	case strings.HasPrefix(lower, "aj_queue_adapter") || strings.HasPrefix(lower, "sq_adapter"):
		return graph.NodeTypeFunction, graph.EdgeTypeCalls

	// ── Pusher ────────────────────────────────────────────────────────────────
	case strings.HasPrefix(lower, "pusher_trigger"):
		return graph.NodeTypePublisher, graph.EdgeTypePusherTrigger
	case strings.HasPrefix(lower, "pusher_subscribe") || strings.HasPrefix(lower, "pusher_channel"):
		return graph.NodeTypeSubscriber, graph.EdgeTypePusherSubscribe

	// ── DOM ───────────────────────────────────────────────────────────────────
	case strings.HasPrefix(lower, "dom_access") || strings.HasPrefix(lower, "query_selector") ||
		strings.HasPrefix(lower, "get_element"):
		return graph.NodeTypeDOMTarget, graph.EdgeTypeDOMRead
	case strings.HasPrefix(lower, "dom_mutation") || strings.HasPrefix(lower, "set_inner") ||
		strings.HasPrefix(lower, "set_text") || strings.HasPrefix(lower, "set_attribute") ||
		strings.HasPrefix(lower, "class_list") || strings.HasPrefix(lower, "set_style"):
		return graph.NodeTypeDOMTarget, graph.EdgeTypeDOMWrite
	case strings.HasPrefix(lower, "dom_create") || strings.HasPrefix(lower, "create_element") ||
		strings.HasPrefix(lower, "clone_node") || strings.HasPrefix(lower, "insert_adjacent"):
		return graph.NodeTypeDOMTarget, graph.EdgeTypeDOMCreate
	case strings.HasPrefix(lower, "dom_remove") || strings.HasPrefix(lower, "remove_child") ||
		strings.HasPrefix(lower, "remove_element"):
		return graph.NodeTypeDOMTarget, graph.EdgeTypeDOMRemove
	case strings.HasPrefix(lower, "dom_event") || strings.HasPrefix(lower, "add_event_listener") ||
		strings.HasPrefix(lower, "remove_event_listener") || strings.HasPrefix(lower, "on_event"):
		return graph.NodeTypeDOMTarget, graph.EdgeTypeDOMListen
	case strings.HasPrefix(lower, "dom_tree") || strings.HasPrefix(lower, "append_child") ||
		strings.HasPrefix(lower, "insert_before") || strings.HasPrefix(lower, "replace_child"):
		return graph.NodeTypeDOMTarget, graph.EdgeTypeDOMWrite

	// ── Navigation links (href / form action in JSX and HTML) ────────────────
	case strings.HasPrefix(lower, "nav_link"):
		return graph.NodeTypeHTTPClient, graph.EdgeTypeNavigatesTo

	// ── Server-sent events client (new EventSource) ──────────────────────────
	case lower == "eventsource_connect":
		return graph.NodeTypeHTTPClient, graph.EdgeTypeHTTPCall

	// ── WebSocket (gorilla server pumps + JS typed dispatch) ─────────────────
	case strings.HasPrefix(lower, "ws_upgrade"):
		return graph.NodeTypeHTTPHandler, graph.EdgeTypeWSUpgrade
	case strings.HasPrefix(lower, "ws_new"):
		return graph.NodeTypeHTTPClient, graph.EdgeTypeWSConnect
	case strings.HasPrefix(lower, "ws_send") || strings.HasPrefix(lower, "ws_write"):
		return graph.NodeTypePublisher, graph.EdgeTypeWSSend
	case strings.HasPrefix(lower, "ws_read") || strings.HasPrefix(lower, "ws_dispatch") ||
		strings.HasPrefix(lower, "ws_on"):
		return graph.NodeTypeSubscriber, graph.EdgeTypeWSRead

	// H.1: `new WebSocketServer({...})` — server-side listen construct.
	// *_server_* isn't covered by any generic heuristic (unlike ws_new/
	// ws_read's prefixes above), so it gets an explicit case, same
	// treatment as express_mount/gin_route_chained.
	case strings.HasPrefix(lower, "ws_server_new") || strings.HasPrefix(lower, "ws_server_attached") ||
		strings.HasPrefix(lower, "ws_server_noserver"):
		return graph.NodeTypeSubscriber, graph.EdgeTypeWSRead
	// X.on("connection", handler): classified as HTTPHandler so the existing
	// LinkRouteHandlers pass (keys on NodeTypeHTTPHandler + Meta["handler"])
	// wires the handler edge, same mechanism Express routes use.
	case lower == "ws_server_connection":
		return graph.NodeTypeHTTPHandler, graph.EdgeTypeCalls

	// ── SSE broadcast hub ─────────────────────────────────────────────────────
	case strings.HasPrefix(lower, "hub_broadcast"):
		return graph.NodeTypePublisher, graph.EdgeTypeHubBroadcast
	case strings.HasPrefix(lower, "hub_subscribe"):
		return graph.NodeTypeSubscriber, graph.EdgeTypeHubSubscribe
	case strings.HasPrefix(lower, "hub_method"):
		return graph.NodeTypeMethod, graph.EdgeTypeCalls

	// ── Cloud SDK boundaries (S3, Bedrock) ────────────────────────────────────
	case strings.HasPrefix(lower, "s3_") || strings.HasPrefix(lower, "bedrock_"):
		return graph.NodeTypeExternalService, graph.EdgeTypeCloudCall

	// ── Datastores (GORM / database/sql) ──────────────────────────────────────
	case strings.HasPrefix(lower, "gorm_query") || strings.HasPrefix(lower, "sql_query"):
		return graph.NodeTypeDatastore, graph.EdgeTypeQueries
	case strings.HasPrefix(lower, "gorm_persist") || strings.HasPrefix(lower, "sql_exec"):
		return graph.NodeTypeDatastore, graph.EdgeTypePersists
	case strings.HasPrefix(lower, "gorm_open") || lower == "sql_open":
		return graph.NodeTypeDatastore, graph.EdgeTypeCalls

	// ── Gin handler-body shapes (request bind / response render) ─────────────
	case strings.HasPrefix(lower, "gin_bind") || strings.HasPrefix(lower, "gin_json"):
		return graph.NodeTypeFunction, graph.EdgeTypeCalls

	// ── Message channel declarations (queue/exchange setup, not pub/sub) ─────
	// kicks_from_queue is the Sneakers/kicks consumer-side queue binding; it is
	// modeled as a channel node (like a queue_declare) so the AMQP contract can
	// join it, cross-service, to the publisher's channel.queue(name) declaration
	// on queue_name.
	case strings.Contains(lower, "queue_declare") || strings.Contains(lower, "exchange_declare") ||
		lower == "kicks_from_queue" || strings.HasPrefix(lower, "amqp_field") ||
		strings.HasPrefix(lower, "amqp_message_type"):
		return graph.NodeTypeChannel, graph.EdgeTypeCalls

	// ── Legacy XHR / jQuery ───────────────────────────────────────────────────
	case strings.HasPrefix(lower, "xhr_") || strings.HasPrefix(lower, "jquery_ajax"):
		return graph.NodeTypeHTTPClient, graph.EdgeTypeHTTPCall
	case strings.HasPrefix(lower, "jquery_selector"):
		return graph.NodeTypeDOMTarget, graph.EdgeTypeDOMRead

	// ── DOM element definitions (HTML/JSX id= / class=) ──────────────────────
	case strings.HasPrefix(lower, "html_element") || strings.HasPrefix(lower, "jsx_element"):
		return graph.NodeTypeElement, graph.EdgeTypeCalls

	// ── gRPC ──────────────────────────────────────────────────────────────────
	case strings.HasPrefix(lower, "grpc_client"):
		return graph.NodeTypeGRPCClient, graph.EdgeTypeGRPCCall
	case strings.HasPrefix(lower, "grpc_server") || strings.HasPrefix(lower, "grpc_handler"):
		return graph.NodeTypeGRPCHandler, graph.EdgeTypeGRPCCall

	// ── GraphQL ───────────────────────────────────────────────────────────────
	case strings.HasPrefix(lower, "graphql_query") || strings.HasPrefix(lower, "graphql_mutation"):
		return graph.NodeTypeGraphQLClient, graph.EdgeTypeGraphQLCall
	case strings.HasPrefix(lower, "graphql_resolver"):
		return graph.NodeTypeGraphQLResolver, graph.EdgeTypeGraphQLCall

	// H.2: Solid Router's inline `<Route path=... component=...>` declares a
	// client-side route (not a server endpoint) — explicit case (checked
	// before the generic *_route→HTTPHandler heuristic below, which is
	// server-side) so it doesn't get misclassified as a handler.
	case lower == "solid_route":
		return graph.NodeTypeRoute, graph.EdgeTypeCalls

	// HH.3: route *scaffolding* — a group declaration that lends its prefix to
	// the routes nested inside it (`resources :users do`, `namespace :api do`,
	// `api := r.Group("/v1")`). It is not an endpoint, so it must not be an
	// http_handler; it stays a node because path composition reads it.
	// Explicit case, and it must precede both the chi_* case below (which
	// `chi_route_group` matches on the "chi_route" prefix) and the generic
	// `contains "route"` heuristic (which every name here matches).
	case lower == "resources_route" || lower == "resource_route" ||
		lower == "namespace_route" ||
		strings.HasPrefix(lower, "gin_route_group") || strings.HasPrefix(lower, "chi_route_group"):
		return graph.NodeTypeRouteGroup, graph.EdgeTypeHTTPCall

	// ── HTTP routes / handlers ────────────────────────────────────────────────
	case strings.HasPrefix(lower, "chi_get") || strings.HasPrefix(lower, "chi_post") ||
		strings.HasPrefix(lower, "chi_put") || strings.HasPrefix(lower, "chi_patch") ||
		strings.HasPrefix(lower, "chi_delete") || strings.HasPrefix(lower, "chi_head") ||
		strings.HasPrefix(lower, "chi_options") || strings.HasPrefix(lower, "chi_route"):
		return graph.NodeTypeHTTPHandler, graph.EdgeTypeHTTPCall
	case strings.Contains(lower, "handler") || strings.Contains(lower, "handle") ||
		strings.Contains(lower, "route"):
		return graph.NodeTypeHTTPHandler, graph.EdgeTypeHTTPCall

	// H.0: express_mount doesn't match any *_route/*_handler substring above
	// (it's a `.use(prefix, router)` mount, not a verb registration) but is
	// still a server-side surface that should be visible as an http_handler
	// node, mirroring gin_route_chained/gin_route_group's explicit-case
	// treatment for shapes that fall outside the generic heuristic.
	case lower == "express_mount":
		return graph.NodeTypeHTTPHandler, graph.EdgeTypeHTTPCall

	// ── HTTP clients ──────────────────────────────────────────────────────────
	case strings.HasPrefix(lower, "faraday_") || strings.HasPrefix(lower, "httparty_") ||
		strings.HasPrefix(lower, "net_http_") || strings.HasPrefix(lower, "rest_client"):
		return graph.NodeTypeHTTPClient, graph.EdgeTypeHTTPCall
	case strings.Contains(lower, "client") || strings.Contains(lower, "request") ||
		strings.Contains(lower, "fetch") || strings.Contains(lower, "axios") ||
		strings.HasPrefix(lower, "http_get") || strings.HasPrefix(lower, "http_post") ||
		strings.HasPrefix(lower, "http_put") || strings.HasPrefix(lower, "http_delete") ||
		strings.HasPrefix(lower, "resty_"):
		return graph.NodeTypeHTTPClient, graph.EdgeTypeHTTPCall

	// ── Message brokers ───────────────────────────────────────────────────────
	case strings.Contains(lower, "publish"):
		return graph.NodeTypePublisher, graph.EdgeTypePublishes
	case strings.Contains(lower, "subscribe") || strings.Contains(lower, "consume"):
		return graph.NodeTypeSubscriber, graph.EdgeTypeSubscribes

	// ── Goroutines ────────────────────────────────────────────────────────────
	case strings.Contains(lower, "goroutine") || strings.Contains(lower, "spawn"):
		return graph.NodeTypeWorker, graph.EdgeTypeSpawns

	default:
		return graph.NodeTypeFunction, graph.EdgeTypeCalls
	}
}

// rubySimpleReceiverRe matches a plain local/instance/class-variable receiver
// (identifier, @ivar, @@cvar) that railsClassify can safely guess a type for.
// Anything else (method chains, index expressions, etc.) is left unresolved
// rather than guessed at.
var rubySimpleReceiverRe = regexp.MustCompile(`^@{0,2}[a-z_][a-zA-Z0-9_]*$`)

// rubyConstantReceiverRe matches a bare or namespaced constant receiver
// (Notifier, Admin::Report) — Klass.delay.method dispatches a class/module
// method, so the receiver already IS the target's qualifying type; no
// naming-convention guess is needed or applied.
var rubyConstantReceiverRe = regexp.MustCompile(`^[A-Z][a-zA-Z0-9_]*(::[A-Z][a-zA-Z0-9_]*)*$`)

// delayedJobTarget derives the Meta["dj_target"] qualified-method join key
// (<Type>#<method>) for a dj_delay or dj_handle_asynchronously match. It
// never guesses: a receiver that cannot be honestly resolved to a class
// (bug-class rule #12 — unresolvable sites still reach the contract engine's
// unmatched:ledger, they just carry no dj_target to join on) leaves the meta
// field unset.
func delayedJobTarget(r MatchResult) string {
	switch r.PatternName {
	case "dj_handle_asynchronously":
		// Implicit-self receiver: the enclosing class is exact, not a guess.
		cls := r.Captures["dj_class"]
		method := strings.TrimPrefix(r.Captures["method_name"], ":")
		if cls == "" || method == "" {
			return ""
		}
		return cls + "#" + method
	case "dj_delay":
		method := r.Captures["job_method"]
		if method == "" {
			return ""
		}
		recv := r.Captures["dj_receiver"]
		if recv == "self" || recv == "" {
			// Implicit-self receiver (explicit `self.delay.x` or bare
			// `delay.x`, no dj_receiver capture at all): exact, resolved the
			// same way as handle_asynchronously.
			if cls := r.Captures["dj_class"]; cls != "" {
				return cls + "#" + method
			}
			return ""
		}
		if rubyConstantReceiverRe.MatchString(recv) {
			// Klass.delay.method — the receiver already is the target type
			// (a class/module method dispatch), not a naming-convention guess.
			return recv + "#" + method
		}
		if cls := railsClassify(recv); cls != "" {
			return cls + "#" + method
		}
		return ""
	default:
		return ""
	}
}

// railsClassify infers a Ruby class name from a plain receiver identifier
// using the Rails naming convention (snake_case variable name -> matching
// class), e.g. "user" -> "User", "current_user" -> "CurrentUser". Only
// applies to simple identifiers/ivars (rubySimpleReceiverRe); anything more
// complex (chained calls, index expressions) returns "" so the caller
// honestly ledgers the site instead of joining on a fabricated key. This is
// a best-effort inferred join: it only ever produces an edge when a method
// node with the resulting qualified name actually exists (the contract
// engine's normal exact-match join), so a wrong guess yields no edge, not a
// wrong one.
func railsClassify(receiver string) string {
	if receiver == "" || !rubySimpleReceiverRe.MatchString(receiver) {
		return ""
	}
	name := strings.TrimLeft(receiver, "@")
	parts := strings.Split(name, "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}

// stripStringLiteral removes surrounding string delimiters from a captured value.
// Handles: Go/JS/Ruby (", ', `); Python prefix forms (f"", r"", b"", u"",
// and combinations: rb"", fr"", etc.); Python triple-quoted strings ("""/”').
func stripStringLiteral(s string) string {
	// Strip Python/Ruby string prefix letters (f, r, b, u and combinations).
	// Only strip when the prefix is immediately followed by a quote character.
	i := 0
	for i < len(s) && i < 3 {
		c := s[i]
		if c != 'f' && c != 'F' && c != 'r' && c != 'R' &&
			c != 'b' && c != 'B' && c != 'u' && c != 'U' {
			break
		}
		i++
	}
	if i > 0 && i < len(s) && (s[i] == '"' || s[i] == '\'' || s[i] == '`') {
		s = s[i:]
	}

	// Python triple-quoted strings: """...""" or '''...'''
	if len(s) >= 6 {
		if s[:3] == `"""` && s[len(s)-3:] == `"""` {
			return s[3 : len(s)-3]
		}
		if s[:3] == "'''" && s[len(s)-3:] == "'''" {
			return s[3 : len(s)-3]
		}
	}

	// Single-quoted, double-quoted, or backtick (Go/JS/Ruby/Python)
	if len(s) >= 2 {
		c := s[0]
		if (c == '"' || c == '\'' || c == '`') && s[len(s)-1] == c {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// resolveURL attempts to resolve a URL capture value that may reference a constant
// variable (up to 3 hops). Returns the resolved value and a confidence level.
//
// Handles:
//   - "VAR + \"/path\""  → look up VAR in constants, prepend
//   - "`${VAR}/path`"    → template literal with leading variable interpolation
//   - Already-literal paths (start with "/" or "http") → returned as-is with "static"
func resolveURL(raw, file string, constants map[string]map[string]string) (string, string) {
	if strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "http") {
		return raw, graph.ConfidenceStatic
	}

	fileConsts := constants[file]

	// Try up to 3 resolution hops.
	for hop := 0; hop < 3; hop++ {
		resolved, conf := tryResolveOne(raw, fileConsts)
		if conf == graph.ConfidenceStatic {
			return resolved, graph.ConfidenceInferred
		}
		if resolved == raw {
			break // no progress
		}
		raw = resolved
	}
	return raw, graph.ConfidenceUnknown
}

// tryResolveOne attempts a single resolution step on raw.
// Returns (resolved, "static") if fully resolved to a literal path,
// or (transformed, "") if partially transformed, or (raw, "") if no match.
func tryResolveOne(raw string, fileConsts map[string]string) (string, string) {
	// Pattern: VAR + "/suffix"  or  VAR + '/suffix'
	if idx := strings.Index(raw, " + "); idx > 0 {
		varName := strings.TrimSpace(raw[:idx])
		suffix := strings.TrimSpace(raw[idx+3:])
		suffix = stripStringLiteral(suffix)
		if val, ok := fileConsts[varName]; ok {
			resolved := val + suffix
			if strings.HasPrefix(resolved, "/") || strings.HasPrefix(resolved, "http") {
				return resolved, graph.ConfidenceStatic
			}
			return resolved, ""
		}
	}

	// Pattern: `${VAR}/suffix`  (template literal already stripped of backticks)
	if strings.HasPrefix(raw, "${") {
		end := strings.Index(raw, "}")
		if end > 2 {
			varName := raw[2:end]
			suffix := raw[end+1:]
			if val, ok := fileConsts[varName]; ok {
				resolved := val + suffix
				if strings.HasPrefix(resolved, "/") || strings.HasPrefix(resolved, "http") {
					return resolved, graph.ConfidenceStatic
				}
				return resolved, ""
			}
		}
	}

	// Plain variable lookup (no concatenation)
	if val, ok := fileConsts[raw]; ok {
		if strings.HasPrefix(val, "/") || strings.HasPrefix(val, "http") {
			return val, graph.ConfidenceStatic
		}
		return val, ""
	}

	// URL-builder call: mermaidURL(level, scope) → look up the helper's
	// returned literal (collected by the fn_return_* constant patterns).
	if open := strings.Index(raw, "("); open > 0 && strings.HasSuffix(raw, ")") {
		fnName := raw[:open]
		if isIdentifier(fnName) {
			if val, ok := fileConsts[fnName]; ok {
				if strings.HasPrefix(val, "/") || strings.HasPrefix(val, "http") {
					return val, graph.ConfidenceStatic
				}
				return val, ""
			}
		}
	}

	return raw, ""
}

// isKeyWalkerNode reports whether t is a producer/consumer node type whose
// key fields should be routed through the language KeyWalker (X.1a): HTTP
// clients, pub/sub publishers and subscribers, and AMQP channels (both
// producer and consumer side share NodeTypeChannel).
func isKeyWalkerNode(t graph.NodeType) bool {
	switch t {
	case graph.NodeTypeHTTPClient, graph.NodeTypePublisher, graph.NodeTypeSubscriber, graph.NodeTypeChannel:
		return true
	// H.2: Solid Router route declarations carry a producer-shaped path key
	// (literal, or a same-file constant-object member) that must go through
	// the same WalkKey resolution instead of being kept as raw capture text.
	case graph.NodeTypeRoute:
		return true
	default:
		return false
	}
}

// constResolverFor builds a contract.ConstResolver closure over a file's
// same-file constant table (name -> literal value), used by KeyWalker to
// resolve shape (b) identifier/constant references. fileConsts may be nil
// (no constants collected for the file); lookups on a nil map are safe.
func constResolverFor(fileConsts map[string]string) contract.ConstResolver {
	return func(name string) (string, bool) {
		v, ok := fileConsts[name]
		return v, ok
	}
}

// isIdentifier reports whether s is a plain identifier (letters, digits, _, $).
func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || r == '$':
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
