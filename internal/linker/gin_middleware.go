package linker

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// LinkGinMiddleware turns a Gin middleware registration (`r.Use(mw)` /
// `group.Use(mw)`) into the calls edges it stands for: every http_handler
// node in scope of the registration --calls--> the middleware's real
// function/method definition. Mirrors LinkRailsFilters' before_action ->
// callback modeling (handler-calls-guard, not guard-calls-handler), so the
// same `impact`/`context`/`trace` traversal that already answers "what does
// this route call" also answers "what guards this route" for free.
//
// Before this pass, `patterns/go/gin_routes.yaml`'s gin_middleware_use
// pattern captured the registration (router variable + middleware
// expression text, e.g. `router":"protected", "middleware":"authMiddleware.
// Authenticate()"`) but nothing consumed it: the node fell through
// classifyPattern's default case as a bare `function` node labelled "Use"
// (the wrapper method, not the middleware) with zero edges. A route guarded
// by real authentication was indistinguishable from an unprotected one.
//
// Scope resolution reuses the same group-receiver chain EnrichRouteGroups
// builds for path-prefix composition (route_group nodes' var_name/receiver
// meta): a middleware registered on a group protects every http_handler
// registered directly on that group AND on any group nested underneath it
// (transitively), computed here independently rather than by depending on
// EnrichRouteGroups' composed output, since only the receiver graph is
// needed, not the resolved prefix strings.
//
// Middleware target resolution deliberately does NOT do a bare service-wide
// name lookup: `cors.New(cors.Config{...})`'s trailing identifier "New"
// collides with unrelated same-named functions elsewhere in the service
// (e.g. a logger constructor), and a wrong security-adjacent edge is worse
// than none. Instead it reuses the Go semantic analyzer's own SSA-resolved
// `calls` edges — already sitting in `edges` by the time this pass runs —
// filtered to ones originating from the function that lexically contains the
// `r.Use(...)` call (found via Node.Line/EndLine containment). SSA already
// proved that specific call resolves; this pass only has to pick the one
// whose target label matches the registration's trailing identifier. An
// external, unindexed middleware (`cors.New`, from a vendored package) has
// no SSA edge to find and correctly falls through to UnresolvedRef instead
// of a guess.
func LinkGinMiddleware(nodes []graph.Node, edges []graph.Edge) ([]graph.Edge, []graph.UnresolvedRef) {
	// file -> group var_name -> receiver var (the group/router it was declared on)
	receiverOf := map[string]map[string]string{}

	type mwUse struct {
		file, service, router, middleware string
		line                              int
	}
	var uses []mwUse

	// file -> router var -> http_handler nodes registered directly on it
	handlersByFileVar := map[string]map[string][]*graph.Node{}

	// enclosing function/method lookup: file -> nodes sorted for containment search
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
		case n.Type == graph.NodeTypeRouteGroup && strings.HasPrefix(n.Meta["pattern"], "gin_route_group"):
			vn, recv := n.Meta["var_name"], n.Meta["receiver"]
			if vn == "" {
				continue
			}
			if receiverOf[n.File] == nil {
				receiverOf[n.File] = map[string]string{}
			}
			receiverOf[n.File][vn] = recv
		case n.Meta["pattern"] == "gin_middleware_use":
			router, mw := n.Meta["router"], n.Meta["middleware"]
			if router == "" || mw == "" {
				continue
			}
			uses = append(uses, mwUse{
				file: n.File, service: n.Service, router: router, middleware: mw, line: n.Line,
			})
		case n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod:
			if n.EndLine > 0 {
				fnRangesByFile[n.File] = append(fnRangesByFile[n.File], fnRange{id: n.ID, line: n.Line, endLine: n.EndLine})
			}
		}
		if n.Type == graph.NodeTypeHTTPHandler {
			router := n.Meta["router"]
			if router == "" {
				continue
			}
			if handlersByFileVar[n.File] == nil {
				handlersByFileVar[n.File] = map[string][]*graph.Node{}
			}
			handlersByFileVar[n.File][router] = append(handlersByFileVar[n.File][router], &nodes[i])
		}
	}

	if len(uses) == 0 {
		return nil, nil
	}

	// callsFrom: enclosing-function node ID -> label -> resolved target node ID.
	// Built only from already-resolved `calls` edges, so a target is only ever
	// offered here if some earlier pass (SSA, tree-sitter call resolution)
	// independently proved the call reaches it.
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
	// `r.Use(...)` call (e.g. SetupRouter).
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

	// descendantsOf returns root plus every group var whose receiver chain
	// (of any length) bottoms out at root, via fixpoint iteration — the same
	// technique EnrichRouteGroups.resolveGinPrefixes uses for prefixes.
	descendantsOf := func(file, root string) map[string]bool {
		result := map[string]bool{root: true}
		recv := receiverOf[file]
		for changed := true; changed; {
			changed = false
			for v, r := range recv {
				if result[r] && !result[v] {
					result[v] = true
					changed = true
				}
			}
		}
		return result
	}

	var newEdges []graph.Edge
	var unresolved []graph.UnresolvedRef
	seenEdge := map[string]bool{}

	for _, u := range uses {
		name := middlewareTargetName(u.middleware)
		if name == "" {
			continue
		}
		scope := descendantsOf(u.file, u.router)
		var handlers []*graph.Node
		for v := range scope {
			handlers = append(handlers, handlersByFileVar[u.file][v]...)
		}
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
				Meta:       map[string]string{"via": "gin_middleware_use", "middleware_expr": u.middleware},
			})
		}
	}
	return newEdges, unresolved
}

var ginMiddlewareIdentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// middlewareTargetName extracts the resolvable identifier out of a
// middleware registration expression: `authMiddleware.Authenticate()` ->
// `Authenticate`, `gin.Recovery()` -> `Recovery`, `LoggingMiddleware(log)` ->
// `LoggingMiddleware`. Returns "" for anything that isn't a simple call
// (nothing to look up, not an error).
func middlewareTargetName(expr string) string {
	s := strings.TrimSpace(expr)
	if idx := strings.Index(s, "("); idx >= 0 {
		s = s[:idx]
	}
	if idx := strings.LastIndex(s, "."); idx >= 0 {
		s = s[idx+1:]
	}
	if !ginMiddlewareIdentRe.MatchString(s) {
		return ""
	}
	return s
}
