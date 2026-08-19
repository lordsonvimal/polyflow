package linker

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

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
	src, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	lang := grammarLangForFile(file)
	root, err := sitter.ParseCtx(context.Background(), src, lang)
	if err != nil {
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
		out = append(out, graph.Node{
			ID:       id,
			Type:     graph.NodeTypeHTTPClient,
			Label:    urlText,
			Service:  service,
			File:     relFile,
			Line:     line,
			Language: "javascript",
			Meta: map[string]string{
				"pattern":  "js_api_wrapper_call_site",
				"wrapper":  callee,
				"url_expr": urlText,
			},
		})
	}
	return out
}
