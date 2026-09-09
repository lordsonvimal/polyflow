package linker

import (
	"fmt"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/valuegraph"
)

// Tier VG.4 — UB.2 and UB.3 re-expressed as one crossing, read in two
// directions (docs/js-value-graph-pilot-plan.md).
//
// js_prop_urls.go and js_prop_transport.go are the mirror halves of one fact: a
// JSX attribute binds a name in the child's scope to an expression in the
// parent's scope. Once the engine can follow that binding, "the URL crosses a
// prop" and "the transport crosses a prop" stop being two producer indexes and
// two value walks, and become one traversal asked twice.
//
// What does NOT move, because it is recognition and policy rather than
// resolution: which call is a transport (findPropClientCallAtLine), which
// argument is the URL (propURLConsumerProp), which verb it carries
// (propURLCallVerb), whether the name is a parameter (jsParamIndex), the
// fan-out cap, the ledger-vs-mint decision, and the one-node-per-URL branching.
// The two passes still hold different policies over the same lattice shape and
// they still each own theirs.
//
// Both are reached only under PF_VALUEGRAPH=1; the legacy walkers are deleted in
// VG.5, not here.

// linkJSPropURLsVG is LinkJSPropURLs on the engine: for each blind-spot ledger
// row, resolve the prop the transport reads its URL from, and mint one client
// per distinct path the crossing produced.
func linkJSPropURLsVG(nodes []graph.Node, ledger []graph.UnresolvedRef, serviceFiles map[string][]string) (newNodes []graph.Node, edges []graph.Edge, out []graph.UnresolvedRef, retract map[string]bool) {
	retract = map[string]bool{}
	rows := propClientDynamicRows(ledger)
	if len(rows) == 0 {
		return nil, nil, nil, retract
	}
	sc := newJSPropScope(nodes, serviceFiles)
	parse := newJSPropParseCache()

	minted := map[string]bool{}
	unresEmitted := map[string]bool{}

	for _, row := range rows {
		p := parse(row.File)
		if p == nil {
			continue
		}
		call := findPropClientCallAtLine(p.root, p.src, row.Line)
		if call == nil {
			continue
		}
		urlNode := propURLConsumerPropNode(call, p.src)
		if urlNode == nil {
			continue
		}
		prop := urlNode.Content(p.src)
		if !fileHasProp(p.root, p.src, prop) {
			continue
		}
		eng := sc.engines[row.Service]
		if eng == nil {
			continue
		}

		producers := vgPropProducers(eng.Resolve(vgPropQuery(p, row.File, urlNode)), vgCrossPropURL)
		if len(producers) == 0 {
			// No render site names this prop on any component this file
			// defines. There is nothing to say about the site that the
			// prop_client_dynamic_url row does not already say.
			continue
		}

		pathProv := map[string]string{}
		for _, pr := range producers {
			// An unresolvable producer ledgers whether or not its siblings
			// resolved — suppressing it would hide a real render site behind
			// the ones that did.
			if pr.fail != "" {
				k := fmt.Sprintf("%s\x00%d\x00%s", pr.file, pr.line, pr.fail)
				if !unresEmitted[k] {
					unresEmitted[k] = true
					out = append(out, graph.UnresolvedRef{
						Service: row.Service, File: pr.file, Line: pr.line,
						Name: prop, Kind: ledgerPropURLUnresolved, Targets: pr.fail,
					})
				}
				continue
			}
			for _, path := range pr.paths {
				if _, seen := pathProv[path]; !seen {
					pathProv[path] = fmt.Sprintf("%s:%d", pr.file, pr.line)
				}
			}
		}
		if len(pathProv) == 0 {
			continue
		}
		if len(pathProv) > maxPropURLFanout {
			out = append(out, graph.UnresolvedRef{
				Service: row.Service, File: row.File, Line: row.Line,
				Name: prop, Kind: ledgerPropURLHighFanout,
			})
			continue
		}

		verb := propURLCallVerb(call, p.src)
		fnLabel := jsEnclosingFnName(call, p.src)
		for _, path := range sortedMapKeys(pathProv) {
			id := fmt.Sprintf("%s:%s:http_client:prop_url:%d:%s", row.Service, row.File, row.Line, path)
			if minted[id] {
				continue
			}
			minted[id] = true
			newNodes = append(newNodes, graph.Node{
				ID:       id,
				Type:     graph.NodeTypeHTTPClient,
				Label:    verb + " " + path,
				Service:  row.Service,
				File:     row.File,
				Line:     row.Line,
				Language: "javascript",
				Meta: map[string]string{
					"pattern":  "prop_url",
					"spa":      "prop_url",
					"method":   verb,
					"url":      path,
					"prop":     prop,
					"producer": pathProv[path],
					"vg_layer": vgLayer,
					"vg_rule":  vgPropURLRule,
				},
			})
			if fnID := sc.fnBySvcLabel[row.Service+"\x00"+fnLabel]; fnID != "" && fnID != id {
				edges = append(edges, graph.Edge{
					ID:         "calls:" + fnID + "->" + id,
					From:       fnID,
					To:         id,
					Type:       graph.EdgeTypeCalls,
					Confidence: graph.ConfidenceInferred,
					Meta:       map[string]string{"spa": "prop_url"},
				})
			}
		}
		retract[PropURLRetractKey(row.File, row.Line)] = true
	}
	return newNodes, edges, out, retract
}

// linkJSPropTransportVG is LinkJSPropTransport on the engine. The site is the
// same ledger row; the difference is entirely in the direction the value comes
// from, and the engine is asked for that by the same call — the reverse
// crossing fires because the URL argument is a parameter of the wrapper the
// child was handed.
func linkJSPropTransportVG(nodes []graph.Node, ledger []graph.UnresolvedRef, serviceFiles map[string][]string) (newNodes []graph.Node, edges []graph.Edge, out []graph.UnresolvedRef, retract map[string]bool) {
	retract = map[string]bool{}
	rows := propClientDynamicRows(ledger)
	if len(rows) == 0 {
		return nil, nil, nil, retract
	}
	sc := newJSPropScope(nodes, serviceFiles)
	parse := newJSPropParseCache()

	minted := map[string]bool{}
	unresEmitted := map[string]bool{}

	for _, row := range rows {
		p := parse(row.File)
		if p == nil {
			continue
		}
		call := findPropClientCallAtLine(p.root, p.src, row.Line)
		if call == nil {
			continue
		}
		urlNode := propURLConsumerPropNode(call, p.src)
		if urlNode == nil {
			continue
		}
		fn := enclosingJSFunction(call)
		if fn == nil {
			continue
		}
		if _, isParam := jsParamIndex(fn, urlNode.Content(p.src), p.src); !isParam {
			continue // bucket B/C, not UB.3
		}
		sym := jsEnclosingFnName(call, p.src)
		if sym == "" {
			continue
		}
		eng := sc.engines[row.Service]
		if eng == nil {
			continue
		}

		producers := vgPropProducers(eng.Resolve(vgPropQuery(p, row.File, urlNode)), vgCrossPropTransport)
		if len(producers) == 0 {
			continue
		}

		pathProv := map[string]string{}
		var caller string
		for _, pr := range producers {
			site := fmt.Sprintf("%s:%d", pr.file, pr.line)
			if pr.fail != "" {
				k := fmt.Sprintf("%s\x00%d\x00%s", pr.file, pr.line, pr.fail)
				if !unresEmitted[k] {
					unresEmitted[k] = true
					out = append(out, graph.UnresolvedRef{
						Service: row.Service, File: pr.file, Line: pr.line, Name: pr.fail,
						Kind: ledgerPropTransportUnresolved, Targets: sym,
					})
				}
				continue
			}
			for _, path := range pr.paths {
				if _, seen := pathProv[path]; !seen {
					pathProv[path] = site
				}
				if caller == "" || site < caller {
					caller = site
				}
			}
		}
		if len(pathProv) == 0 {
			continue
		}
		if len(pathProv) > maxPropURLFanout {
			out = append(out, graph.UnresolvedRef{
				Service: row.Service, File: row.File, Line: row.Line,
				Name: sym, Kind: ledgerPropTransportHighFanout,
			})
			continue
		}

		verb := propURLCallVerb(call, p.src)
		fnID := sc.fnBySvcLabel[row.Service+"\x00"+sym]
		for _, path := range sortedMapKeys(pathProv) {
			id := fmt.Sprintf("%s:%s:http_client:prop_transport:%d:%s", row.Service, row.File, row.Line, path)
			if minted[id] {
				continue
			}
			minted[id] = true
			newNodes = append(newNodes, graph.Node{
				ID:       id,
				Type:     graph.NodeTypeHTTPClient,
				Label:    verb + " " + path,
				Service:  row.Service,
				File:     row.File,
				Line:     row.Line,
				Language: "javascript",
				Meta: map[string]string{
					"pattern":  "prop_transport",
					"spa":      "prop_transport",
					"method":   verb,
					"url":      path,
					"via":      sym,
					"caller":   caller,
					"producer": pathProv[path],
					"vg_layer": vgLayer,
					"vg_rule":  vgPropTransportRule,
				},
			})
			if fnID != "" && fnID != id {
				edges = append(edges, graph.Edge{
					ID:         "calls:" + fnID + "->" + id,
					From:       fnID,
					To:         id,
					Type:       graph.EdgeTypeCalls,
					Confidence: graph.ConfidenceInferred,
					Meta:       map[string]string{"spa": "prop_transport"},
				})
			}
		}
		retract[PropURLRetractKey(row.File, row.Line)] = true
	}
	return newNodes, edges, out, retract
}

// ── shared plumbing ─────────────────────────────────────────────────────────

type jsPropParse struct {
	src  []byte
	root *sitter.Node
}

// newJSPropParseCache memoizes the consumer-file parses for one pass. jsParse
// is already memoized for the link phase; this only avoids re-looking-up a file
// once per ledger row.
func newJSPropParseCache() func(string) *jsPropParse {
	cache := map[string]*jsPropParse{}
	return func(rel string) *jsPropParse {
		if p, ok := cache[rel]; ok {
			return p
		}
		var p *jsPropParse
		if src, root, _, ok := jsParse(rel); ok {
			p = &jsPropParse{src: src, root: root}
		}
		cache[rel] = p
		return p
	}
}

func propClientDynamicRows(ledger []graph.UnresolvedRef) []graph.UnresolvedRef {
	var rows []graph.UnresolvedRef
	for _, u := range ledger {
		if u.Kind == "prop_client_dynamic_url" {
			rows = append(rows, u)
		}
	}
	return rows
}

// vgPropQuery asks about one expression in the consumer's file. Root is the
// file, not the enclosing function: a crossed binding is by definition not in
// the function, and the scope chain has to reach the file before the crossing
// rules are consulted.
func vgPropQuery(p *jsPropParse, file string, expr *sitter.Node) valuegraph.Query {
	return valuegraph.Query{File: file, Src: p.src, Root: p.root, Expr: expr}
}
