package patterns

import (
	"context"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	gositter "github.com/smacker/go-tree-sitter/golang"
	jssitter "github.com/smacker/go-tree-sitter/javascript"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"
	tssitter "github.com/smacker/go-tree-sitter/typescript/typescript"
	tsxsitter "github.com/smacker/go-tree-sitter/typescript/tsx"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// MatchResult holds a single pattern match against source code.
type MatchResult struct {
	PatternName string
	NodeID      string
	Captures    map[string]string // capture name -> matched text
	Line        int
	File        string
}

// compiledQuery holds a compiled tree-sitter query and the original pattern.
type compiledQuery struct {
	query   *sitter.Query
	pattern *Pattern
}

// TreeSitterMatcher runs tree-sitter queries against source files.
type TreeSitterMatcher struct {
	registry *Registry
	mu       sync.Mutex
	// compiled queries cached per language: language -> patternName -> compiledQuery
	compiled map[string][]compiledQuery
}

// NewTreeSitterMatcher creates a matcher backed by the given registry.
func NewTreeSitterMatcher(reg *Registry) *TreeSitterMatcher {
	return &TreeSitterMatcher{
		registry: reg,
		compiled: make(map[string][]compiledQuery),
	}
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

			// Build capture map: capture name -> text
			captures := make(map[string]string, len(m2.Captures))
			var minLine int = -1
			for _, cap := range m2.Captures {
				name := cq.query.CaptureNameForId(cap.Index)
				text := cap.Node.Content(src)
				captures[name] = text
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

			results = append(results, MatchResult{
				PatternName: cq.pattern.Name,
				Captures:    captures,
				Line:        minLine,
				File:        file,
			})
		}
	}

	return results, nil
}

// MatchToNodes converts raw match results into typed graph nodes and edges.
func (m *TreeSitterMatcher) MatchToNodes(service string, results []MatchResult) ([]graph.Node, []graph.Edge) {
	return MatchToGraph(service, results)
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

// MatchToGraph maps match results to graph nodes and edges.
// Node IDs follow the design doc format: service:file:type:name:line
func MatchToGraph(service string, results []MatchResult) ([]graph.Node, []graph.Edge) {
	nodes := make([]graph.Node, 0, len(results))
	var edges []graph.Edge

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

	// Build per-file constant table from const_string / const_template_prefix results.
	// file -> varName -> literalValue
	constants := make(map[string]map[string]string)
	for _, r := range defResults {
		if r.PatternName != "const_string" && r.PatternName != "const_template_prefix" {
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
		}

		// ID format: service:file:type:name:line  (design doc §SQLite Schema)
		// Function/method/component nodes use the captured name so edges can target the same ID.
		idName := r.PatternName
		namedTypes := nodeType == graph.NodeTypeFunction || nodeType == graph.NodeTypeMethod || nodeType == graph.NodeTypeComponent
		if namedTypes && label != r.PatternName {
			idName = label
		}
		nodeID := fmt.Sprintf("%s:%s:%s:%s:%d", service, r.File, string(nodeType), idName, r.Line)

		// Build meta from all captures
		meta := make(map[string]string, len(r.Captures))
		maps.Copy(meta, r.Captures)

		// Strip surrounding quotes from path and url captures (tree-sitter includes them).
		for _, key := range []string{"path", "url", "method"} {
			if v, ok := meta[key]; ok {
				meta[key] = stripStringLiteral(v)
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
	seenClientLines := make(map[string]bool) // "file:line" → true
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
			seenClientLines[key] = true
		}
		filtered = append(filtered, n)
	}
	nodes = filtered

	// Pass 2: emit caller→callee edges using line-proximity.
	// For each non-function node, find the closest function/method node defined
	// in the same file at a line <= this node's line. That is the enclosing function.
	//
	// Build a per-file sorted list of (line, nodeID) for function/method nodes.
	// Also build a per-file name→nodeID index for Pass 3 call-ref resolution.
	type lineID struct {
		line int
		id   string
	}
	funcsByFile := make(map[string][]lineID)
	nameByFileAndName := make(map[string]string) // "file\x00name" -> nodeID
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod {
			funcsByFile[n.File] = append(funcsByFile[n.File], lineID{n.Line, n.ID})
			nameByFileAndName[n.File+"\x00"+n.Label] = n.ID
		}
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
		funcs := funcsByFile[n.File]
		if len(funcs) == 0 {
			continue
		}
		// Find the closest function whose line is <= this node's line.
		var best *lineID
		for j := range funcs {
			f := &funcs[j]
			if f.line <= n.Line {
				if best == nil || f.line > best.line {
					best = f
				}
			}
		}
		if best == nil {
			continue
		}
		edgeType := graph.EdgeTypeCalls
		if n.Type == graph.NodeTypeComponent {
			edgeType = graph.EdgeTypeRenders
		}
		edges = append(edges, graph.Edge{
			ID:   fmt.Sprintf("%s:%s->%s", string(edgeType), best.id, n.ID),
			From: best.id,
			To:   n.ID,
			Type: edgeType,
		})
	}

	// Pass 3: resolve call-reference results (component_fn_call).
	// For each call site, find the enclosing function by proximity and emit a
	// calls edge to the target function (resolved by name in the same file).
	for _, r := range callRefs {
		callee, ok := r.Captures["callee"]
		if !ok {
			continue
		}
		callee = stripStringLiteral(callee)

		// Resolve callee to an existing node in the same file.
		calleeID, ok := nameByFileAndName[r.File+"\x00"+callee]
		if !ok {
			continue
		}

		// Find enclosing function by proximity, skipping the callee itself.
		// A nested function (e.g. loadSource defined inside Detail) appears as
		// a closer proximity match than the outer function, but we want the
		// outer function to own the event-prop reference.
		funcs := funcsByFile[r.File]
		var best *lineID
		for j := range funcs {
			f := &funcs[j]
			if f.line <= r.Line && f.id != calleeID {
				if best == nil || f.line > best.line {
					best = f
				}
			}
		}
		if best == nil {
			continue
		}

		edgeType := callRefEdgeType(r.PatternName)
		edgeID := fmt.Sprintf("%s:%s->%s", string(edgeType), best.id, calleeID)
		edges = append(edges, graph.Edge{
			ID:   edgeID,
			From: best.id,
			To:   calleeID,
			Type: edgeType,
		})
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

	return nodes, edges
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
	// ── TypeScript structural declarations (suppress from graph) ──────────────
	case lower == "interface_declaration" || lower == "interface_extends":
		return graph.NodeTypeInterface, graph.EdgeTypeCalls
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
	case strings.HasPrefix(lower, "datastar_sse") || strings.HasPrefix(lower, "sse_"):
		return graph.NodeTypeHTTPHandler, graph.EdgeTypeSSEEndpoint
	case strings.HasPrefix(lower, "datastar_action") || strings.HasPrefix(lower, "datastar_on"):
		return graph.NodeTypeHTTPClient, graph.EdgeTypeDatastarAction
	case strings.HasPrefix(lower, "datastar_bind") || strings.HasPrefix(lower, "datastar_signal"):
		return graph.NodeTypeFunction, graph.EdgeTypeDatastarBind

	// ── Sidekiq ───────────────────────────────────────────────────────────────
	case strings.HasPrefix(lower, "sidekiq_perform") || strings.Contains(lower, "perform_async") ||
		strings.Contains(lower, "perform_in") || strings.Contains(lower, "perform_at"):
		return graph.NodeTypePublisher, graph.EdgeTypeSidekiqEnqueue
	case strings.Contains(lower, "sidekiq_worker") || strings.Contains(lower, "sidekiq_job"):
		return graph.NodeTypeSubscriber, graph.EdgeTypeSidekiqPerform

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

	return raw, ""
}
