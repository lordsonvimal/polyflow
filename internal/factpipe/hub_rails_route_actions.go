package factpipe

import (
	"path/filepath"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/railsinflect"
)

// hub_rails_route_actions.go is the "rails_route_actions" hub provider (see
// hub.go) — the Tier FX FX.8 migration of internal/linker/
// rails_route_actions.go's LinkRailsRouteActions.
//
// FX.8.24's blocker diagnosis (docs/declarative-framework-pipeline-plan.md)
// framed this pass as needing a primitive that assembles one join key —
// namespace + "/" + resource — from two independently-varying node_meta
// columns at rule-eval time, and concluded no existing primitive does that.
// That framing is correct about the symptom but not the fix: a `.dl` rule
// does not need ONE joined string to compare two facts for equality, it needs
// the SAME two columns on both sides and a plain multi-column join — no
// concatenation anywhere. The real gap was never a join primitive; it was
// that neither side actually had (Namespace, Resource) as two clean, already
// -agreeing columns:
//
//   - The route side's node_meta("controller_module") is not always the
//     namespace: railsRouteTarget (ported below verbatim as rraRouteTarget)
//     only trusts it when the producer stamped it (moduleKnown); a verb route
//     with no explicit module falls back to reading the namespace off the
//     URL's path segments, and a member/collection verb route has no
//     node_meta("resource") at all — its resource is read off the path too.
//     That reconstruction is real per-node string logic, not a value already
//     sitting in one node_meta column, so it cannot be a `derive:` (which
//     only ever joins columns that are already there) — it is exactly what a
//     HubProvider is for: a plain Go function over the graph-so-far.
//   - The controller side has no (Namespace, Resource) columns at all — only
//     a File path. Splitting "app/controllers/client_api/v1/lros_controller.
//     rb" into ("client_api/v1", "lros") is pure path arithmetic
//     (controllerPath, ported below as rraControllerPath) with nowhere to run
//     except Go: path_transform's `capture: file` config in the one
//     FX.8.P5 test that exercises it is unwired to any real tree-sitter
//     capture (no pattern in this tree binds a `@file` capture — file_routes,
//     path_transform's other intended consumer, is not migrated either), and
//     even if it were, path_transform always prefixes its output with a
//     literal "/" (path_transform.go's applyPathTransform), which the route
//     side's node_meta("controller_module") does not — comparing the two
//     would need the very string surgery `derive:` is deliberately forbidden
//     from doing.
//
// So this hub computes BOTH sides' (Namespace, Resource) directly in Go,
// ported near-verbatim from the retired pass, and asserts them as facts a
// plain equi-join in rules/ruby/rails_route_actions.dl compares column-for-
// column. `derive:` (internal/factpipe/derive.go) was still built as approved
// generic infra per this session's design review — it has no consumer in
// this migration; see the plan doc's FX.8.24 entry for the full account.
//
// This hub also computes what a `derive:`-adjacent join could never reach at
// all: the leading-colon action spelling a verb route's tree-sitter capture
// keeps (":subscribe") is stripped once, in Go (every one of its 5 existing
// Go consumers already does the identical strings.TrimPrefix themselves —
// confirmed by grep across internal/parser, internal/deadcode and the
// retired pass — so this changes no observable behaviour, just moves the
// no-op-safe strip to one place), and the singular→plural controller-name
// retry (railsinflect.Pluralize, ported as rraPluralController) — the
// `inflect` verb only ever runs on a freshly captured tree-sitter node's text
// during Stage-1 extraction, never a node_meta value, so calling
// railsinflect.Pluralize directly in Go is the only way to reach it here too.
func init() { RegisterHub("rails_route_actions", railsRouteActionsHub) }

// rraRouteActionPred / rraCandResourcePred / rraRouteNsPred / rraCtrlPred are
// the fact predicates this hub asserts:
//
//	rra_route_action(H, Action)                colon-stripped action
//	rra_cand_resource(H, Name, Rank)            Rank 0 = as written, 1 = pluralized
//	rra_route_ns(H, Namespace)                  the module a route resolves against
//	rra_ctrl_ns_resource(Id, Namespace, Resource) any controller-file declaration
const (
	rraRouteActionPred  = "rra_route_action"
	rraCandResourcePred = "rra_cand_resource"
	rraRouteNsPred      = "rra_route_ns"
	rraCtrlPred         = "rra_ctrl_ns_resource"
)

func railsRouteActionsHub(nodes []graph.Node, files []string, _ string, _ []graph.LinkHint, _ graph.SchemaConfig, _ []graph.UnresolvedRef) []Fact {
	var out []Fact
	for i := range nodes {
		n := &nodes[i]
		if n.Language != "ruby" {
			continue
		}
		switch n.Type {
		case graph.NodeTypeHTTPHandler:
			out = append(out, rraRouteFacts(n)...)
		case graph.NodeTypeClass, graph.NodeTypeFunction, graph.NodeTypeMethod:
			out = append(out, rraControllerFacts(n)...)
		}
	}
	return out
}

// rraRouteFacts emits one route's action/namespace/candidate-resource facts,
// or nothing when the route carries no resolvable target at all (a bare
// http_verb_route with no `to:`, the documented parser gap — reporting it
// would just restate a known unaddressable case).
func rraRouteFacts(n *graph.Node) []Fact {
	action, resource, namespace, ok := rraRouteTarget(n)
	if !ok {
		return nil
	}
	origin := Origin{Kind: OriginPrimitive, File: n.File, Line: n.Line, Pattern: "rails_route_actions"}
	facts := []Fact{
		{Pred: rraRouteActionPred, Args: []Atom{Node(n.ID), Str(action)}, Origin: origin},
		{Pred: rraRouteNsPred, Args: []Atom{Node(n.ID), Str(namespace)}, Origin: origin},
		{Pred: rraCandResourcePred, Args: []Atom{Node(n.ID), Str(resource), Int(0)}, Origin: origin},
	}
	if alt, ok := rraPluralController(n, resource); ok {
		facts = append(facts, Fact{Pred: rraCandResourcePred, Args: []Atom{Node(n.ID), Str(alt), Int(1)}, Origin: origin})
	}
	return facts
}

// rraControllerFacts emits (Namespace, Resource) for one controller-file
// declaration: every class node (the inherited lookup's ancestor seed), and
// every function/method node that is a real declaration rather than a
// pattern-derived call site (`before_action :x` must not look like a
// declared action).
func rraControllerFacts(n *graph.Node) []Fact {
	if !rraIsControllerFile(n.File) {
		return nil
	}
	if n.Type != graph.NodeTypeClass && n.Meta["pattern"] != "" && n.Meta["end_line"] == "" {
		return nil
	}
	ctrlPath, ok := rraControllerPath(n.File)
	if !ok {
		return nil
	}
	ns, resource := rraSplitLast(ctrlPath)
	return []Fact{{
		Pred:   rraCtrlPred,
		Args:   []Atom{Node(n.ID), Str(ns), Str(resource)},
		Origin: Origin{Kind: OriginPrimitive, File: n.File, Line: n.Line, Pattern: "rails_route_actions"},
	}}
}

// rraSplitLast splits "client_api/v1/lros" into ("client_api/v1", "lros"), or
// ("", "lros") when there is no "/" — the controller-file counterpart of the
// route side's (controller_module, resource) pair.
func rraSplitLast(ctrlPath string) (namespace, resource string) {
	if i := strings.LastIndex(ctrlPath, "/"); i >= 0 {
		return ctrlPath[:i], ctrlPath[i+1:]
	}
	return "", ctrlPath
}

// rraPluralController is Tier CR, ported verbatim from the retired
// railsPluralController: the second and last controller-name candidate a
// singular `resource :x` declaration gets, tried only after the name as
// written misses, and never when the route names its controller outright.
func rraPluralController(n *graph.Node, resource string) (string, bool) {
	if n.Meta["resource_style"] != "singular" || n.Meta["controller_explicit"] != "" {
		return "", false
	}
	plural := railsinflect.Pluralize(resource)
	if plural == resource {
		return "", false
	}
	return plural, true
}

// rraRouteTarget recovers (action, resource, namespace) from a Rails
// http_handler's meta, ported verbatim from the retired railsRouteTarget.
func rraRouteTarget(n *graph.Node) (action, resource, namespace string, ok bool) {
	action = strings.TrimPrefix(n.Meta["action"], ":")
	if action == "" {
		return "", "", "", false
	}

	routePath := n.Meta["path"]
	if routePath == "" {
		routePath = n.Meta["full_path"]
	}
	segs := rraPathSegments(routePath)

	explicitModule, moduleKnown := n.Meta["controller_module"]

	if resource = n.Meta["resource"]; resource != "" {
		if moduleKnown {
			return action, resource, explicitModule, true
		}
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
		return action, resource, rraNamespace(segs[:cut]), true
	}

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
	return action, resource, rraNamespace(segs[:len(segs)-1]), true
}

// rraNamespace, ported verbatim from the retired railsNamespace.
func rraNamespace(prefix []string) string {
	var out []string
	for i := 0; i < len(prefix); i++ {
		if strings.HasPrefix(prefix[i], ":") {
			continue
		}
		if i+1 < len(prefix) && strings.HasPrefix(prefix[i+1], ":") {
			i++
			continue
		}
		out = append(out, prefix[i])
	}
	return strings.Join(out, "/")
}

func rraPathSegments(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// rraIsControllerFile / rraControllerPath, ported verbatim from
// internal/linker/rails_views.go's isControllerFile / controllerPath —
// duplicated rather than shared, the same call internal/factpipe's other hub
// providers make (FX.8.44's doc comment): internal/factpipe cannot import
// internal/linker.
func rraIsControllerFile(file string) bool {
	s := filepath.ToSlash(file)
	return strings.HasSuffix(s, "_controller.rb") && rraControllersMarkerIndex(s) >= 0
}

func rraControllerPath(file string) (string, bool) {
	s := filepath.ToSlash(file)
	i := rraControllersMarkerIndex(s)
	if i < 0 {
		return "", false
	}
	return strings.TrimSuffix(s[i+len("app/controllers/"):], "_controller.rb"), true
}

func rraControllersMarkerIndex(s string) int {
	const marker = "app/controllers/"
	i := strings.LastIndex(s, marker)
	if i < 0 {
		return -1
	}
	if i > 0 && s[i-1] != '/' {
		return -1
	}
	return i
}
