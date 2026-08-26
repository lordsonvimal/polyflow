package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// railsHandler builds an http_handler node the way the Ruby route patterns
// stamp them: no Meta["handler"], the target named by convention instead.
func railsHandler(svc, label string, line int, meta map[string]string) graph.Node {
	return graph.Node{
		ID:       svc + ":config/routes.rb:http_handler:" + label + ":" + itoa(line),
		Type:     graph.NodeTypeHTTPHandler,
		Label:    label,
		Service:  svc,
		File:     "/repo/config/routes.rb",
		Line:     line,
		Language: "ruby",
		Meta:     meta,
	}
}

// railsAction builds a controller action method node. end_line is set because
// a declaration always has one — the discriminator that separates a `def` from
// a `before_action` call site.
func railsAction(svc, file, name string, line int) graph.Node {
	return graph.Node{
		ID:       svc + ":" + file + ":function:" + name + ":" + itoa(line),
		Type:     graph.NodeTypeFunction,
		Label:    name,
		Service:  svc,
		File:     file,
		Line:     line,
		Language: "ruby",
		Meta:     map[string]string{"end_line": itoa(line + 4)},
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func callTargets(edges []graph.Edge, fromID string) []string {
	var out []string
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.From == fromID {
			out = append(out, e.To)
		}
	}
	return out
}

// TestLinkRailsRouteActions_WorkedExample is the flow from the readiness plan:
// orion-vega-agent PUTs /client_api/v1/lros/:id, which `resources :lros`
// inside `namespace :client_api { namespace :v1 }` serves via
// ClientApi::V1::LrosController#update. Before this pass the chain ended at
// config/routes.rb and `impact --file lros_controller.rb` reported one file.
func TestLinkRailsRouteActions_WorkedExample(t *testing.T) {
	t.Parallel()
	const ctrl = "/repo/app/controllers/client_api/v1/lros_controller.rb"
	h := railsHandler("orion", "PUT /client_api/v1/lros/:id", 592, map[string]string{
		"action":   "update",
		"resource": "lros",
		"method":   "PUT",
		"path":     "/client_api/v1/lros/:id",
		"pattern":  "rest_resource_route",
	})
	nodes := []graph.Node{
		h,
		railsAction("orion", ctrl, "update", 58),
		railsAction("orion", ctrl, "create", 14),
	}

	edges, unresolved := LinkRailsRouteActions(nodes)

	require.Len(t, edges, 1)
	assert.Equal(t, nodes[1].ID, edges[0].To, "route must reach #update, not another action")
	assert.Equal(t, graph.EdgeTypeCalls, edges[0].Type)
	assert.Empty(t, unresolved)
}

// TestLinkRailsRouteActions_NamespaceDisambiguates is the case that makes the
// namespace derivation load-bearing rather than cosmetic: orion really does
// have both app/controllers/files_controller.rb and
// app/controllers/client_api/v1/files_controller.rb. A namespace-blind match is
// a coin flip between them, and the relaxed fallback must refuse.
//
// The route is also *nested* (/folders/:folder_id/files/:id), so the parent
// resource has to be dropped from the prefix while the namespace is kept —
// Rails puts a nested child's controller at the namespace level, not under the
// parent.
func TestLinkRailsRouteActions_NamespaceDisambiguates(t *testing.T) {
	t.Parallel()
	const nsCtrl = "/repo/app/controllers/client_api/v1/files_controller.rb"
	const rootCtrl = "/repo/app/controllers/files_controller.rb"

	h := railsHandler("orion", "PATCH /client_api/v1/folders/:folder_id/files/:id", 610, map[string]string{
		"action":   "update",
		"resource": "files",
		"path":     "/client_api/v1/folders/:folder_id/files/:id",
		"pattern":  "rest_resource_route",
	})
	nodes := []graph.Node{
		h,
		railsAction("orion", nsCtrl, "update", 40),
		railsAction("orion", rootCtrl, "update", 12),
	}

	edges, unresolved := LinkRailsRouteActions(nodes)

	require.Len(t, edges, 1)
	assert.Equal(t, nodes[1].ID, edges[0].To,
		"the namespaced controller serves a namespaced route, not the root one")
	assert.Empty(t, unresolved)
}

// TestLinkRailsRouteActions_AmbiguousRefuses is the guard against the
// LinkRubyTypeRelations failure mode (8952577), where `partial` confidence
// disguised 36 phantom edges as honest ambiguity. Two same-named controllers
// in namespaces neither of which the route names must produce no edge at all.
func TestLinkRailsRouteActions_AmbiguousRefuses(t *testing.T) {
	t.Parallel()
	h := railsHandler("orion", "GET /reports", 20, map[string]string{
		"action":   "index",
		"resource": "reports",
		"path":     "/reports",
		"pattern":  "rest_resource_route",
	})
	nodes := []graph.Node{
		h,
		railsAction("orion", "/repo/app/controllers/admin/reports_controller.rb", "index", 5),
		railsAction("orion", "/repo/app/controllers/client_api/v1/reports_controller.rb", "index", 9),
	}

	edges, unresolved := LinkRailsRouteActions(nodes)

	assert.Empty(t, edges, "two plausible controllers must not be guessed between")
	require.Len(t, unresolved, 1)
	assert.Equal(t, UnresolvedRailsRouteAction, unresolved[0].Kind)
	assert.Equal(t, "reports#index", unresolved[0].Name)
	assert.Equal(t, 20, unresolved[0].Line, "the ledger must point at the route, not the controller")
}

// TestLinkRailsRouteActions_APIControllerWithoutNewStaysUnresolved is the
// false-positive class found by auditing the first fleet run: `resources
// :users` inside an API namespace declares all seven REST routes, but an API
// controller implements neither `new` nor `edit` — those render HTML forms.
// The exact lookup correctly missed, and the relaxed fallback then wandered
// out of the namespace and linked /client_api/v1/users/new to the *root*
// UsersController#new, a different controller serving a different UI. It did
// this 13 times on orion.
//
// The rule: if a controller exists at the derived namespace, the route
// resolves there or not at all.
func TestLinkRailsRouteActions_APIControllerWithoutNewStaysUnresolved(t *testing.T) {
	t.Parallel()
	const apiCtrl = "/repo/app/controllers/client_api/v1/users_controller.rb"
	const rootCtrl = "/repo/app/controllers/users_controller.rb"

	h := railsHandler("orion", "GET /client_api/v1/users/new", 44, map[string]string{
		"action":   "new",
		"resource": "users",
		"path":     "/client_api/v1/users/new",
		"pattern":  "rest_resource_route",
	})
	nodes := []graph.Node{
		h,
		railsAction("orion", apiCtrl, "index", 10), // API controller exists…
		railsAction("orion", apiCtrl, "show", 20),  // …but declares no `new`
		railsAction("orion", rootCtrl, "new", 33),  // the HTML controller does
	}

	edges, unresolved := LinkRailsRouteActions(nodes)

	assert.Empty(t, edges, "a route with no implementation must not borrow another namespace's")
	require.Len(t, unresolved, 1)
	assert.Equal(t, "users#new", unresolved[0].Name)
}

// TestLinkRailsRouteActions_NoNameSimilarityFallback pins the removal of the
// namespace-relaxed fallback the phase plan originally specified. Measured on
// the fleet it emitted 5 edges and all 5 were wrong, because the cases it fires
// on are exactly the ones where Rails has overridden the convention:
// `resources :studies, controller: "containers"` should reach
// ContainersController, and matching on the resource *name* picked
// client_api/v1/studies_controller.rb instead.
//
// A unique same-named controller elsewhere in the service is not evidence.
func TestLinkRailsRouteActions_NoNameSimilarityFallback(t *testing.T) {
	t.Parallel()
	const elsewhere = "/repo/app/controllers/client_api/v1/studies_controller.rb"
	h := railsHandler("orion", "GET /studies", 126, map[string]string{
		"action":   "index",
		"resource": "studies", // routes.rb also says controller: "containers"
		"path":     "/studies",
		"pattern":  "rest_resource_route",
	})
	nodes := []graph.Node{h, railsAction("orion", elsewhere, "index", 12)}

	edges, unresolved := LinkRailsRouteActions(nodes)

	assert.Empty(t, edges, "a same-named controller in another namespace is not evidence")
	require.Len(t, unresolved, 1)
	assert.Equal(t, "studies#index", unresolved[0].Name)
}

// TestLinkRailsRouteActions_VerbRoutes covers the four verb families, which
// record no `resource` and prefix their action with a colon. The resource has
// to be read back off the path — past the trailing action segment, and past
// the `:id` for the member form.
func TestLinkRailsRouteActions_VerbRoutes(t *testing.T) {
	t.Parallel()
	const workspaces = "/repo/app/controllers/workspaces_controller.rb"
	const files = "/repo/app/controllers/client_api/v1/files_controller.rb"

	member := railsHandler("orion", "POST /studies/:study_id/workspaces/:id/subscribe", 300, map[string]string{
		"action":    ":subscribe",
		"full_path": "/studies/:study_id/workspaces/:id/subscribe",
		"path":      "/studies/:study_id/workspaces/:id/subscribe",
		"pattern":   "member_verb_route",
	})
	collection := railsHandler("orion", "POST /client_api/v1/files/copy", 410, map[string]string{
		"action":    ":copy",
		"full_path": "/client_api/v1/files/copy",
		"path":      "/client_api/v1/files/copy",
		"pattern":   "collection_verb_route",
	})
	nodes := []graph.Node{
		member, collection,
		railsAction("orion", workspaces, "subscribe", 77),
		railsAction("orion", files, "copy", 120),
	}

	edges, unresolved := LinkRailsRouteActions(nodes)

	require.Empty(t, unresolved)
	assert.Equal(t, []string{nodes[2].ID}, callTargets(edges, member.ID),
		"member route: strip :subscribe then :id, resource is workspaces")
	assert.Equal(t, []string{nodes[3].ID}, callTargets(edges, collection.ID),
		"collection route: strip :copy, resource is files under client_api/v1")
}

// TestLinkRailsRouteActions_BeforeActionIsNotAnAction pins the discriminator
// borrowed from linkControllerActions (rails_views.go:314). A pattern-derived
// node with no end_line is a call site — `before_action :restrict_access` —
// and a route must never link to the filter invocation instead of the def.
func TestLinkRailsRouteActions_BeforeActionIsNotAnAction(t *testing.T) {
	t.Parallel()
	const ctrl = "/repo/app/controllers/sessions_controller.rb"
	callSite := graph.Node{
		ID:       "orion:" + ctrl + ":function:destroy:3",
		Type:     graph.NodeTypeFunction,
		Label:    "destroy",
		Service:  "orion",
		File:     ctrl,
		Line:     3,
		Language: "ruby",
		Meta:     map[string]string{"pattern": "ruby_call"}, // no end_line
	}
	h := railsHandler("orion", "DELETE /sessions/:id", 15, map[string]string{
		"action":   "destroy",
		"resource": "sessions",
		"path":     "/sessions/:id",
		"pattern":  "rest_resource_route",
	})

	edges, unresolved := LinkRailsRouteActions([]graph.Node{h, callSite})

	assert.Empty(t, edges, "a call site is not a declaration")
	require.Len(t, unresolved, 1)
	assert.Equal(t, UnresolvedRailsRouteAction, unresolved[0].Kind)
}

// TestLinkRailsRouteActions_GoRoutesUntouched guards the split from
// LinkRouteHandlers: a Go route carries Meta["handler"] and is that pass's
// business. Handling it here too would double-wire it.
func TestLinkRailsRouteActions_GoRoutesUntouched(t *testing.T) {
	t.Parallel()
	goRoute := graph.Node{
		ID:       "maple-manager:router.go:http_handler:GET /config:12",
		Type:     graph.NodeTypeHTTPHandler,
		Label:    "GET /config",
		Service:  "maple-manager",
		File:     "/repo/internal/routes/router.go",
		Line:     12,
		Language: "go",
		Meta:     map[string]string{"handler": "appConfigHandler.SaveConfig", "action": "SaveConfig"},
	}
	edges, unresolved := LinkRailsRouteActions([]graph.Node{goRoute})
	assert.Empty(t, edges)
	assert.Empty(t, unresolved)
}

// TestLinkRailsRouteActions_HTTPVerbRouteIsSilent — a genuinely target-less
// http_verb_route (no `to:`, no `=>`, e.g. resolvable only by a lambda)
// records neither action nor resource. It must not be reported as unresolved:
// that would restate an unaddressable gap 61 times per index and re-inflate
// the very footer the ledger-hygiene phase exists to shrink. Since
// docs/rails-route-explicit-to-target-plan.md, a `to:`-carrying route no
// longer takes this path — see
// TestLinkRailsRouteActions_HTTPVerbRouteWithExplicitTarget.
func TestLinkRailsRouteActions_HTTPVerbRouteIsSilent(t *testing.T) {
	t.Parallel()
	h := railsHandler("orion", "GET /async_operations/poll", 88, map[string]string{
		"full_path": "/async_operations/poll",
		"method":    "GET",
		"path":      "/async_operations/poll",
		"pattern":   "http_verb_route",
	})
	edges, unresolved := LinkRailsRouteActions([]graph.Node{h})
	assert.Empty(t, edges)
	assert.Empty(t, unresolved)
}

// TestLinkRailsRouteActions_HTTPVerbRouteWithExplicitTarget is the worked
// example from docs/rails-route-explicit-to-target-plan.md: an explicit
// `to: "controller#action"` verb route, once the parser stamps
// action/resource from the `to:` value instead of the URL, must resolve the
// same way a rest_resource_route does.
func TestLinkRailsRouteActions_HTTPVerbRouteWithExplicitTarget(t *testing.T) {
	t.Parallel()
	const ctrl = "/repo/app/controllers/lyra_job_items_controller.rb"
	h := railsHandler("orion", "POST /queue_compute_dependencies", 378, map[string]string{
		"action":            "queue_compute_dependencies",
		"resource":          "lyra_job_items",
		"controller_module": "",
		"method":            "POST",
		"path":              "/queue_compute_dependencies",
		"pattern":           "http_verb_route",
	})
	target := railsAction("orion", ctrl, "queue_compute_dependencies", 63)
	nodes := []graph.Node{h, target}

	edges, unresolved := LinkRailsRouteActions(nodes)

	require.Empty(t, unresolved)
	assert.Equal(t, []string{target.ID}, callTargets(edges, h.ID))
}

// TestLinkRailsRouteActions_ExplicitTargetResourceNotInURL pins the
// linker-side fix in docs/rails-route-explicit-to-target-plan.md: a `to:`
// target's resource does not appear as a URL segment at all (that's the
// point of `to:` — it decouples the URL from the controller), so
// railsRouteTarget must check moduleKnown before searching the path for the
// resource, not after. Every other existing fixture's resource is a URL
// segment by construction, so this is the one case that pins the reorder.
func TestLinkRailsRouteActions_ExplicitTargetResourceNotInURL(t *testing.T) {
	t.Parallel()
	const ctrl = "/repo/app/controllers/admin/db_status_controller.rb"
	h := railsHandler("orion", "GET /x", 40, map[string]string{
		"action":            "index",
		"resource":          "db_status",
		"controller_module": "admin",
		"method":            "GET",
		"path":              "/x",
		"pattern":           "http_verb_route",
	})
	target := railsAction("orion", ctrl, "index", 5)
	nodes := []graph.Node{h, target}

	edges, unresolved := LinkRailsRouteActions(nodes)

	require.Empty(t, unresolved)
	assert.Equal(t, []string{target.ID}, callTargets(edges, h.ID))
}

// TestLinkRailsRouteActions_OneEdgePerRoute — a route serves exactly one
// action. Two outgoing edges would mean the index collapsed distinct
// controllers onto one label, the bug class 8d4f19d fixed on the Go side.
func TestLinkRailsRouteActions_OneEdgePerRoute(t *testing.T) {
	t.Parallel()
	const ctrl = "/repo/app/controllers/lros_controller.rb"
	h := railsHandler("orion", "GET /lros", 5, map[string]string{
		"action":   "index",
		"resource": "lros",
		"path":     "/lros",
		"pattern":  "rest_resource_route",
	})
	// Same action label declared twice in one file (Ruby can yield a function
	// and a method node for the same def).
	dup := railsAction("orion", ctrl, "index", 10)
	dup.Type = graph.NodeTypeMethod
	dup.ID += ":m"

	edges, _ := LinkRailsRouteActions([]graph.Node{h, railsAction("orion", ctrl, "index", 10), dup})
	assert.Len(t, edges, 1)
}

// TestLinkRailsRouteActions_DeviseForControllersOverride is Phase DV.1's
// worked example: `devise_for :users, controllers: { sessions: "sessions" }`
// synthesizes a devise_route handler with Meta{resource:"sessions",
// action:"create"} (internal/parser/ruby_route_paths.go's emitDeviseRoutes),
// which must resolve to SessionsController#create by the exact same
// by-convention mechanism a plain `resources` route already uses — DV.1
// deliberately needs zero changes here, only correct Meta shape.
func TestLinkRailsRouteActions_DeviseForControllersOverride(t *testing.T) {
	t.Parallel()
	const ctrl = "/repo/app/controllers/sessions_controller.rb"
	h := railsHandler("orion", "POST /users/sign_in", 12, map[string]string{
		"action":            "create",
		"resource":          "sessions",
		"method":            "POST",
		"path":              "/users/sign_in",
		"pattern":           "devise_route",
		"controller_module": "",
	})
	target := railsAction("orion", ctrl, "create", 20)
	nodes := []graph.Node{h, target}

	edges, unresolved := LinkRailsRouteActions(nodes)

	require.Empty(t, unresolved)
	assert.Equal(t, []string{target.ID}, callTargets(edges, h.ID))
}
