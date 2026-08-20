package linker

import (
	"fmt"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// LinkExpressMiddleware turns an Express middleware registration
// (`app.use(mw)` / `router.use(mw)`) into the calls edges it stands for:
// every http_handler node registered on the same receiver variable, in the
// same file, --calls--> the middleware's real function/method definition.
// Mirrors LinkGinMiddleware/LinkRailsFilters' handler-calls-guard modeling
// (not guard-calls-handler), so the same impact/context/trace traversal
// answers "what guards this route" for free, and Tier NV's
// ClassifyEdgeNoise treats it exactly like rails_filter/gin_middleware_use
// (see internal/graph/noiseclass.go).
//
// Scope is deliberately narrower than Gin's: Express has no equivalent of
// `.Group()`'s derived-receiver chain in this codebase's route model — only
// `express_mount`'s `.use("/prefix", router)` exists, and prefix/mount
// composition across files is NOT attempted here, the same descope
// express_mount's own doc comment already records for path reconstruction.
// v1 links same-file, same-receiver-variable only: `app.use(mw)` protects
// every `app.get/post/...` registered on that same `app` variable in that
// file. A route registered on a router mounted elsewhere via
// `app.use("/prefix", router)` is not linked to the parent's middleware —
// a real, recorded gap (falls through to unresolved / no-op), not a wrong
// guess.
//
// Target resolution reuses middlewareTargetName and the same
// enclosing-function/only-trust-already-resolved-calls-edges technique
// gin_middleware.go established — both are pure string/line-range logic
// with nothing Go-specific in them.
func LinkExpressMiddleware(nodes []graph.Node, edges []graph.Edge) ([]graph.Edge, []graph.UnresolvedRef) {
	type mwUse struct {
		file, service, recv, middleware string
		line                            int
	}
	var uses []mwUse

	// file -> receiver var -> http_handler nodes registered directly on it
	handlersByFileVar := map[string]map[string][]*graph.Node{}

	type fnRange struct {
		id            string
		line, endLine int
	}
	fnRangesByFile := map[string][]fnRange{}

	nodeByID := make(map[string]*graph.Node, len(nodes))

	for i := range nodes {
		n := &nodes[i]
		nodeByID[n.ID] = n
		switch {
		case n.Meta["pattern"] == "express_middleware_use":
			recv, mw := n.Meta["recv"], n.Meta["middleware"]
			if recv == "" || mw == "" {
				continue
			}
			uses = append(uses, mwUse{
				file: n.File, service: n.Service, recv: recv, middleware: mw, line: n.Line,
			})
		case n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod:
			if n.EndLine > 0 {
				fnRangesByFile[n.File] = append(fnRangesByFile[n.File], fnRange{id: n.ID, line: n.Line, endLine: n.EndLine})
			}
		}
		if n.Type == graph.NodeTypeHTTPHandler {
			recv := n.Meta["recv"]
			if recv == "" {
				continue
			}
			if handlersByFileVar[n.File] == nil {
				handlersByFileVar[n.File] = map[string][]*graph.Node{}
			}
			handlersByFileVar[n.File][recv] = append(handlersByFileVar[n.File][recv], &nodes[i])
		}
	}

	if len(uses) == 0 {
		return nil, nil
	}

	// callsFrom: enclosing-function node ID -> label -> resolved target node ID.
	// Built only from already-resolved `calls` edges, so a target is only ever
	// offered here if some earlier pass independently proved the call reaches
	// it — same discipline as LinkGinMiddleware.
	callsFrom := map[string]map[string]string{}
	for i := range edges {
		e := &edges[i]
		if e.Type != graph.EdgeTypeCalls {
			continue
		}
		to := nodeByID[e.To]
		if to == nil {
			continue
		}
		if callsFrom[e.From] == nil {
			callsFrom[e.From] = map[string]string{}
		}
		callsFrom[e.From][to.Label] = e.To
	}

	// enclosingFunc finds the innermost function/method whose line range
	// contains `line` in `file` — the function lexically wrapping the
	// `.use(...)` call. Module-level registrations (common in Express —
	// e.g. `app.use(express.static(...))` outside any function) have no
	// enclosing function and correctly fall through to unresolved below.
	enclosingFunc := func(file string, line int) string {
		best := ""
		bestSpan := -1
		for _, r := range fnRangesByFile[file] {
			if line < r.line || line > r.endLine {
				continue
			}
			span := r.endLine - r.line
			if bestSpan == -1 || span < bestSpan {
				best, bestSpan = r.id, span
			}
		}
		return best
	}

	var newEdges []graph.Edge
	var unresolved []graph.UnresolvedRef
	seenEdge := map[string]bool{}

	for _, u := range uses {
		name := middlewareTargetName(u.middleware)
		if name == "" {
			continue
		}
		handlers := handlersByFileVar[u.file][u.recv]
		if len(handlers) == 0 {
			continue
		}

		fn := enclosingFunc(u.file, u.line)
		target := ""
		if fn != "" {
			target = callsFrom[fn][name]
		}
		if target == "" {
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: u.service, File: u.file, Line: u.line, Name: name, Kind: "middleware",
			})
			continue
		}
		for _, h := range handlers {
			id := fmt.Sprintf("%s->%s:middleware", h.ID, target)
			if seenEdge[id] {
				continue
			}
			seenEdge[id] = true
			newEdges = append(newEdges, graph.Edge{
				ID:         id,
				From:       h.ID,
				To:         target,
				Type:       graph.EdgeTypeCalls,
				Label:      "middleware",
				Confidence: "inferred",
				Meta:       map[string]string{"via": "express_middleware_use", "middleware_expr": u.middleware},
			})
		}
	}
	return newEdges, unresolved
}
