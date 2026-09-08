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

	edges, unresolved := LinkRailsRouteActions(nodes, nil)

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

	edges, unresolved := LinkRailsRouteActions(nodes, nil)

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

	edges, unresolved := LinkRailsRouteActions(nodes, nil)

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

	edges, unresolved := LinkRailsRouteActions(nodes, nil)

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

	edges, unresolved := LinkRailsRouteActions(nodes, nil)

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

	edges, unresolved := LinkRailsRouteActions(nodes, nil)

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

	edges, unresolved := LinkRailsRouteActions([]graph.Node{h, callSite}, nil)

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
	edges, unresolved := LinkRailsRouteActions([]graph.Node{goRoute}, nil)
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
	edges, unresolved := LinkRailsRouteActions([]graph.Node{h}, nil)
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

	edges, unresolved := LinkRailsRouteActions(nodes, nil)

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

	edges, unresolved := LinkRailsRouteActions(nodes, nil)

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

	edges, _ := LinkRailsRouteActions([]graph.Node{h, railsAction("orion", ctrl, "index", 10), dup}, nil)
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

	edges, unresolved := LinkRailsRouteActions(nodes, nil)

	require.Empty(t, unresolved)
	assert.Equal(t, []string{target.ID}, callTargets(edges, h.ID))
}

// singularResource builds the http_handler a singular `resource :x` mints:
// no `:id` in the path (there is only ever one of it) and no index action,
// with Meta["resource_style"] recording the declaration form.
func singularResource(svc, name, action, method, path string, extra ...string) graph.Node {
	meta := map[string]string{
		"action":            action,
		"resource":          name,
		"method":            method,
		"path":              path,
		"pattern":           "rest_resource_route",
		"resource_style":    "singular",
		"controller_module": "",
	}
	for i := 0; i+1 < len(extra); i += 2 {
		meta[extra[i]] = extra[i+1]
	}
	return railsHandler(svc, method+" "+path, 44, meta)
}

// TestLinkRailsRouteActions_SingularResourcePluralController is Tier CR's
// worked example. Rails routes the singular `resource :session` to the
// *plural* SessionsController; resolving the declaration's name verbatim
// looked for session_controller.rb, found nothing, and ledgered
// `session#create` while sessions_controller.rb sat on disk.
func TestLinkRailsRouteActions_SingularResourcePluralController(t *testing.T) {
	t.Parallel()
	const ctrl = "/repo/app/controllers/sessions_controller.rb"
	h := singularResource("orion", "session", "create", "POST", "/session")
	target := railsAction("orion", ctrl, "create", 9)

	edges, unresolved := LinkRailsRouteActions([]graph.Node{h, target}, nil)

	require.Empty(t, unresolved)
	assert.Equal(t, []string{target.ID}, callTargets(edges, h.ID))
}

// TestLinkRailsRouteActions_SingularResourceInNamespace pins that the plural
// candidate is tried *within* the route's namespace and not outside it. The
// namespace guard is the only thing keeping two same-named controllers apart,
// and a second candidate is a second chance to break it.
func TestLinkRailsRouteActions_SingularResourceInNamespace(t *testing.T) {
	t.Parallel()
	const nsCtrl = "/repo/app/controllers/admin/homes_controller.rb"
	const rootCtrl = "/repo/app/controllers/homes_controller.rb"
	h := singularResource("orion", "home", "show", "GET", "/admin/home",
		"controller_module", "admin")
	nsTarget := railsAction("orion", nsCtrl, "show", 4)

	edges, unresolved := LinkRailsRouteActions([]graph.Node{
		h, nsTarget, railsAction("orion", rootCtrl, "show", 4),
	}, nil)

	require.Empty(t, unresolved)
	assert.Equal(t, []string{nsTarget.ID}, callTargets(edges, h.ID),
		"the pluralized candidate must stay inside the route's own namespace")
}

// TestLinkRailsRouteActions_SingularNameAsWrittenWins covers the ordering: the
// name as declared is candidate one, and a controller that matches it is taken
// without inflecting. Rails' own `resource :widget` → WidgetsController is the
// common case, but an app that really did write widget_controller.rb must not
// be redirected to a differently-named neighbour.
func TestLinkRailsRouteActions_SingularNameAsWrittenWins(t *testing.T) {
	t.Parallel()
	const asWritten = "/repo/app/controllers/widget_controller.rb"
	const pluralCtrl = "/repo/app/controllers/widgets_controller.rb"
	h := singularResource("orion", "widget", "show", "GET", "/widget")
	target := railsAction("orion", asWritten, "show", 6)

	edges, unresolved := LinkRailsRouteActions([]graph.Node{
		h, target, railsAction("orion", pluralCtrl, "show", 6),
	}, nil)

	require.Empty(t, unresolved)
	assert.Equal(t, []string{target.ID}, callTargets(edges, h.ID))
}

// TestLinkRailsRouteActions_ExplicitControllerBeatsPlural is the guard the
// lookup doc comment already argued for in the plural case: `resources
// :studies, controller: "containers"` names its target outright, and a
// name-similarity guess has nothing to stand on against a stated fact. The
// route walker puts the option's basename in Meta["resource"] and flags it, so
// inflection is suppressed entirely — even though studies_controller.rb exists
// here and the pluralized candidate would have hit it.
func TestLinkRailsRouteActions_ExplicitControllerBeatsPlural(t *testing.T) {
	t.Parallel()
	const named = "/repo/app/controllers/containers_controller.rb"
	const decoy = "/repo/app/controllers/studies_controller.rb"
	h := singularResource("orion", "containers", "show", "GET", "/study",
		"controller_explicit", "true")
	target := railsAction("orion", named, "show", 11)

	edges, unresolved := LinkRailsRouteActions([]graph.Node{
		h, target, railsAction("orion", decoy, "show", 11),
	}, nil)

	require.Empty(t, unresolved)
	assert.Equal(t, []string{target.ID}, callTargets(edges, h.ID))
}

// TestLinkRailsRouteActions_PluralResourceNotInflected keeps CR off the
// unresolved entries that are *correctly* unresolved. Most of a real monolith's
// route ledger is bare `resources :x` declarations minting all seven REST
// routes against a controller that implements two — genuinely dead routes. A
// plural declaration already spells its controller, so it gets no second
// candidate and `widgetses_controller.rb` is never looked for.
func TestLinkRailsRouteActions_PluralResourceNotInflected(t *testing.T) {
	t.Parallel()
	h := railsHandler("orion", "GET /widgets/new", 30, map[string]string{
		"action":            "new",
		"resource":          "widgets",
		"path":              "/widgets/new",
		"pattern":           "rest_resource_route",
		"resource_style":    "plural",
		"controller_module": "",
	})
	nodes := []graph.Node{
		h,
		railsAction("orion", "/repo/app/controllers/widgets_controller.rb", "index", 3),
		railsAction("orion", "/repo/app/controllers/widgetses_controller.rb", "new", 3),
	}

	edges, unresolved := LinkRailsRouteActions(nodes, nil)

	assert.Empty(t, edges, "a dead REST route stays dead")
	require.Len(t, unresolved, 1)
	assert.Equal(t, "widgets#new", unresolved[0].Name)
}

// TestLinkRailsRouteActions_SingularWithoutPluralControllerStillLedgers is the
// regression the acceptance table's *lower* bound exists for: when the plural
// controller does not exist, the route must ledger as before and must not fall
// back onto some other controller that happens to serve the same action name.
func TestLinkRailsRouteActions_SingularWithoutPluralControllerStillLedgers(t *testing.T) {
	t.Parallel()
	h := singularResource("orion", "dashboard", "show", "GET", "/dashboard")
	nodes := []graph.Node{
		h,
		railsAction("orion", "/repo/app/controllers/reports_controller.rb", "show", 7),
	}

	edges, unresolved := LinkRailsRouteActions(nodes, nil)

	assert.Empty(t, edges)
	require.Len(t, unresolved, 1)
	assert.Equal(t, "dashboard#show", unresolved[0].Name,
		"the ledger records the name as declared, not the inflected guess")
	assert.Equal(t, UnresolvedRailsRouteAction, unresolved[0].Kind)
}

// TestLinkRailsRouteActions_SingularResourceAlreadyPlural: one of cedar's
// singular `resource` declarations is spelled plural already
// (`resource :settings`). The inflector's identity case is what stops it
// becoming `settingses`, and this pins that the route still resolves.
func TestLinkRailsRouteActions_SingularResourceAlreadyPlural(t *testing.T) {
	t.Parallel()
	const ctrl = "/repo/app/controllers/settings_controller.rb"
	h := singularResource("orion", "settings", "update", "PATCH", "/settings")
	target := railsAction("orion", ctrl, "update", 15)

	edges, unresolved := LinkRailsRouteActions([]graph.Node{h, target}, nil)

	require.Empty(t, unresolved)
	assert.Equal(t, []string{target.ID}, callTargets(edges, h.ID))
}

// TestLinkRailsRouteActions_OneEdgePerSingularRoute is the fan-out gate. CR
// adds a second lookup for the same handler; if both candidates could ever
// emit, a route would call two different controllers and the corpus' currently
// perfect route→action precision would quietly halve.
func TestLinkRailsRouteActions_OneEdgePerSingularRoute(t *testing.T) {
	t.Parallel()
	h := singularResource("orion", "home", "show", "GET", "/home")
	nodes := []graph.Node{
		h,
		railsAction("orion", "/repo/app/controllers/home_controller.rb", "show", 3),
		railsAction("orion", "/repo/app/controllers/homes_controller.rb", "show", 3),
	}

	edges, _ := LinkRailsRouteActions(nodes, nil)

	assert.Len(t, edges, 1, "one route, one action: %+v", edges)
}

// ── Tier RA: actions supplied by a base class or an included concern ────────
//
// Cedar's ledger held 99 unresolved route actions. Opening the controller named
// by 20 randomly-sampled rows found 6 whose action exists but is inherited, and
// 14 with no such action anywhere — the honest majority a bare `resources`
// declaration mints against a controller that implements two of seven verbs.
// These tests pin both halves: the 6 must resolve, the 14 must not.

// railsClass builds the class node the Ruby parser stamps for a controller or
// concern, and the `contains` edges from it to the methods it declares.
func railsClass(svc, file, name string, methods ...graph.Node) (graph.Node, []graph.Edge) {
	cls := graph.Node{
		ID:       svc + ":" + file + ":class:" + name + ":1",
		Type:     graph.NodeTypeClass,
		Label:    name,
		Service:  svc,
		File:     file,
		Line:     1,
		Language: "ruby",
	}
	var edges []graph.Edge
	for _, m := range methods {
		edges = append(edges, graph.Edge{
			ID:   "contains:" + cls.ID + "->" + m.ID,
			From: cls.ID,
			To:   m.ID,
			Type: graph.EdgeTypeContains,
		})
	}
	return cls, edges
}

func railsInherits(from, to graph.Node, via string) graph.Edge {
	return graph.Edge{
		ID:   "inherits:" + from.ID + "->" + to.ID,
		From: from.ID,
		To:   to.ID,
		Type: graph.EdgeTypeInherits,
		Meta: map[string]string{"via": via},
	}
}

// TestLinkRailsRouteActions_ActionFromIncludedConcern is cedar's
// HomeCommonActions case, six ledger rows on its own: three tenant namespaces
// each declare `resource :home`, and each of their controllers gets `show` and
// `get_tab_data` from one included module. The concern lives in
// app/controllers/concerns/, which is not a `_controller.rb` file, so the
// direct path index cannot see it at all.
func TestLinkRailsRouteActions_ActionFromIncludedConcern(t *testing.T) {
	t.Parallel()
	const ctrl = "/repo/app/controllers/pcda/homes_controller.rb"
	const concernFile = "/repo/app/controllers/concerns/home_common_actions.rb"

	show := railsAction("orion", concernFile, "show", 11)
	local := railsAction("orion", ctrl, "pusher_script", 7)
	ctrlCls, ctrlEdges := railsClass("orion", ctrl, "Pcda::HomesController", local)
	concernCls, concernEdges := railsClass("orion", concernFile, "HomeCommonActions", show)

	h := railsHandler("orion", "GET /pcda/home", 78, map[string]string{
		"action":         "show",
		"resource":       "home",
		"resource_style": "singular",
		"path":           "/pcda/home",
		"pattern":        "rest_resource_route",
	})
	nodes := []graph.Node{h, show, local, ctrlCls, concernCls}
	edges := append(append(ctrlEdges, concernEdges...),
		railsInherits(ctrlCls, concernCls, "mixin"))

	out, unresolved := LinkRailsRouteActions(nodes, edges)

	require.Len(t, out, 1)
	assert.Equal(t, show.ID, out[0].To, "the route must reach the concern's #show")
	assert.Empty(t, unresolved)
}

// TestLinkRailsRouteActions_ActionFromBaseClass is the single largest cedar
// group — 13 rows — where IntegrationApiBaseController implements a generic
// `index`/`show` and the per-resource controllers add only private helpers.
func TestLinkRailsRouteActions_ActionFromBaseClass(t *testing.T) {
	t.Parallel()
	const ctrl = "/repo/app/controllers/integration_api/v1/mappings_controller.rb"
	const base = "/repo/app/controllers/integration_api/v1/integration_api_base_controller.rb"

	index := railsAction("orion", base, "index", 45)
	findAll := railsAction("orion", ctrl, "find_all", 6)
	ctrlCls, ctrlEdges := railsClass("orion", ctrl, "MappingsController", findAll)
	baseCls, baseEdges := railsClass("orion", base, "IntegrationApiBaseController", index)

	h := railsHandler("orion", "GET /integration_api/v1/standards/:standard_id/mappings", 815, map[string]string{
		"action":   "index",
		"resource": "mappings",
		"path":     "/integration_api/v1/standards/:standard_id/mappings",
		"pattern":  "rest_resource_route",
	})
	nodes := []graph.Node{h, index, findAll, ctrlCls, baseCls}
	edges := append(append(ctrlEdges, baseEdges...),
		railsInherits(ctrlCls, baseCls, "superclass"))

	out, unresolved := LinkRailsRouteActions(nodes, edges)

	require.Len(t, out, 1)
	assert.Equal(t, index.ID, out[0].To)
	assert.Empty(t, unresolved)
}

// TestLinkRailsRouteActions_DeadRestActionsStayUnresolved is the test that
// stops RA over-matching, and it is the most important one in this file. Tier
// CR's post-mortem established that most of the ledger is correct: `resources
// :widgets` declares seven routes whether or not the controller implements
// them. Having a base class in the chain must not change that — nothing up
// there defines the missing six either.
//
// A tier that closes this ledger has started matching things that do not exist.
func TestLinkRailsRouteActions_DeadRestActionsStayUnresolved(t *testing.T) {
	t.Parallel()
	const ctrl = "/repo/app/controllers/api/v1/async_operations_controller.rb"
	const base = "/repo/app/controllers/api/v1/api_controller.rb"

	index := railsAction("orion", ctrl, "index", 4)
	authenticate := railsAction("orion", base, "authenticate", 3)
	ctrlCls, ctrlEdges := railsClass("orion", ctrl, "AsyncOperationsController", index)
	baseCls, baseEdges := railsClass("orion", base, "ApiController", authenticate)

	nodes := []graph.Node{index, authenticate, ctrlCls, baseCls}
	var handlers []graph.Node
	for _, action := range []string{"index", "show", "new", "edit", "create", "update", "destroy"} {
		h := railsHandler("orion", "ANY /api/v1/async_operations "+action, 324, map[string]string{
			"action":   action,
			"resource": "async_operations",
			"path":     "/api/v1/async_operations",
			"pattern":  "rest_resource_route",
		})
		handlers = append(handlers, h)
		nodes = append(nodes, h)
	}
	edges := append(append(ctrlEdges, baseEdges...),
		railsInherits(ctrlCls, baseCls, "superclass"))

	out, unresolved := LinkRailsRouteActions(nodes, edges)

	require.Len(t, out, 1, "only the implemented action links")
	assert.Equal(t, index.ID, out[0].To)
	assert.Equal(t, handlers[0].ID, out[0].From)
	assert.Len(t, unresolved, 6, "the six unimplemented REST actions must still ledger")
	for _, u := range unresolved {
		assert.Equal(t, UnresolvedRailsRouteAction, u.Kind)
		assert.Empty(t, u.Targets, "a plain miss had no candidates to list")
	}
}

// TestLinkRailsRouteActions_LocalOverrideBeatsInherited — the ancestor walk is
// a fallback, not a preference. A controller that overrides an inherited action
// must reach its own definition, or every API controller in cedar would trace
// into IntegrationApiBaseController's generic #show instead of its own.
func TestLinkRailsRouteActions_LocalOverrideBeatsInherited(t *testing.T) {
	t.Parallel()
	const ctrl = "/repo/app/controllers/integration_api/v1/standards_controller.rb"
	const base = "/repo/app/controllers/integration_api/v1/integration_api_base_controller.rb"

	own := railsAction("orion", ctrl, "show", 12)
	inherited := railsAction("orion", base, "show", 39)
	ctrlCls, ctrlEdges := railsClass("orion", ctrl, "StandardsController", own)
	baseCls, baseEdges := railsClass("orion", base, "IntegrationApiBaseController", inherited)

	h := railsHandler("orion", "GET /integration_api/v1/standards/:id", 771, map[string]string{
		"action":   "show",
		"resource": "standards",
		"path":     "/integration_api/v1/standards/:id",
		"pattern":  "rest_resource_route",
	})
	nodes := []graph.Node{h, own, inherited, ctrlCls, baseCls}
	edges := append(append(ctrlEdges, baseEdges...),
		railsInherits(ctrlCls, baseCls, "superclass"))

	out, unresolved := LinkRailsRouteActions(nodes, edges)

	require.Len(t, out, 1)
	assert.Equal(t, own.ID, out[0].To)
	assert.Empty(t, unresolved)
}

// TestLinkRailsRouteActions_AmbiguousAncestorsRefuse — a superclass and an
// included concern at the same distance both defining `show` is a question
// about Ruby's method resolution order, which the graph does not record.
// Refuse and list both, the same way two same-named controllers already do.
func TestLinkRailsRouteActions_AmbiguousAncestorsRefuse(t *testing.T) {
	t.Parallel()
	const ctrl = "/repo/app/controllers/widgets_controller.rb"

	fromBase := railsAction("orion", "/repo/app/controllers/base_controller.rb", "show", 4)
	fromConcern := railsAction("orion", "/repo/app/controllers/concerns/showable.rb", "show", 4)
	ctrlCls, _ := railsClass("orion", ctrl, "WidgetsController")
	baseCls, baseEdges := railsClass("orion", "/repo/app/controllers/base_controller.rb", "BaseController", fromBase)
	concernCls, concernEdges := railsClass("orion", "/repo/app/controllers/concerns/showable.rb", "Showable", fromConcern)

	h := railsHandler("orion", "GET /widgets/:id", 3, map[string]string{
		"action":   "show",
		"resource": "widgets",
		"path":     "/widgets/:id",
		"pattern":  "rest_resource_route",
	})
	nodes := []graph.Node{h, fromBase, fromConcern, ctrlCls, baseCls, concernCls}
	edges := append(append(baseEdges, concernEdges...),
		railsInherits(ctrlCls, baseCls, "superclass"),
		railsInherits(ctrlCls, concernCls, "mixin"))

	out, unresolved := LinkRailsRouteActions(nodes, edges)

	assert.Empty(t, out, "two candidates at one hop is not an answer")
	require.Len(t, unresolved, 1)
	assert.Equal(t, "widgets#show", unresolved[0].Name)
	assert.Contains(t, unresolved[0].Targets, fromBase.ID)
	assert.Contains(t, unresolved[0].Targets, fromConcern.ID)
}

// TestLinkRailsRouteActions_NearestAncestorWins — the same collision one hop
// apart is not a collision. Ruby resolves to the nearer definition, so a
// grandparent's #show must lose to a parent's.
func TestLinkRailsRouteActions_NearestAncestorWins(t *testing.T) {
	t.Parallel()
	near := railsAction("orion", "/repo/app/controllers/mid_controller.rb", "show", 4)
	far := railsAction("orion", "/repo/app/controllers/root_controller.rb", "show", 4)
	ctrlCls, _ := railsClass("orion", "/repo/app/controllers/widgets_controller.rb", "WidgetsController")
	midCls, midEdges := railsClass("orion", "/repo/app/controllers/mid_controller.rb", "MidController", near)
	rootCls, rootEdges := railsClass("orion", "/repo/app/controllers/root_controller.rb", "RootController", far)

	h := railsHandler("orion", "GET /widgets/:id", 3, map[string]string{
		"action":   "show",
		"resource": "widgets",
		"path":     "/widgets/:id",
		"pattern":  "rest_resource_route",
	})
	nodes := []graph.Node{h, near, far, ctrlCls, midCls, rootCls}
	edges := append(append(midEdges, rootEdges...),
		railsInherits(ctrlCls, midCls, "superclass"),
		railsInherits(midCls, rootCls, "superclass"))

	out, unresolved := LinkRailsRouteActions(nodes, edges)

	require.Len(t, out, 1)
	assert.Equal(t, near.ID, out[0].To)
	assert.Empty(t, unresolved)
}

// TestLinkRailsRouteActions_AncestorWalkStaysInNamespace — RA must not become
// the name-similarity fallback Tier CR removed. An ancestor is reached from the
// controller the namespace names; a same-named controller elsewhere in the tree
// is still not a candidate, even if its base class defines the action.
func TestLinkRailsRouteActions_AncestorWalkStaysInNamespace(t *testing.T) {
	t.Parallel()
	rootCtrlCls, _ := railsClass("orion", "/repo/app/controllers/users_controller.rb", "UsersController")
	base := railsAction("orion", "/repo/app/controllers/base_controller.rb", "new", 4)
	baseCls, baseEdges := railsClass("orion", "/repo/app/controllers/base_controller.rb", "BaseController", base)

	h := railsHandler("orion", "GET /api/v1/users/new", 46, map[string]string{
		"action":   "new",
		"resource": "users",
		"path":     "/api/v1/users/new",
		"pattern":  "rest_resource_route",
	})
	nodes := []graph.Node{h, base, rootCtrlCls, baseCls}
	edges := append(baseEdges, railsInherits(rootCtrlCls, baseCls, "superclass"))

	out, unresolved := LinkRailsRouteActions(nodes, edges)

	assert.Empty(t, out, "api/v1/users has no controller; the root one is not a substitute")
	require.Len(t, unresolved, 1)
	assert.Equal(t, "users#new", unresolved[0].Name)
}

// TestLinkRailsRouteActions_BeforeActionIsNotAnInheritedAction carries the
// direct index's call-site discriminator into the ancestor walk. A base class
// listing `before_action :audit` mints a pattern-derived function node with no
// end_line; inheriting it must not make `audit` a routable action.
func TestLinkRailsRouteActions_BeforeActionIsNotAnInheritedAction(t *testing.T) {
	t.Parallel()
	const base = "/repo/app/controllers/base_controller.rb"
	callSite := graph.Node{
		ID:       "orion:" + base + ":function:audit:2",
		Type:     graph.NodeTypeFunction,
		Label:    "audit",
		Service:  "orion",
		File:     base,
		Line:     2,
		Language: "ruby",
		Meta:     map[string]string{"pattern": "ruby_method_call"},
	}
	ctrlCls, _ := railsClass("orion", "/repo/app/controllers/widgets_controller.rb", "WidgetsController")
	baseCls, baseEdges := railsClass("orion", base, "BaseController", callSite)

	h := railsHandler("orion", "GET /widgets/audit", 3, map[string]string{
		"action":    "audit",
		"full_path": "/widgets/audit",
		"pattern":   "collection_verb_route",
	})
	nodes := []graph.Node{h, callSite, ctrlCls, baseCls}
	edges := append(baseEdges, railsInherits(ctrlCls, baseCls, "superclass"))

	out, unresolved := LinkRailsRouteActions(nodes, edges)

	assert.Empty(t, out, "a before_action invocation is not a def")
	assert.Len(t, unresolved, 1)
}
