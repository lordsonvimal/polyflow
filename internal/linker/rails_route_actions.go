package linker

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// UnresolvedRailsRouteAction is the ledger kind for a Rails route whose
// controller action could not be pinned. Recorded rather than guessed: a route
// that links to the wrong action is worse than one that links to nothing,
// because the wrong action reads as authoritative.
const UnresolvedRailsRouteAction = "rails_route_action_unresolved"

// LinkRailsRouteActions emits `calls` edges from Rails http_handler nodes to
// the controller action method that serves them.
//
// LinkRouteHandlers (linker.go:29) cannot do this. It pins a route to its
// implementation through Meta["handler"] — the receiver-qualified string a Go
// route records ("baseImageHandler.SaveConfig") — and returns early for any
// handler without one. Rails routes never set that key; they carry an entirely
// different vocabulary (action / resource / verb / full_path / on), because a
// Rails route names its target by *convention* rather than by reference. So
// every Rails handler fell through that gate: 0 of 851 on the datascience
// fleet had a single outgoing edge, and every cross-service and frontend flow
// into a Ruby service terminated at config/routes.rb.
//
// The convention is recoverable from meta alone, no re-parsing:
//
//	resources :lros            in namespace client_api/v1
//	  → http_handler "PUT /client_api/v1/lros/:id"
//	    Meta{action:"update", resource:"lros", path:"/client_api/v1/lros/:id"}
//	  → ClientApi::V1::LrosController#update
//	    app/controllers/client_api/v1/lros_controller.rb:58
//
// Resolution is strictest-first and stops at the first hit. An ambiguous route
// emits no edge and one UnresolvedRef instead — LinkRubyTypeRelations (8952577)
// is the precedent for why: `partial` confidence there disguised 36 phantom
// edges as honest ambiguity.
//
// Must run after the parser has produced http_handler nodes and controller
// function nodes; ordering against the other link passes does not matter, as
// this reads nodes only.
func LinkRailsRouteActions(nodes []graph.Node) ([]graph.Edge, []graph.UnresolvedRef) {
	idx := newControllerActionIndex(nodes)

	var (
		edges      []graph.Edge
		unresolved []graph.UnresolvedRef
		seen       = map[string]bool{}
	)

	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPHandler || n.Language != "ruby" {
			continue
		}
		action, resource, namespace, ok := railsRouteTarget(n)
		if !ok {
			// No action recorded at all (http_verb_route: the target lives in an
			// unparsed `to:` / `=>` argument). Nothing to resolve against, and
			// reporting it as unresolved would just restate a known parser gap.
			continue
		}

		calleeID, found := idx.lookup(n.Service, namespace, resource, action)
		if !found {
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: n.Service,
				File:    n.File,
				Line:    n.Line,
				Name:    resource + "#" + action,
				Kind:    UnresolvedRailsRouteAction,
			})
			continue
		}

		// A route serves exactly one action. Two edges out of one handler would
		// mean the index collapsed distinct controllers, so dedupe on the source
		// as well as the pair.
		if seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		edges = append(edges, graph.Edge{
			ID:         fmt.Sprintf("calls:%s->%s", n.ID, calleeID),
			From:       n.ID,
			To:         calleeID,
			Type:       graph.EdgeTypeCalls,
			Confidence: graph.ConfidenceStatic,
		})
	}
	return edges, unresolved
}

// railsRouteTarget recovers (action, resource, namespace) from a Rails
// http_handler's meta. The two route families spell it differently.
//
//	rest_resource_route    Meta{action:"update", resource:"files",
//	                            path:"/client_api/v1/folders/:folder_id/files/:id"}
//	member_verb_route      Meta{action:":resolve",
//	                            full_path:"/studies/:study_id/issue_lists/:id/resolve"}
//	collection_verb_route  Meta{action:":copy", full_path:"/client_api/v1/files/copy"}
//
// A verb route records no resource, so it is read back off the path: the
// action segment is trailing, preceded by an optional `:id` for member routes,
// and the segment before that names the resource.
func railsRouteTarget(n *graph.Node) (action, resource, namespace string, ok bool) {
	action = strings.TrimPrefix(n.Meta["action"], ":")
	if action == "" {
		return "", "", "", false
	}

	routePath := n.Meta["path"]
	if routePath == "" {
		routePath = n.Meta["full_path"]
	}
	segs := pathSegments(routePath)

	// The route walker records the controller module it composed the route
	// under. Prefer it over re-deriving one from the URL: since C.1b the URL
	// also carries `scope` path prefixes, which contribute no module at all, so
	// `scope "app" { resources :studies }` reads as app/studies_controller.rb
	// to a path-derived guess and StudiesController to Rails.
	explicitModule, moduleKnown := n.Meta["controller_module"]

	if resource = n.Meta["resource"]; resource != "" {
		// The resource segment can repeat (/files/:id/files); the route's own
		// resource is the last one, everything before it is context.
		cut := -1
		for i := len(segs) - 1; i >= 0; i-- {
			if segs[i] == resource {
				cut = i
				break
			}
		}
		if cut < 0 {
			return "", "", "", false
		}
		if moduleKnown {
			return action, resource, explicitModule, true
		}
		return action, resource, railsNamespace(segs[:cut]), true
	}

	// Verb route: strip the trailing action segment, then a trailing dynamic
	// segment (`:id`) for the member form. What remains ends in the resource.
	if len(segs) > 0 && segs[len(segs)-1] == action {
		segs = segs[:len(segs)-1]
	}
	if len(segs) > 0 && strings.HasPrefix(segs[len(segs)-1], ":") {
		segs = segs[:len(segs)-1]
	}
	if len(segs) == 0 {
		return "", "", "", false
	}
	resource = segs[len(segs)-1]
	if strings.HasPrefix(resource, ":") {
		return "", "", "", false
	}
	if moduleKnown {
		return action, resource, explicitModule, true
	}
	return action, resource, railsNamespace(segs[:len(segs)-1]), true
}

// railsNamespace reduces the segments preceding a resource to the module
// namespace its controller lives in.
//
// The prefix mixes two things that look alike in a URL but mean opposite
// things to Rails. `namespace :client_api` puts the controller in a
// subdirectory; a *parent resource* in a nested route does not — the child's
// controller sits at the namespace level, not under the parent. The two are
// distinguishable because a parent resource is always followed by its dynamic
// key:
//
//	/client_api/v1/folders/:folder_id/files/:id  resource "files"
//	 └─ client_api, v1 → namespace   └─ folders/:folder_id → nested parent, dropped
//	 ⇒ app/controllers/client_api/v1/files_controller.rb
//
// This is load-bearing rather than cosmetic: nextGen has both
// app/controllers/files_controller.rb and
// app/controllers/client_api/v1/files_controller.rb, so a namespace-blind
// match would be a coin flip between them.
func railsNamespace(prefix []string) string {
	var out []string
	for i := 0; i < len(prefix); i++ {
		if strings.HasPrefix(prefix[i], ":") {
			continue // a dynamic key; its parent was dropped with it
		}
		if i+1 < len(prefix) && strings.HasPrefix(prefix[i+1], ":") {
			i++ // parent resource + its key
			continue
		}
		out = append(out, prefix[i])
	}
	return strings.Join(out, "/")
}

func pathSegments(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// controllerActionIndex maps a controller's on-disk path to the action methods
// it declares, so a route can be resolved by convention without touching disk.
type controllerActionIndex struct {
	// byPath: service \x00 controllerPath \x00 action → node ID
	byPath map[string]string
}

func newControllerActionIndex(nodes []graph.Node) *controllerActionIndex {
	idx := &controllerActionIndex{byPath: map[string]string{}}
	// Node order is not stable across runs, so collect and sort before taking
	// "the first" of anything — an index built in map-iteration order produced a
	// run-to-run edge flip once already (key_dynamic_raw, acbb20e).
	type entry struct{ pathKey, id string }
	var entries []entry

	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeMethod {
			continue
		}
		if !isControllerFile(n.File) {
			continue
		}
		// Same discriminator linkControllerActions uses (rails_views.go:314): a
		// pattern-derived function node with no end_line is a *call site*, not a
		// declaration. Without this, `before_action :restrict_access` at the top
		// of a controller is indistinguishable from a `def restrict_access` and
		// routes would link to the filter invocation.
		if n.Meta["pattern"] != "" && n.Meta["end_line"] == "" {
			continue
		}
		ctrlPath, ok := controllerPath(n.File)
		if !ok {
			continue
		}
		entries = append(entries, entry{
			pathKey: n.Service + "\x00" + ctrlPath + "\x00" + n.Label,
			id:      n.ID,
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].id < entries[j].id })

	for _, e := range entries {
		if _, exists := idx.byPath[e.pathKey]; !exists {
			idx.byPath[e.pathKey] = e.id
		}
	}
	return idx
}

// lookup resolves a route against exactly one controller: the one its own
// namespace names. There is deliberately no fallback.
//
// The plan for this phase specified a namespace-relaxed second attempt —
// accept a unique <resource>_controller.rb found anywhere in the service — to
// catch routes whose controller sits outside their URL's shape. Measured on
// the datascience fleet it produced **5 edges, all 5 wrong**, and removing it
// cost nothing else (581 → 576 wired). Both failure modes are cases where the
// convention genuinely does not apply, so a name-similarity guess has nothing
// to stand on:
//
//   - `resources :users` in an API namespace declares all seven REST routes,
//     but an API controller implements neither `new` nor `edit` — they render
//     HTML forms. The route has no implementation; the fallback linked 13 of
//     them to the *root* UsersController, a different controller serving a
//     different UI.
//   - `resources :studies, controller: "containers"` names its controller
//     outright, overriding the resource name the fallback keys on. It should
//     reach ContainersController; the fallback chose
//     client_api/v1/studies_controller.rb on the strength of the name alone.
//
// The second case is a real gap, but the answer is to parse the `controller:`
// option (phase A.5) so the route states its target, not to guess from a name
// that Rails has explicitly overridden.
func (idx *controllerActionIndex) lookup(service, namespace, resource, action string) (string, bool) {
	ctrlPath := resource
	if namespace != "" {
		ctrlPath = namespace + "/" + resource
	}
	id, ok := idx.byPath[service+"\x00"+ctrlPath+"\x00"+action]
	return id, ok
}
