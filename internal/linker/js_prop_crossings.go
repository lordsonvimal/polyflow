package linker

import (
	"fmt"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/schemaurl"
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
// VG.5 retired the legacy walkers and the PF_VALUEGRAPH flag: these are the
// passes. The tier prose for each — what UB.2 and UB.3 are for, and why one
// node per URL rather than one node with many edges — stays in js_prop_urls.go
// and js_prop_transport.go alongside the recognition helpers they still own.
//
// MS.3 (partial): a producer value the engine leaves opaque because it reads
// a discovered data asset (a member-expression key read off a pinned entity,
// or a call to a learnt accessor function) is recovered by
// valuegraph_adapter.go's vgSchemaProducerFallback, through the same generic
// schemaurl.Resolver the non-crossing schema passes use — see
// vgPropReadSite. This closes the URL-crosses-a-prop shape of MS.3. It does
// NOT close the shape where the *entity-bearing schema object itself*
// crosses a prop into a component that reads a key straight off it (no
// separate URL-only prop) — that still needs a crossing-capable engine
// inside internal/factpipe's schema_url_link_sweep hub, which has no such
// engine today (docs/schema-driven-url-resolution-plan.md MS.3 status).

// LinkJSPropURLs is the Tier UB.2 pass: for each blind-spot ledger row, resolve
// the prop the transport reads its URL from through the forward crossing, and
// mint one client per distinct path the crossing produced.
//
// It returns the synthetic http_client nodes, the `calls` edges wiring them to
// their enclosing functions, the prop_url_* ledger, and the set of
// prop_client_dynamic_url sites that resolved (keyed by PropURLRetractKey) so
// the caller can retract them.
func LinkJSPropURLs(nodes []graph.Node, ledger []graph.UnresolvedRef, serviceFiles map[string][]string, resolver *schemaurl.Resolver) (newNodes []graph.Node, edges []graph.Edge, out []graph.UnresolvedRef, retract map[string]bool) {
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

		producers := vgPropProducers(eng.Resolve(vgPropQuery(p, row.File, urlNode)), vgCrossPropURL, resolver, row.Service)
		if len(producers) == 0 {
			// No render site names this prop on any component this file
			// defines. There is nothing to say about the site that the
			// prop_client_dynamic_url row does not already say.
			continue
		}

		pathProv := map[string]string{}
		schemaMeta := map[string]map[string]string{}
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
				if pr.schemaMeta != nil {
					schemaMeta[path] = pr.schemaMeta
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
			meta := map[string]string{
				"pattern":  "prop_url",
				"spa":      "prop_url",
				"method":   verb,
				"url":      path,
				"prop":     prop,
				"producer": pathProv[path],
				"vg_layer": vgLayer,
				"vg_rule":  vgPropURLRule,
			}
			for k, v := range schemaMeta[path] {
				meta[k] = v
			}
			newNodes = append(newNodes, graph.Node{
				ID:       id,
				Type:     graph.NodeTypeHTTPClient,
				Label:    verb + " " + path,
				Service:  row.Service,
				File:     row.File,
				Line:     row.Line,
				Language: "javascript",
				Meta:     meta,
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

// LinkJSPropTransport is the Tier UB.3 pass. The site is the same ledger row as
// UB.2's; the difference is entirely in the direction the value comes from, and
// the engine is asked for that by the same call — the reverse crossing fires
// because the URL argument is a parameter of the wrapper the child was handed.
//
// It returns the synthetic http_client nodes, the `calls` edges wiring them to
// the wrapper function, the prop_transport_* ledger, and the resolved
// prop_client_dynamic_url sites for the caller to retract.
func LinkJSPropTransport(nodes []graph.Node, ledger []graph.UnresolvedRef, serviceFiles map[string][]string, resolver *schemaurl.Resolver) (newNodes []graph.Node, edges []graph.Edge, out []graph.UnresolvedRef, retract map[string]bool) {
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

		producers := vgPropProducers(eng.Resolve(vgPropQuery(p, row.File, urlNode)), vgCrossPropTransport, resolver, row.Service)
		if len(producers) == 0 {
			continue
		}

		pathProv := map[string]string{}
		schemaMeta := map[string]map[string]string{}
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
				if pr.schemaMeta != nil {
					schemaMeta[path] = pr.schemaMeta
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
			meta := map[string]string{
				"pattern":  "prop_transport",
				"spa":      "prop_transport",
				"method":   verb,
				"url":      path,
				"via":      sym,
				"caller":   caller,
				"producer": pathProv[path],
				"vg_layer": vgLayer,
				"vg_rule":  vgPropTransportRule,
			}
			for k, v := range schemaMeta[path] {
				meta[k] = v
			}
			newNodes = append(newNodes, graph.Node{
				ID:       id,
				Type:     graph.NodeTypeHTTPClient,
				Label:    verb + " " + path,
				Service:  row.Service,
				File:     row.File,
				Line:     row.Line,
				Language: "javascript",
				Meta:     meta,
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
