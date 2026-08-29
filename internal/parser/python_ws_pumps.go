package parser

import "github.com/lordsonvimal/polyflow/internal/graph"

// dropUnscopedWSPumpNodes implements PW.2's containment gate
// (docs/messaging-crossservice-flow-plan.md): patterns/python/
// fastapi_websocket.yaml's ws_read_pump_fastapi/ws_write_pump_fastapi match
// on method name alone (receive/receive_text/.../send/send_json/...),
// because FastAPI's WebSocket parameter can be named anything — unlike Go's
// gorilla patterns, which anchor on the fixed `conn` receiver. That breadth
// creates real false-positive risk (an unrelated `response.send()` or
// `queue.receive()`), so a node is only kept when its call site falls
// inside a function whose ws_upgrade_fastapi decorator was also matched in
// the same file.
//
// This runs as a post-graph filter, not a pre-graph MatchResult filter
// (contrast dropNonHTTPPythonMatches): the scoping span comes from the
// Function/Method node patterns/python/functions.yaml's func_decl already
// emits (its @_def capture gives an accurate EndLine), so reusing it here
// avoids a second tree-sitter walk purely to recompute function spans Tier
// PC already computed once.
func dropUnscopedWSPumpNodes(file string, nodes []graph.Node, edges []graph.Edge) ([]graph.Node, []graph.Edge) {
	needsGate := false
	for i := range nodes {
		if nodes[i].File != file {
			continue
		}
		if p := nodes[i].Meta["pattern"]; p == "ws_read_pump_fastapi" || p == "ws_write_pump_fastapi" {
			needsGate = true
			break
		}
	}
	if !needsGate {
		return nodes, edges
	}

	type span struct {
		line, end int
		id, label string
	}
	var funcSpans []span
	for i := range nodes {
		n := &nodes[i]
		if n.File != file {
			continue
		}
		if n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod {
			funcSpans = append(funcSpans, span{n.Line, n.EndLine, n.ID, n.Label})
		}
	}

	// innermost returns the tightest enclosing span (latest start line)
	// containing line, among spans with a known end line.
	innermost := func(line int) (span, bool) {
		best := -1
		for i := range funcSpans {
			s := &funcSpans[i]
			if s.end == 0 || s.line > line || line > s.end {
				continue
			}
			if best == -1 || s.line > funcSpans[best].line {
				best = i
			}
		}
		if best == -1 {
			return span{}, false
		}
		return funcSpans[best], true
	}

	// A ws_upgrade_fastapi match sits on the decorator line, which precedes
	// (and so is never contained by) the decorated function_definition's own
	// span — func_decl's @_def spans only the def, not the decorator above
	// it. So the enclosing function is resolved by name (captured as
	// Meta["name"] on the http_handler node), not by line containment.
	wsScoped := map[string]bool{}
	for i := range nodes {
		n := &nodes[i]
		if n.File != file || n.Meta["pattern"] != "ws_upgrade_fastapi" {
			continue
		}
		name := n.Meta["name"]
		if name == "" {
			continue
		}
		for _, f := range funcSpans {
			if f.label == name {
				wsScoped[f.id] = true
			}
		}
	}

	drop := map[string]bool{}
	kept := nodes[:0]
	for i := range nodes {
		n := &nodes[i]
		p := n.Meta["pattern"]
		if n.File == file && (p == "ws_read_pump_fastapi" || p == "ws_write_pump_fastapi") {
			f, ok := innermost(n.Line)
			if !ok || !wsScoped[f.id] {
				drop[n.ID] = true
				continue
			}
		}
		kept = append(kept, *n)
	}
	if len(drop) == 0 {
		return nodes, edges
	}

	keptEdges := edges[:0]
	for i := range edges {
		e := &edges[i]
		if drop[e.From] || drop[e.To] {
			continue
		}
		keptEdges = append(keptEdges, *e)
	}

	return kept, keptEdges
}
