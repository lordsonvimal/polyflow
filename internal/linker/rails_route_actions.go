package linker

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/railsinflect"
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
// every Rails handler fell through that gate: 0 of 851 on the juniper
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
func LinkRailsRouteActions(nodes []graph.Node, allEdges []graph.Edge) ([]graph.Edge, []graph.UnresolvedRef) {
	idx := newControllerActionIndex(nodes, allEdges)

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

		names := []string{resource}
		if alt, ok := railsPluralController(n, resource); ok {
			names = append(names, alt)
		}

		var (
			calleeID  string
			found     bool
			ambiguous []string
		)
		for _, name := range names {
			if calleeID, found = idx.lookup(n.Service, namespace, name, action); found {
				break
			}
		}
		// Tier RA: only once the action is absent from the controller's own file
		// do we ask what it inherits. Local first is not an optimisation — a
		// controller that overrides an inherited action must reach its override.
		if !found {
			for _, name := range names {
				var amb []string
				if calleeID, found, amb = idx.lookupInherited(n.Service, namespace, name, action); found {
					break
				}
				if len(amb) > 0 {
					ambiguous = amb
					break
				}
			}
		}
		if !found {
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: n.Service,
				File:    n.File,
				Line:    n.Line,
				Name:    resource + "#" + action,
				Kind:    UnresolvedRailsRouteAction,
				Targets: strings.Join(ambiguous, "\n"),
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

// railsPluralController is Tier CR: the second and last controller-name
// candidate a Rails route gets, tried only after the name as written misses.
//
// Rails maps the *singular* `resource :session` onto the *plural*
// SessionsController — a singleton resource is singular in the URL and in the
// declaration, but its controller is not. Resolving the declaration's name
// verbatim looks for session_controller.rb, which does not exist, so on cedar
// every action of all 19 singular resources ledgered while the controller sat
// on disk one "s" away.
//
// Two guards, both narrow on purpose:
//
//   - Only `resource_style == "singular"`. A plural `resources` declaration
//     already spells its controller correctly, and inflecting it again would
//     start guessing at controllers for the legitimately-dead routes that make
//     up most of the unresolved ledger (a bare `resources :widgets` mints all
//     seven REST routes whether or not the controller implements them).
//   - Never when the route named its controller outright. `controller:` is an
//     exact basename, already in Meta["resource"]; inflecting it would turn a
//     stated target into a guessed one.
//
// A wrong plural finds nothing and falls through to the ledger, so the cost of
// an inflection miss is a missing edge, never a wrong one.
func railsPluralController(n *graph.Node, resource string) (string, bool) {
	if n.Meta["resource_style"] != "singular" || n.Meta["controller_explicit"] != "" {
		return "", false
	}
	plural := railsinflect.Pluralize(resource)
	if plural == resource {
		return "", false
	}
	return plural, true
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
		if moduleKnown {
			return action, resource, explicitModule, true
		}
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
// This is load-bearing rather than cosmetic: orion has both
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

	// Tier RA. classAt: service \x00 controllerPath → the class nodes declared
	// in that controller file, sorted by ID. ancestors: class node ID → the
	// classes and modules it inherits or includes, sorted. methodsOf: class node
	// ID → declared method label → node ID.
	classAt   map[string][]string
	ancestors map[string][]string
	methodsOf map[string]map[string]string
}

// maxControllerAncestorHops bounds the ancestor walk. Every one of the 24
// inherited actions cedar resolves sits at hop 1 — a controller and its base
// class, or a controller and the concern it includes — so 4 is slack, not a
// working depth. The bound exists because `inherits` is a graph, not a tree:
// a diamond of concerns would otherwise be walked repeatedly.
const maxControllerAncestorHops = 4

func newControllerActionIndex(nodes []graph.Node, allEdges []graph.Edge) *controllerActionIndex {
	idx := &controllerActionIndex{
		byPath:    map[string]string{},
		classAt:   map[string][]string{},
		ancestors: map[string][]string{},
		methodsOf: map[string]map[string]string{},
	}
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

	idx.buildAncestry(nodes, allEdges)
	return idx
}

// buildAncestry collects the three tables the Tier RA fallback walks: which
// class a controller file declares, what that class inherits or includes, and
// which methods each class in the repo declares.
//
// `contains` and `inherits` are both already in the graph by the time this pass
// runs (the parser emits the first, LinkRubyTypeRelations the second, and it is
// ordered earlier in the pipeline), so none of this re-reads a file.
//
// Unlike Tier AT this deliberately does *not* filter Meta["via"] == "mixin".
// AT asked "is this class an ActiveRecord model", where a mixin answers nothing;
// RA asks "can this controller respond to `show`", and `include
// HomeCommonActions` is precisely how six of cedar's route actions arrive. Both
// relations put a method on the instance, which is the only property RA needs.
func (idx *controllerActionIndex) buildAncestry(nodes []graph.Node, allEdges []graph.Edge) {
	byID := make(map[string]*graph.Node, len(nodes))
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}

	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeClass || n.Language != "ruby" {
			continue
		}
		ctrlPath, ok := controllerPath(n.File)
		if !ok || !isControllerFile(n.File) {
			continue
		}
		key := n.Service + "\x00" + ctrlPath
		idx.classAt[key] = append(idx.classAt[key], n.ID)
	}

	for i := range allEdges {
		e := &allEdges[i]
		switch e.Type {
		case graph.EdgeTypeInherits:
			if byID[e.From] == nil || byID[e.To] == nil {
				continue
			}
			idx.ancestors[e.From] = append(idx.ancestors[e.From], e.To)
		case graph.EdgeTypeContains:
			parent, child := byID[e.From], byID[e.To]
			if parent == nil || child == nil || parent.Type != graph.NodeTypeClass {
				continue
			}
			if child.Type != graph.NodeTypeFunction && child.Type != graph.NodeTypeMethod {
				continue
			}
			// The same call-site-versus-declaration discriminator the direct
			// index uses: an inherited `before_action :audit` is not an action.
			if child.Meta["pattern"] != "" && child.Meta["end_line"] == "" {
				continue
			}
			m := idx.methodsOf[parent.ID]
			if m == nil {
				m = map[string]string{}
				idx.methodsOf[parent.ID] = m
			}
			// Node order is not stable across runs; keep the lowest ID so a class
			// that declares one label twice resolves the same way every index.
			if prev, exists := m[child.Label]; !exists || child.ID < prev {
				m[child.Label] = child.ID
			}
		}
	}

	for k := range idx.classAt {
		sort.Strings(idx.classAt[k])
	}
	for k := range idx.ancestors {
		sort.Strings(idx.ancestors[k])
	}
}

// lookup resolves a route against exactly one controller: the one its own
// namespace names. There is deliberately no fallback.
//
// The plan for this phase specified a namespace-relaxed second attempt —
// accept a unique <resource>_controller.rb found anywhere in the service — to
// catch routes whose controller sits outside their URL's shape. Measured on
// the juniper fleet it produced **5 edges, all 5 wrong**, and removing it
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

// lookupInherited is Tier RA: the same controller, asked what it inherits.
//
// It changes nothing about *which* controller serves a route — the namespace
// derivation and the singular-resource plural are still the only two candidate
// names, and there is still no name-similarity fallback. All it adds is that a
// controller whose action arrives from a base class or an included concern is
// no longer indistinguishable from one that has no such action at all. On cedar
// those were 24 of 99 ledger rows: `IntegrationApiBaseController` defines a
// generic `show`/`index` that thirteen API routes reach, `HomeCommonActions`
// supplies `show` and `get_tab_data` to the per-tenant home controllers, and
// two controllers subclass a sibling controller outright.
//
// The remaining 75 rows are the graph telling the truth and must stay: a bare
// `resources :async_operations` declares seven REST routes against a controller
// that implements `poll` and `delete_async_op`, and those five unimplemented
// actions are dead however far up the chain you look.
//
// Breadth-first, nearest ancestor wins, because that is Ruby's own rule. Within
// one hop it refuses instead: two ancestors at the same distance both defining
// the action is a question about Ruby's method resolution order — include order
// against superclass, `prepend` against `include` — that the graph does not
// record, and picking one would be a guess wearing an edge's confidence. The
// caller ledgers those with the candidates listed in Targets.
func (idx *controllerActionIndex) lookupInherited(service, namespace, resource, action string) (id string, found bool, ambiguous []string) {
	ctrlPath := resource
	if namespace != "" {
		ctrlPath = namespace + "/" + resource
	}
	frontier := idx.classAt[service+"\x00"+ctrlPath]
	if len(frontier) == 0 {
		return "", false, nil
	}

	seen := map[string]bool{}
	for _, c := range frontier {
		seen[c] = true
	}

	for hop := 0; hop < maxControllerAncestorHops && len(frontier) > 0; hop++ {
		var next []string
		for _, c := range frontier {
			for _, a := range idx.ancestors[c] {
				if seen[a] {
					continue
				}
				seen[a] = true
				next = append(next, a)
			}
		}

		var hits []string
		for _, a := range next {
			if mid, ok := idx.methodsOf[a][action]; ok {
				hits = append(hits, mid)
			}
		}
		sort.Strings(hits)
		hits = dedupeSortedIDs(hits)
		switch {
		case len(hits) == 1:
			return hits[0], true, nil
		case len(hits) > 1:
			return "", false, hits
		}
		frontier = next
	}
	return "", false, nil
}

func dedupeSortedIDs(in []string) []string {
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}
