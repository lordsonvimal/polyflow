package pipeline_test

// FX.8.24 (2026-09-15): rails_route_actions — Tier FX migration of
// internal/linker/rails_route_actions.go's LinkRailsRouteActions, replaced by
// patterns/ruby/rails_route_actions.yaml + rules/ruby/rails_route_actions.dl,
// driven by the "rails_route_actions" hub provider
// (internal/factpipe/hub_rails_route_actions.go).
//
// This framework is hub-only (no `patterns:`/`facts:` block): every input is
// either the FX.1 bridge (node_meta/defines/ancestor_dist off graphSoFar) or
// the hub's own real-Go recomputation of (action, resource, namespace) and
// controller-file (namespace, resource). No source files are parsed, so
// these fixtures hand-build graph.Node/graph.Edge the same way the retired
// pass's own tests did — mirroring pusher_helper_calls_test.go's convention
// for a framework with no tree-sitter extraction of its own.

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func rraActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	return reg.Active([]deps.Dependency{{Name: "railties", Version: "7.1.0"}})
}

func rraHandler(svc, label string, line int, meta map[string]string) graph.Node {
	return graph.Node{
		ID: svc + ":config/routes.rb:http_handler:" + label, Type: graph.NodeTypeHTTPHandler,
		Label: label, Service: svc, File: "/repo/config/routes.rb", Line: line,
		Language: "ruby", Meta: meta,
	}
}

func rraAction(svc, file, name string, line int) graph.Node {
	return graph.Node{
		ID: svc + ":" + file + ":function:" + name, Type: graph.NodeTypeFunction,
		Label: name, Service: svc, File: file, Line: line, Language: "ruby",
		Meta: map[string]string{"end_line": "999"},
	}
}

func rraClass(svc, file, name string) graph.Node {
	return graph.Node{
		ID: svc + ":" + file + ":class:" + name, Type: graph.NodeTypeClass,
		Label: name, Service: svc, File: file, Line: 1, Language: "ruby",
	}
}

func rraContains(from, to graph.Node) graph.Edge {
	return graph.Edge{ID: "contains:" + from.ID + "->" + to.ID, From: from.ID, To: to.ID, Type: graph.EdgeTypeContains}
}

func rraInherits(from, to graph.Node, via string) graph.Edge {
	return graph.Edge{ID: "inherits:" + from.ID + "->" + to.ID, From: from.ID, To: to.ID, Type: graph.EdgeTypeInherits, Meta: map[string]string{"via": via}}
}

func rraTargets(edges []graph.Edge, from string) []string {
	var out []string
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.From == from {
			out = append(out, e.To)
		}
	}
	return out
}

func TestRailsRouteActionsRule_DirectMatch(t *testing.T) {
	const ctrl = "/repo/app/controllers/client_api/v1/lros_controller.rb"
	h := rraHandler("orion", "PUT /client_api/v1/lros/:id", 592, map[string]string{
		"action": "update", "resource": "lros", "path": "/client_api/v1/lros/:id",
		"controller_module": "client_api/v1",
	})
	update := rraAction("orion", ctrl, "update", 58)
	create := rraAction("orion", ctrl, "create", 14)
	snap := graph.Snapshot{Nodes: []graph.Node{h, update, create}}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := rraTargets(res.Edges, h.ID)
	if len(got) != 1 || got[0] != update.ID {
		t.Fatalf("got %v, want [%s]", got, update.ID)
	}
	if len(res.Unresolved) != 0 {
		t.Errorf("unresolved = %+v, want none", res.Unresolved)
	}
}

func TestRailsRouteActionsRule_NamespaceDisambiguates(t *testing.T) {
	const nsCtrl = "/repo/app/controllers/client_api/v1/files_controller.rb"
	const rootCtrl = "/repo/app/controllers/files_controller.rb"
	h := rraHandler("orion", "PATCH /files/:id", 610, map[string]string{
		"action": "update", "resource": "files", "path": "/client_api/v1/folders/:folder_id/files/:id",
		"controller_module": "client_api/v1",
	})
	nsTarget := rraAction("orion", nsCtrl, "update", 40)
	rootTarget := rraAction("orion", rootCtrl, "update", 12)
	snap := graph.Snapshot{Nodes: []graph.Node{h, nsTarget, rootTarget}}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := rraTargets(res.Edges, h.ID)
	if len(got) != 1 || got[0] != nsTarget.ID {
		t.Fatalf("got %v, want [%s] (the namespaced controller, not root)", got, nsTarget.ID)
	}
}

func TestRailsRouteActionsRule_AmbiguousDirectCollapsesToLowestID(t *testing.T) {
	// Two controllers in DIFFERENT namespaces, neither named by the route: no
	// candidate at all, not an ambiguity (the direct index is namespace-keyed).
	const ctrl = "/repo/app/controllers/reports_controller.rb"
	h := rraHandler("orion", "GET /reports", 20, map[string]string{
		"action": "index", "resource": "reports", "path": "/reports",
		"controller_module": "admin",
	})
	target := rraAction("orion", ctrl, "index", 5)
	snap := graph.Snapshot{Nodes: []graph.Node{h, target}}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Edges) != 0 {
		t.Errorf("edges = %+v, want none (namespace admin has no controller here)", res.Edges)
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Kind != "rails_route_action_unresolved" {
		t.Fatalf("unresolved = %+v, want one rails_route_action_unresolved row", res.Unresolved)
	}
	if res.Unresolved[0].Name != "reports#index" {
		t.Errorf("unresolved name = %q, want reports#index", res.Unresolved[0].Name)
	}
}

func TestRailsRouteActionsRule_VerbRouteActionColonStripped(t *testing.T) {
	const ctrl = "/repo/app/controllers/workspaces_controller.rb"
	h := rraHandler("orion", "POST /studies/:study_id/workspaces/:id/subscribe", 300, map[string]string{
		"action": ":subscribe", "full_path": "/studies/:study_id/workspaces/:id/subscribe",
	})
	target := rraAction("orion", ctrl, "subscribe", 77)
	snap := graph.Snapshot{Nodes: []graph.Node{h, target}}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := rraTargets(res.Edges, h.ID)
	if len(got) != 1 || got[0] != target.ID {
		t.Fatalf("got %v, want [%s] (member route: strip :subscribe then :id)", got, target.ID)
	}
}

func TestRailsRouteActionsRule_BeforeActionIsNotADeclaration(t *testing.T) {
	const ctrl = "/repo/app/controllers/sessions_controller.rb"
	callSite := graph.Node{
		ID: "orion:" + ctrl + ":function:destroy", Type: graph.NodeTypeFunction,
		Label: "destroy", Service: "orion", File: ctrl, Line: 3, Language: "ruby",
		Meta: map[string]string{"pattern": "ruby_call"}, // no end_line
	}
	h := rraHandler("orion", "DELETE /sessions/:id", 15, map[string]string{
		"action": "destroy", "resource": "sessions", "path": "/sessions/:id",
		"controller_module": "",
	})
	snap := graph.Snapshot{Nodes: []graph.Node{h, callSite}}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Edges) != 0 {
		t.Errorf("edges = %+v, want none (a call site is not a declaration)", res.Edges)
	}
	if len(res.Unresolved) != 1 {
		t.Fatalf("unresolved = %+v, want one row", res.Unresolved)
	}
}

func TestRailsRouteActionsRule_SingularResourcePluralController(t *testing.T) {
	const ctrl = "/repo/app/controllers/sessions_controller.rb"
	h := rraHandler("orion", "POST /session", 44, map[string]string{
		"action": "create", "resource": "session", "path": "/session",
		"resource_style": "singular", "controller_module": "",
	})
	target := rraAction("orion", ctrl, "create", 9)
	snap := graph.Snapshot{Nodes: []graph.Node{h, target}}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := rraTargets(res.Edges, h.ID)
	if len(got) != 1 || got[0] != target.ID {
		t.Fatalf("got %v, want [%s] (session -> SessionsController via pluralize retry)", got, target.ID)
	}
}

func TestRailsRouteActionsRule_ExplicitControllerBeatsPlural(t *testing.T) {
	const named = "/repo/app/controllers/containers_controller.rb"
	const decoy = "/repo/app/controllers/studies_controller.rb"
	h := rraHandler("orion", "GET /study", 44, map[string]string{
		"action": "show", "resource": "containers", "path": "/study",
		"resource_style": "singular", "controller_explicit": "true", "controller_module": "",
	})
	target := rraAction("orion", named, "show", 11)
	decoyTarget := rraAction("orion", decoy, "show", 11)
	snap := graph.Snapshot{Nodes: []graph.Node{h, target, decoyTarget}}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := rraTargets(res.Edges, h.ID)
	if len(got) != 1 || got[0] != target.ID {
		t.Fatalf("got %v, want [%s] (controller_explicit suppresses inflection)", got, target.ID)
	}
}

func TestRailsRouteActionsRule_ActionFromIncludedConcern(t *testing.T) {
	const ctrl = "/repo/app/controllers/pcda/homes_controller.rb"
	const concernFile = "/repo/app/controllers/concerns/home_common_actions.rb"

	show := rraAction("orion", concernFile, "show", 11)
	local := rraAction("orion", ctrl, "pusher_script", 7)
	ctrlCls := rraClass("orion", ctrl, "Pcda::HomesController")
	concernCls := rraClass("orion", concernFile, "HomeCommonActions")

	h := rraHandler("orion", "GET /pcda/home", 78, map[string]string{
		"action": "show", "resource": "home", "resource_style": "singular",
		"path": "/pcda/home", "controller_module": "pcda",
	})
	nodes := []graph.Node{h, show, local, ctrlCls, concernCls}
	edges := []graph.Edge{
		rraContains(ctrlCls, local), rraContains(concernCls, show),
		rraInherits(ctrlCls, concernCls, "mixin"),
	}
	snap := graph.Snapshot{Nodes: nodes, Edges: edges}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := rraTargets(res.Edges, h.ID)
	if len(got) != 1 || got[0] != show.ID {
		t.Fatalf("got %v, want [%s] (the route must reach the concern's #show)", got, show.ID)
	}
}

func TestRailsRouteActionsRule_DeadRestActionsStayUnresolved(t *testing.T) {
	const ctrl = "/repo/app/controllers/api/v1/async_operations_controller.rb"
	const base = "/repo/app/controllers/api/v1/api_controller.rb"

	index := rraAction("orion", ctrl, "index", 4)
	authenticate := rraAction("orion", base, "authenticate", 3)
	ctrlCls := rraClass("orion", ctrl, "AsyncOperationsController")
	baseCls := rraClass("orion", base, "ApiController")

	nodes := []graph.Node{index, authenticate, ctrlCls, baseCls}
	var handlerIDs []string
	for i, action := range []string{"index", "show", "new", "edit", "create", "update", "destroy"} {
		h := rraHandler("orion", "ANY /api/v1/async_operations "+action, 324+i, map[string]string{
			"action": action, "resource": "async_operations", "path": "/api/v1/async_operations",
			"controller_module": "api/v1",
		})
		nodes = append(nodes, h)
		handlerIDs = append(handlerIDs, h.ID)
	}
	edges := []graph.Edge{
		rraContains(ctrlCls, index), rraContains(baseCls, authenticate),
		rraInherits(ctrlCls, baseCls, "superclass"),
	}
	snap := graph.Snapshot{Nodes: nodes, Edges: edges}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Edges) != 1 || res.Edges[0].To != index.ID || res.Edges[0].From != handlerIDs[0] {
		t.Fatalf("edges = %+v, want exactly one, index only", res.Edges)
	}
	if len(res.Unresolved) != 6 {
		t.Fatalf("unresolved = %d rows, want 6 (the unimplemented REST actions)", len(res.Unresolved))
	}
}

func TestRailsRouteActionsRule_AmbiguousAncestorsLedger(t *testing.T) {
	const ctrl = "/repo/app/controllers/widgets_controller.rb"
	fromBase := rraAction("orion", "/repo/app/controllers/base_controller.rb", "show", 4)
	fromConcern := rraAction("orion", "/repo/app/controllers/concerns/showable.rb", "show", 4)
	ctrlCls := rraClass("orion", ctrl, "WidgetsController")
	baseCls := rraClass("orion", "/repo/app/controllers/base_controller.rb", "BaseController")
	concernCls := rraClass("orion", "/repo/app/controllers/concerns/showable.rb", "Showable")

	h := rraHandler("orion", "GET /widgets/:id", 3, map[string]string{
		"action": "show", "resource": "widgets", "path": "/widgets/:id", "controller_module": "",
	})
	nodes := []graph.Node{h, fromBase, fromConcern, ctrlCls, baseCls, concernCls}
	edges := []graph.Edge{
		rraContains(baseCls, fromBase), rraContains(concernCls, fromConcern),
		rraInherits(ctrlCls, baseCls, "superclass"),
		rraInherits(ctrlCls, concernCls, "mixin"),
	}
	snap := graph.Snapshot{Nodes: nodes, Edges: edges}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Edges) != 0 {
		t.Errorf("edges = %+v, want none (two candidates at one hop is not an answer)", res.Edges)
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Name != "widgets#show" {
		t.Fatalf("unresolved = %+v, want one widgets#show row", res.Unresolved)
	}
}

func TestRailsRouteActionsRule_NearestAncestorWins(t *testing.T) {
	near := rraAction("orion", "/repo/app/controllers/mid_controller.rb", "show", 4)
	far := rraAction("orion", "/repo/app/controllers/root_controller.rb", "show", 4)
	ctrlCls := rraClass("orion", "/repo/app/controllers/widgets_controller.rb", "WidgetsController")
	midCls := rraClass("orion", "/repo/app/controllers/mid_controller.rb", "MidController")
	rootCls := rraClass("orion", "/repo/app/controllers/root_controller.rb", "RootController")

	h := rraHandler("orion", "GET /widgets/:id", 3, map[string]string{
		"action": "show", "resource": "widgets", "path": "/widgets/:id", "controller_module": "",
	})
	nodes := []graph.Node{h, near, far, ctrlCls, midCls, rootCls}
	edges := []graph.Edge{
		rraContains(midCls, near), rraContains(rootCls, far),
		rraInherits(ctrlCls, midCls, "superclass"),
		rraInherits(midCls, rootCls, "superclass"),
	}
	snap := graph.Snapshot{Nodes: nodes, Edges: edges}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := rraTargets(res.Edges, h.ID)
	if len(got) != 1 || got[0] != near.ID {
		t.Fatalf("got %v, want [%s] (nearer ancestor wins)", got, near.ID)
	}
}

func TestRailsRouteActionsRule_LocalOverrideBeatsInherited(t *testing.T) {
	const ctrl = "/repo/app/controllers/integration_api/v1/standards_controller.rb"
	const base = "/repo/app/controllers/integration_api/v1/integration_api_base_controller.rb"

	own := rraAction("orion", ctrl, "show", 12)
	inherited := rraAction("orion", base, "show", 39)
	ctrlCls := rraClass("orion", ctrl, "StandardsController")
	baseCls := rraClass("orion", base, "IntegrationApiBaseController")

	h := rraHandler("orion", "GET /integration_api/v1/standards/:id", 771, map[string]string{
		"action": "show", "resource": "standards", "path": "/integration_api/v1/standards/:id",
		"controller_module": "integration_api/v1",
	})
	nodes := []graph.Node{h, own, inherited, ctrlCls, baseCls}
	edges := []graph.Edge{
		rraContains(ctrlCls, own), rraContains(baseCls, inherited),
		rraInherits(ctrlCls, baseCls, "superclass"),
	}
	snap := graph.Snapshot{Nodes: nodes, Edges: edges}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := rraTargets(res.Edges, h.ID)
	if len(got) != 1 || got[0] != own.ID {
		t.Fatalf("got %v, want [%s] (the controller's own override, not the base class)", got, own.ID)
	}
}

func TestRailsRouteActionsRule_OneEdgePerRoute(t *testing.T) {
	const ctrl = "/repo/app/controllers/lros_controller.rb"
	h := rraHandler("orion", "GET /lros", 5, map[string]string{
		"action": "index", "resource": "lros", "path": "/lros", "controller_module": "",
	})
	dup1 := rraAction("orion", ctrl, "index", 10)
	dup2 := rraAction("orion", ctrl, "index", 10)
	dup2.Type = graph.NodeTypeMethod
	dup2.ID += ":m"
	snap := graph.Snapshot{Nodes: []graph.Node{h, dup1, dup2}}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Edges) != 1 {
		t.Fatalf("edges = %+v, want exactly one", res.Edges)
	}
}

func TestRailsRouteActionsRule_GoRoutesUntouched(t *testing.T) {
	goRoute := graph.Node{
		ID: "svc:router.go:http_handler:GET /config", Type: graph.NodeTypeHTTPHandler,
		Label: "GET /config", Service: "svc", File: "/repo/internal/routes/router.go", Line: 12,
		Language: "go", Meta: map[string]string{"handler": "appConfigHandler.SaveConfig", "action": "SaveConfig"},
	}
	snap := graph.Snapshot{Nodes: []graph.Node{goRoute}}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Edges) != 0 || len(res.Unresolved) != 0 {
		t.Errorf("go route must be untouched, got edges=%+v unresolved=%+v", res.Edges, res.Unresolved)
	}
}

func TestRailsRouteActionsRule_HTTPVerbRouteNoTargetIsSilent(t *testing.T) {
	h := rraHandler("orion", "GET /async_operations/poll", 88, map[string]string{
		"full_path": "/async_operations/poll", "method": "GET", "path": "/async_operations/poll",
	})
	snap := graph.Snapshot{Nodes: []graph.Node{h}}

	res, err := pipeline.Run(rraActive(t), nil, snap)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Edges) != 0 || len(res.Unresolved) != 0 {
		t.Errorf("no action recorded must stay silent, got edges=%+v unresolved=%+v", res.Edges, res.Unresolved)
	}
}
