package patterns

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	gositter "github.com/smacker/go-tree-sitter/golang"
	htmlsitter "github.com/smacker/go-tree-sitter/html"
	jssitter "github.com/smacker/go-tree-sitter/javascript"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"
	tssitter "github.com/smacker/go-tree-sitter/typescript/typescript"
	tsxsitter "github.com/smacker/go-tree-sitter/typescript/tsx"

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
	case "ruby":
		return rubysitter.GetLanguage()
	case "html":
		return htmlsitter.GetLanguage()
	default:
		return nil
	}
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
		q, err := sitter.NewQuery([]byte(p.Query), lang)
		if err != nil {
			log.Printf("patterns: failed to compile query %q for language %q: %v", p.Name, patternLang, err)
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
	return m.execQueries(cqs, root, src, file)
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

	return m.execQueries(cqs, root, src, file)
}

func (m *TreeSitterMatcher) execQueries(cqs []compiledQuery, root *sitter.Node, src []byte, file string) ([]MatchResult, error) {
	var results []MatchResult

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

			// Build capture map: capture name -> text.
			// Captures whose name starts with "_" are positional only: they
			// contribute line-range information (e.g. @_def spanning a whole
			// function body) but their text is not stored, so declaration
			// bodies never leak into node meta.
			captures := make(map[string]string, len(m2.Captures))
			var minLine int = -1
			var defEndLine int
			for _, cap := range m2.Captures {
				name := cq.query.CaptureNameForId(cap.Index)
				if strings.HasPrefix(name, "_") {
					// Positional-only capture: it marks the span of the whole
					// declaration, so its end row bounds the definition body.
					if endRow := int(cap.Node.EndPoint().Row) + 1; endRow > defEndLine {
						defEndLine = endRow
					}
				} else {
					captures[name] = cap.Node.Content(src)
				}
				row := int(cap.Node.StartPoint().Row) + 1 // 1-indexed
				if minLine < 0 || row < minLine {
					minLine = row
				}
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

			mr := MatchResult{
				PatternName: cq.pattern.Name,
				Captures:    captures,
				Line:        minLine,
				EndLine:     defEndLine,
				File:        file,
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
		patternName == "cobra_run"
}

// isConstantPattern returns true for pattern results that only feed the URL
// constant-propagation table and never become graph nodes: literal const
// declarations and URL-builder helpers that return a literal.
func isConstantPattern(patternName string) bool {
	return patternName == "const_string" ||
		patternName == "const_template_prefix" ||
		patternName == "fn_return_string" ||
		patternName == "fn_return_template_prefix"
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
		nodeType, _ := classifyPattern(r.PatternName)
		if nodeType == graph.NodeTypeInterface || nodeType == graph.NodeTypeTypeAlias {
			continue
		}
		// Constant declarations exist only to feed URL propagation (the
		// constants table above); emitting them as nodes floods the graph
		// with rootless "function" entries for every const in the codebase.
		if isConstantPattern(r.PatternName) {
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
			nodeType == graph.NodeTypeComponent || nodeType == graph.NodeTypeElement
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

		// G.6 multi-candidate key: patterns that capture @branch_N produce nodes
		// with key_candidates meta (JSON array) so the engine can fan-out and match
		// each alternative independently. The raw branch captures are cleared after
		// assembly.
		if branches := extractBranchCaptures(meta); len(branches) >= 2 {
			data, _ := json.Marshal(branches)
			meta["key_candidates"] = string(data)
			// Remove individual branch_N captures — they are transient metadata
			for k := range meta {
				if strings.HasPrefix(k, "branch_") {
					delete(meta, k)
				}
			}
			// Ensure the primary key field is empty so the engine selects it for
			// injection. Method remains set (e.g. GET for nav links).
			meta["path"] = ""
			meta["url"] = ""
			label = "branch_enum"
		}

		// G.6 dynamic key: patterns that capture @key_expr carry a non-literal
		// expression in the key position. Stamp key_dynamic=true; the engine
		// surfaces a dynamic_<kind> ledger entry instead of silently dropping.
		if keyExpr, ok := meta["key_expr"]; ok {
			meta["key_dynamic"] = "true"
			meta["key_dynamic_raw"] = keyExpr // preserved for ledger Name field
			delete(meta, "key_expr")
			label = "dynamic"
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
		for _, key := range []string{"path", "url", "method", "prefix", "instance_base_url", "alias_base_url", "selector", "id", "class"} {
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

		// Handle Go 1.22 ServeMux "METHOD /path" route format: split into method+path.
		if path, ok := meta["path"]; ok {
			if i := strings.Index(path, " "); i > 0 {
				meta["method"] = path[:i]
				meta["path"] = path[i+1:]
				label = meta["method"] + " " + meta["path"]
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
	seenClientLines := make(map[string]bool) // "file:line" → true
	seenNavPaths := make(map[string]bool)    // "file\x00path" → true
	filtered := nodes[:0]
	for i := range nodes {
		n := nodes[i]
		if n.Type == graph.NodeTypeHTTPClient {
			key := fmt.Sprintf("%s:%d", n.File, n.Line)
			if handlerLines[key] {
				continue // drop: a handler pattern already owns this call site
			}
			if seenClientLines[key] {
				continue // drop: already have an http_client node for this line
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
			seenClientLines[key] = true
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
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeMethod:
			end := 0
			if v, ok := n.Meta["end_line"]; ok {
				fmt.Sscanf(v, "%d", &end)
			}
			funcsByFile[n.File] = append(funcsByFile[n.File], lineID{n.Line, end, n.ID})
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

	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod {
			continue
		}
		// Type declarations don't need caller→callee edges.
		if n.Type == graph.NodeTypeInterface || n.Type == graph.NodeTypeTypeAlias {
			continue
		}
		// G.7: alias/instance binding markers are not call sites; they only
		// contribute to EnrichAliases's alias table and must not emit calls edges.
		if n.Type == graph.NodeTypeVariable && (n.Meta["alias_name"] != "" || n.Meta["instance_name"] != "") {
			continue
		}
		var fromID string
		// Skip the node's own scope entry: a worker node must attribute to the
		// function that spawns it, not to itself.
		if best := enclosingFunc(n.File, n.Line, n.ID); best != nil {
			fromID = best.id
		} else if fromID = moduleNodeFor(n.File); fromID == "" {
			continue
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
	seenChannels := make(map[string]bool)
	for i := range nodes {
		n := &nodes[i]
		exchange, hasEx := n.Meta["exchange"]
		if !hasEx || exchange == "" {
			continue
		}
		exchange = stripStringLiteral(exchange)
		routingKey := stripStringLiteral(n.Meta["routing_key"])
		channelKey := exchange + "/" + routingKey
		channelID := fmt.Sprintf("%s:channel:%s", service, channelKey)

		if !seenChannels[channelID] {
			seenChannels[channelID] = true
			nodes = append(nodes, graph.Node{
				ID:      channelID,
				Type:    graph.NodeTypeChannel,
				Label:   channelKey,
				Service: service,
				Meta:    map[string]string{"exchange": exchange, "routing_key": routingKey},
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

// isJSModuleFile reports whether file is a JavaScript/TypeScript module —
// the only language here whose top-level statements execute on load.
func isJSModuleFile(file string) bool {
	for _, ext := range []string{".js", ".jsx", ".ts", ".tsx", ".mjs"} {
		if strings.HasSuffix(file, ext) {
			return true
		}
	}
	return false
}

// normalizeEventName reduces an event-binding attribute or property to its bare
// event name: "onClick" → "click", "on:click"/"oncapture:click" → "click",
// "onclick" → "click".
func normalizeEventName(prop string) string {
	p := prop
	switch {
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

	// ── Background jobs (delayed_job, solid_queue, ActiveJob, Sidekiq) ───────
	case strings.HasPrefix(lower, "sidekiq_perform") || strings.Contains(lower, "perform_async") ||
		strings.Contains(lower, "perform_in") || strings.Contains(lower, "perform_at") ||
		strings.HasPrefix(lower, "dj_delay") || strings.HasPrefix(lower, "dj_enqueue") ||
		strings.HasPrefix(lower, "dj_handle_async") || strings.HasPrefix(lower, "aj_perform_later"):
		return graph.NodeTypePublisher, graph.EdgeTypeJobEnqueue
	case strings.Contains(lower, "sidekiq_worker") || strings.Contains(lower, "sidekiq_job") ||
		strings.HasPrefix(lower, "aj_perform_method"):
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

	// ── Rails controllers ─────────────────────────────────────────────────────
	case lower == "controller_action":
		return graph.NodeTypeMethod, graph.EdgeTypeCalls

	// ── Message channel declarations (queue/exchange setup, not pub/sub) ─────
	case strings.Contains(lower, "queue_declare") || strings.Contains(lower, "exchange_declare"):
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

	// ── HTTP routes / handlers ────────────────────────────────────────────────
	case strings.HasPrefix(lower, "chi_get") || strings.HasPrefix(lower, "chi_post") ||
		strings.HasPrefix(lower, "chi_put") || strings.HasPrefix(lower, "chi_patch") ||
		strings.HasPrefix(lower, "chi_delete") || strings.HasPrefix(lower, "chi_head") ||
		strings.HasPrefix(lower, "chi_options") || strings.HasPrefix(lower, "chi_route"):
		return graph.NodeTypeHTTPHandler, graph.EdgeTypeHTTPCall
	case strings.Contains(lower, "handler") || strings.Contains(lower, "handle") ||
		strings.Contains(lower, "route"):
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

// stripStringLiteral removes surrounding Go/JS string delimiters (", ', `)
// and raw Go backtick literals from a captured value.
func stripStringLiteral(s string) string {
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

// extractBranchCaptures collects branch_N capture values from a meta map in
// index order (branch_0, branch_1, …) and strips surrounding quotes. Returns
// nil when fewer than two branches are present.
func extractBranchCaptures(meta map[string]string) []string {
	var branches []string
	for i := 0; ; i++ {
		key := fmt.Sprintf("branch_%d", i)
		v, ok := meta[key]
		if !ok {
			break
		}
		branches = append(branches, stripStringLiteral(v))
	}
	if len(branches) < 2 {
		return nil
	}
	return branches
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
