// Command fiber is Phase 1's reference plugin
// (docs/linker-plugin-architecture-plan.md): a real, third-party-shaped
// linker for github.com/gofiber/fiber/v2, a Go web framework polyflow's own
// patterns/go/*.yaml does not otherwise cover (chi/gin are built in; fiber
// is not). It is authored exactly the way a real plugin author would: it
// imports sdk/linkplugin only, never internal/*.
//
// Two components prove the multi-component manifest schema against a real
// case (not just a single-component fixture like Phase 0's fakeplugin):
//
//   - "routes": patterns/routes.yaml captures each `app.Get("/path", handler)`-
//     shaped registration as a fiber_route node (auto-classified
//     graph.NodeTypeHTTPHandler by internal/patterns/matcher.go's
//     substring-on-"route" default — no core change needed for a brand new
//     pattern name). Link resolves the handler reference to the actual
//     function/method node in the same batch and draws a "routes_to" edge —
//     a new edge kind, minted the way the plan's SDK doc says a plugin is
//     expected to.
//   - "middleware": patterns/middleware.yaml captures each `app.Use(mw)`
//     registration. Link resolves mw to its function/method node too, and
//     additionally dials back into core's containment capability (declared
//     via Requires) to look up the enclosing scope of the file the
//     registration lives in.
//
// Link alone cannot connect the two components' nodes to each other — a
// route and the middleware guarding it are captured by separate patterns in
// separate Link calls, batched independently. Instead of resolving the join
// in Link, each component's Link stashes its own resolved (file -> function
// node ID) facts as carrier UnresolvedRef entries (the same technique
// internal/linker/amqp_handshake.go uses for its cross-service queue-name
// join — a bridge symbol, not a real unresolved miss) and Reconcile,
// invoked once per plugin after every component's Link results are pooled,
// cross-joins same-file route/middleware pairs into the real "calls" edge
// (handler --calls--> middleware, mirroring LinkGinMiddleware's direction),
// then retracts the carrier entries so they never reach the ledger UI.
package main

import (
	"fmt"
	"strings"

	"github.com/lordsonvimal/polyflow/sdk/linkplugin"
	lpgraph "github.com/lordsonvimal/polyflow/sdk/linkplugin/graph"
)

// nodeTypeFunction mirrors internal/graph/model.go's NodeTypeFunction. A
// plugin never imports internal/graph (see docs/linker-plugin-architecture-plan.md's
// "Pinned Go surface"), so well-known core node type strings a plugin needs
// to recognize are copied here, the same way sdk/linkplugin/graph itself is
// a vendored copy rather than an import.
const nodeTypeFunction = "function"

// carrier UnresolvedRef kinds: these are join keys for Reconcile, not real
// ledger misses — Reconcile retracts every one it consumes.
const (
	kindRouteResolved      = "fiber_route_resolved"
	kindMiddlewareResolved = "fiber_middleware_resolved"
	// kindRouteMiss/kindMiddlewareMiss are real, user-visible unresolved
	// entries: a route or Use() registration whose target identifier has no
	// matching function/method node anywhere in the batch.
	kindRouteMiss      = "fiber_route_handler"
	kindMiddlewareMiss = "fiber_middleware_target"
)

type fiberPlugin struct{}

func (fiberPlugin) Name() string { return "fiber" }

func (fiberPlugin) Requires(componentID string) []linkplugin.Capability {
	if componentID == "middleware" {
		return []linkplugin.Capability{linkplugin.CapContainment}
	}
	return nil
}

func (fiberPlugin) Link(ctx *linkplugin.LinkContext) (linkplugin.Result, error) {
	switch ctx.ComponentID {
	case "routes":
		return linkRoutes(ctx), nil
	case "middleware":
		return linkMiddleware(ctx), nil
	default:
		return linkplugin.Result{}, fmt.Errorf("fiber: unknown component %q", ctx.ComponentID)
	}
}

// linkRoutes resolves each fiber_route node's captured handler identifier to
// a real function/method node in the same batch, emitting a routes_to edge
// on success and a carrier fiber_route_resolved entry Reconcile later joins
// against middleware — or a real fiber_route_handler unresolved miss.
func linkRoutes(ctx *linkplugin.LinkContext) linkplugin.Result {
	var res linkplugin.Result
	for _, n := range ctx.Nodes {
		if n.Meta["pattern"] != "fiber_route" {
			continue
		}
		handlerName := trailingIdent(n.Meta["handler"])
		target := resolveFunctionNode(ctx.Nodes, n.File, handlerName)
		if target == nil {
			res.Unresolved = append(res.Unresolved, lpgraph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: handlerName, Kind: kindRouteMiss,
			})
			continue
		}
		res.Edges = append(res.Edges, lpgraph.Edge{
			ID:     fmt.Sprintf("fiber:routes_to:%s->%s", n.ID, target.ID),
			From:   n.ID,
			To:     target.ID,
			Type:   "routes_to",
			Method: n.Meta["method"],
			Path:   stripQuotes(n.Meta["path"]),
		})
		res.Unresolved = append(res.Unresolved, lpgraph.UnresolvedRef{
			Service: n.Service, File: n.File,
			Name: handlerName, Kind: kindRouteResolved, Targets: target.ID,
		})
	}
	return res
}

// linkMiddleware resolves each fiber_middleware_use node's captured
// middleware identifier to a function/method node the same way linkRoutes
// does, using CapContainment (declared in Requires("middleware")) to record
// the registration's enclosing scope alongside the resolved target — a real
// dial-back over the plugin subprocess boundary, exercising the capability
// end to end even though a bare `main.go` fixture usually has no enclosing
// class/struct scope to find.
func linkMiddleware(ctx *linkplugin.LinkContext) linkplugin.Result {
	var res linkplugin.Result

	var scopeOf map[string]linkplugin.Scope
	if ctx.Containment != nil {
		scopeOf = ctx.Containment.BulkResolve(ctx.Files)
	}

	for _, n := range ctx.Nodes {
		if n.Meta["pattern"] != "fiber_middleware_use" {
			continue
		}
		mwName := trailingIdent(n.Meta["middleware"])
		target := resolveFunctionNode(ctx.Nodes, n.File, mwName)
		if target == nil {
			res.Unresolved = append(res.Unresolved, lpgraph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: mwName, Kind: kindMiddlewareMiss,
			})
			continue
		}
		targets := target.ID
		if scope, ok := scopeOf[n.File]; ok {
			targets = targets + "|scope=" + scope.Kind
		}
		res.Unresolved = append(res.Unresolved, lpgraph.UnresolvedRef{
			Service: n.Service, File: n.File,
			Name: mwName, Kind: kindMiddlewareResolved, Targets: targets,
		})
	}
	return res
}

// resolveFunctionNode finds a function/method node named label among nodes,
// preferring one declared in the same file as the reference site (the
// common case) and falling back to any file in the current batch (a
// cross-file registration, e.g. handlers declared in a separate package
// file within the same service).
func resolveFunctionNode(nodes []lpgraph.Node, file, label string) *lpgraph.Node {
	var fallback *lpgraph.Node
	for i := range nodes {
		n := &nodes[i]
		if n.Type != nodeTypeFunction || n.Label != label {
			continue
		}
		if n.File == file {
			return n
		}
		if fallback == nil {
			fallback = n
		}
	}
	return fallback
}

func trailingIdent(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "."); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func stripQuotes(s string) string {
	return strings.Trim(s, `"`)
}

// unresolvedKey mirrors internal/indexer's unresolvedRefKey exactly — the
// Result.Retract key format (Kind, File, Name) both sides of the wire must
// agree on since a plugin's Retract list crosses an RPC boundary and can't
// carry a Go closure or map reference (see the plan's Result.Retract doc).
func unresolvedKey(kind, file, name string) string {
	return kind + "\x00" + file + "\x00" + name
}

// Reconcile joins linkRoutes' and linkMiddleware's carrier entries by file:
// every handler resolved in a file --calls--> every middleware resolved in
// that same file, mirroring LinkGinMiddleware's handler-calls-guard
// direction (internal/linker/gin_middleware.go) so the same impact/context/
// trace traversal that already answers "what does this route call" answers
// "what guards this route" for a Fiber service too. The carrier entries
// themselves are retracted — they were a join key, never a real miss.
func (fiberPlugin) Reconcile(ctx *linkplugin.ReconcileContext) (linkplugin.Result, error) {
	routes := ctx.ComponentResults["routes"]
	middleware := ctx.ComponentResults["middleware"]

	handlersByFile := map[string][]string{}
	var retract []string
	for _, u := range routes.Unresolved {
		if u.Kind != kindRouteResolved {
			continue
		}
		handlersByFile[u.File] = append(handlersByFile[u.File], u.Targets)
		retract = append(retract, unresolvedKey(u.Kind, u.File, u.Name))
	}

	middlewareByFile := map[string][]string{}
	for _, u := range middleware.Unresolved {
		if u.Kind != kindMiddlewareResolved {
			continue
		}
		// Targets may carry a trailing "|scope=..." annotation from
		// linkMiddleware's containment lookup — strip it back to a bare
		// node ID before using it as an edge endpoint.
		mwID := u.Targets
		if i := strings.Index(mwID, "|"); i >= 0 {
			mwID = mwID[:i]
		}
		middlewareByFile[u.File] = append(middlewareByFile[u.File], mwID)
		retract = append(retract, unresolvedKey(u.Kind, u.File, u.Name))
	}

	var res linkplugin.Result
	res.Retract = retract
	for file, handlerIDs := range handlersByFile {
		for _, mwID := range middlewareByFile[file] {
			for _, h := range handlerIDs {
				res.Edges = append(res.Edges, lpgraph.Edge{
					ID:   fmt.Sprintf("fiber:calls:%s->%s", h, mwID),
					From: h,
					To:   mwID,
					Type: "calls",
					Meta: map[string]string{"guarded_by": "fiber_middleware"},
				})
			}
		}
	}
	return res, nil
}

func main() {
	linkplugin.Serve(fiberPlugin{})
}
