package linker

import (
	"fmt"
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// LinkJSAPIWrapperCalls mints an http_client node for every call to a
// WB.1-detected JS/TS wrapper function, even across files and even when the
// call's URL argument is a local variable rather than a literal.
//
// producer_alias_url_call/obj_call (patterns/javascript/producer_alias.yaml)
// only fire when the call's URL-position argument is itself a string/
// template literal, so a wrapper called as `apiPut(updateUrl, data)`
// (updateUrl built up elsewhere in the caller — the common real-world shape,
// confirmed on orion's services/ApiServices.js callers) never matches at
// all, regardless of whether the wrapper's own body proves it forwards to
// axios/fetch. And the existing wrapper_url_* consumer
// (contract/alias.go's wrapperURLTable) only disambiguates an *existing*
// producer_alias_* node's argument among several literal candidates — it
// can't create a node where none exists, and it resolves the wrapper name
// against the CALL SITE's own file (contract.indirKey), so it only ever
// helped a wrapper called from the same file it's declared in. A shared
// module like ApiServices.js, imported and called from dozens of other
// files, was invisible to it entirely.
//
// This pass closes both gaps: it builds a SERVICE-WIDE (not per-file) table
// of wrapper name -> URL parameter index from wrapper_url_*-tagged nodes
// carrying a param_index (the WB.4-style "positional probe" patterns:
// wrapper_url_positional_fetch_call/axios_call and the axios(config-object)
// patterns added alongside this fix) — discovered dynamically from each
// wrapper's own body, not a hardcoded name list, so any codebase's shared
// axios/fetch wrapper module is caught, not just orion's apiGet/apiPut/....
// It then re-walks every JS/TS file's AST for call_expression sites whose
// callee is a bare identifier matching one of those names, minting an
// http_client node from the argument at the wrapper's own forwarded
// position — literal or identifier either way.
func LinkJSAPIWrapperCalls(nodes []graph.Node, serviceFiles map[string][]string) ([]graph.Node, []graph.Edge) {
	// service -> wrapperName -> URL param index
	wrapperParamIndex := map[string]map[string]int{}
	// file -> enclosing function/method ranges, for attributing each new
	// http_client node to its containing function with a calls edge — the
	// same line-range containment technique LinkGinMiddleware uses to find
	// the function lexically wrapping a call site.
	type fnRange struct {
		id            string
		line, endLine int
	}
	fnRangesByFile := map[string][]fnRange{}
	for i := range nodes {
		n := &nodes[i]
		if (n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod) && n.EndLine > 0 {
			fnRangesByFile[n.File] = append(fnRangesByFile[n.File], fnRange{id: n.ID, line: n.Line, endLine: n.EndLine})
		}
		if !strings.HasPrefix(n.Meta["pattern"], "wrapper_url_") {
			continue
		}
		wname := n.Meta["wrapper_name"]
		idxStr := n.Meta["param_index"]
		if wname == "" || idxStr == "" {
			continue // url_key-based facts (member/destructure forms) are out of scope here
		}
		idx, err := strconv.Atoi(idxStr)
		if err != nil {
			continue
		}
		if wrapperParamIndex[n.Service] == nil {
			wrapperParamIndex[n.Service] = map[string]int{}
		}
		if _, exists := wrapperParamIndex[n.Service][wname]; !exists {
			wrapperParamIndex[n.Service][wname] = idx
		}
	}
	if len(wrapperParamIndex) == 0 {
		return nil, nil
	}

	// Discovered wrapperParamIndex above is single-hop: it only knows a
	// function is a wrapper because its OWN body calls fetch/axios directly.
	// A wrapper of a wrapper (`apiPost` forwards to `genericRequest`, which
	// itself forwards to `fetch`) is common in real codebases that centralize
	// retry/auth/logging behind one inner primitive, and was invisible before
	// this loop: a service-wide re-scan discovers any function whose body
	// forwards one of its own parameters into an ALREADY-known wrapper name,
	// registers it too, and repeats until no new wrapper is found. Same-service
	// only, for the same reason the base table is per-service (K.7a) — a
	// wrapper chain that crosses a service/package boundary (e.g. into an
	// imported library) is out of reach for a source-only parser and is left
	// unresolved rather than guessed at.
	for svc, files := range serviceFiles {
		wrappers := wrapperParamIndex[svc]
		if len(wrappers) == 0 {
			continue
		}
		for hop := 0; hop < 5; hop++ {
			added := false
			for _, file := range files {
				if !isJSFile(file) {
					continue
				}
				for name, idx := range discoverJSTransitiveWrappers(file, wrappers) {
					if _, exists := wrappers[name]; !exists {
						wrappers[name] = idx
						added = true
					}
				}
			}
			if !added {
				break
			}
		}
	}

	enclosingFunc := func(file string, line int) string {
		best, bestSpan := "", -1
		for _, r := range fnRangesByFile[file] {
			if line < r.line || line > r.endLine {
				continue
			}
			if span := r.endLine - r.line; bestSpan == -1 || span < bestSpan {
				best, bestSpan = r.id, span
			}
		}
		return best
	}

	var newNodes []graph.Node
	var newEdges []graph.Edge
	seenID := map[string]bool{}

	for svc, files := range serviceFiles {
		wrappers := wrapperParamIndex[svc]
		if len(wrappers) == 0 {
			continue
		}
		for _, file := range files {
			if !isJSFile(file) {
				continue
			}
			for _, n := range scanJSWrapperCallSites(svc, file, wrappers) {
				if seenID[n.ID] {
					continue
				}
				seenID[n.ID] = true
				newNodes = append(newNodes, n)
				if fn := enclosingFunc(n.File, n.Line); fn != "" {
					newEdges = append(newEdges, graph.Edge{
						ID:         fmt.Sprintf("%s->%s:wrapper_call", fn, n.ID),
						From:       fn,
						To:         n.ID,
						Type:       graph.EdgeTypeCalls,
						Confidence: "inferred",
						Meta:       map[string]string{"via": "js_api_wrapper_call_site"},
					})
				}
			}
		}
	}
	return newNodes, newEdges
}

// scanJSWrapperCallSites re-parses file and returns one http_client node per
// call to a name in wrappers, mirroring LinkRubyTypeRelations/LinkRailsFilters'
// documented reason for re-parsing instead of reading a pattern-emitted node:
// the wrapper table is only known after all files are collected, so the call
// sites can't have been captured with this knowledge at parse time.
func scanJSWrapperCallSites(service, file string, wrappers map[string]int) []graph.Node {
	src, root, lang, ok := jsParse(file)
	if !ok {
		return nil
	}

	q, err := compiledQuery(`
		(call_expression
			function: (identifier) @callee
			arguments: (arguments) @args) @call`, lang)
	if err != nil {
		return nil
	}
	cur := sitter.NewQueryCursor()
	cur.Exec(q, root)

	relFile := patterns.RelativizeToCwd(file)
	var out []graph.Node
	for {
		m, ok := cur.NextMatch()
		if !ok {
			break
		}
		var calleeNode, argsNode, callNode *sitter.Node
		for _, c := range m.Captures {
			switch q.CaptureNameForId(c.Index) {
			case "callee":
				calleeNode = c.Node
			case "args":
				argsNode = c.Node
			case "call":
				callNode = c.Node
			}
		}
		if calleeNode == nil || argsNode == nil || callNode == nil {
			continue
		}
		callee := calleeNode.Content(src)
		paramIdx, ok := wrappers[callee]
		if !ok {
			continue
		}
		if paramIdx >= int(argsNode.NamedChildCount()) {
			continue // wrapper's URL param has no matching argument at this call site
		}
		urlArg := argsNode.NamedChild(paramIdx)
		urlText := strings.TrimSpace(urlArg.Content(src))
		if urlText == "" {
			continue
		}
		line := int(callNode.StartPoint().Row) + 1
		id := fmt.Sprintf("%s:%s:http_client:js_api_wrapper_call:%s:%d", service, relFile, callee, line)
		label := urlText
		meta := map[string]string{
			"pattern":  "js_api_wrapper_call_site",
			"wrapper":  callee,
			"url_expr": urlText,
		}
		// The raw urlText above is the argument's verbatim source span — fine
		// as a bare string/template literal, but a `+`-concatenation or a
		// local `var url = ...` reference needs the same reconstruction the
		// direct-fetch/axios patterns get via the javascript KeyWalker
		// (contract/keywalk_javascript.go), or it never carries a Meta["url"]
		// the http.yaml contract's [method, path] key can match against —
		// every wrapper-forwarded call site fell to the synthetic `unresolved`
		// node regardless of how resolvable its URL actually was.
		if walker := contract.KeyWalkerFor("javascript"); walker != nil {
			noConsts := func(string) (string, bool) { return "", false }
			switch cands, dynamic := walker.WalkKey(urlArg, src, noConsts); {
			case dynamic:
				meta["key_dynamic"] = "true"
				meta["key_dynamic_raw"] = urlText
				label = "dynamic"
			case len(cands) == 1:
				meta["url"] = cands[0]
				label = cands[0]
			case len(cands) >= 2:
				meta["key_candidates"] = contract.MarshalKeyCandidates(cands)
				label = "branch_enum"
			}
		}
		out = append(out, graph.Node{
			ID:       id,
			Type:     graph.NodeTypeHTTPClient,
			Label:    label,
			Service:  service,
			File:     relFile,
			Line:     line,
			Language: "javascript",
			Meta:     meta,
		})
	}
	return out
}

// discoverJSTransitiveWrappers re-parses file and returns wrapper-name ->
// forwarded-param-index for every function/arrow function defined in it whose
// body calls an ALREADY-known wrapper (a key of wrappers), forwarding one of
// its OWN parameters at that wrapper's expected argument position — i.e. one
// more hop of the same forwarding shape jsWrapperParamIndex (matcher.go)
// detects for a direct fetch/axios call, just against a dynamically-growing
// name set instead of a hardcoded "fetch"/"axios" match. Mirrors
// scanJSWrapperCallSites' re-parse rationale: the wrapper table two hops
// out is only known after this same pass has already run once.
func discoverJSTransitiveWrappers(file string, wrappers map[string]int) map[string]int {
	src, root, _, ok := jsParse(file)
	if !ok {
		return nil
	}

	out := map[string]int{}
	var walkDefs func(n *sitter.Node)
	walkDefs = func(n *sitter.Node) {
		if n == nil {
			return
		}
		var params, name *sitter.Node
		switch n.Type() {
		case "function_declaration":
			params = n.ChildByFieldName("parameters")
			name = n.ChildByFieldName("name")
		case "arrow_function", "function_expression":
			params = n.ChildByFieldName("parameters")
			if decl := n.Parent(); decl != nil && decl.Type() == "variable_declarator" {
				name = decl.ChildByFieldName("name")
			}
		}
		if params != nil && name != nil {
			if _, idx, ok := jsForwardedParamCall(n, params, wrappers, src); ok {
				out[name.Content(src)] = idx
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walkDefs(n.Child(i))
		}
	}
	walkDefs(root)
	if len(out) == 0 {
		return nil
	}
	return out
}

// jsForwardedParamCall scans fnDef's body (not descending into a nested
// function definition — a nested closure's own forwarding says nothing about
// the outer function) for a call to one of wrappers whose argument at that
// wrapper's own forwarded index is itself one of fnDef's own parameters.
// Returns the wrapper name it called and fnDef's own matching parameter
// index.
func jsForwardedParamCall(fnDef, params *sitter.Node, wrappers map[string]int, src []byte) (calledWrapper string, ownParamIndex int, ok bool) {
	var paramNames []string
	for i := 0; i < int(params.NamedChildCount()); i++ {
		p := params.NamedChild(i)
		if p.Type() == "required_parameter" || p.Type() == "optional_parameter" {
			if inner := p.ChildByFieldName("pattern"); inner != nil {
				p = inner
			}
		}
		if p.Type() == "identifier" {
			paramNames = append(paramNames, p.Content(src))
		} else {
			paramNames = append(paramNames, "")
		}
	}

	body := fnDef.ChildByFieldName("body")
	if body == nil {
		return "", -1, false
	}

	var found bool
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil || found {
			return
		}
		switch n.Type() {
		case "function_declaration", "arrow_function", "function_expression":
			return // do not descend into a nested closure
		case "call_expression":
			if fn := n.ChildByFieldName("function"); fn != nil && fn.Type() == "identifier" {
				callee := fn.Content(src)
				if wrapIdx, known := wrappers[callee]; known {
					if args := n.ChildByFieldName("arguments"); args != nil && wrapIdx < int(args.NamedChildCount()) {
						argText := args.NamedChild(wrapIdx).Content(src)
						for pi, pname := range paramNames {
							if pname != "" && pname == argText {
								found, calledWrapper, ownParamIndex, ok = true, callee, pi, true
								return
							}
						}
					}
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			if found {
				return
			}
			walk(n.Child(i))
		}
	}
	walk(body)
	return calledWrapper, ownParamIndex, ok
}
