package indexer

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// wantLinkPasses is the pipeline's expected shape (name + scope), in
// execution order. FR.5a/b's tripwire: a change here must be a deliberate
// edit to buildLinkPasses (reordering, adding, removing, or reclassifying a
// pass), never an accidental drift. scope classification is the result of
// the FR.5b audit documented in docs/fr5-linking-refactor-subplan.md's
// Classification table: scopeCrossService means the pass's correctness
// depends on seeing nodes from more than one service (confirmed by reading
// each matcher's use of node.Service); everything else only ever compares
// nodes within one service/file and is scopeSameServiceOnly.
var wantLinkPasses = []struct {
	name  string
	scope passScope
}{
	{"js_link", scopeSameServiceOnly},
	{"js_globals", scopeSameServiceOnly},
	{"js_type_relations", scopeSameServiceOnly},
	{"js_receiver_type_calls", scopeSameServiceOnly},
	{"ruby_type_relations", scopeSameServiceOnly},
	{"ruby_class_method_calls", scopeSameServiceOnly},
	{"ruby_receiver_type_calls", scopeSameServiceOnly},
	{"ruby_associations", scopeSameServiceOnly},
	{"rails_filters", scopeSameServiceOnly},
	{"ruby_mixin_methods", scopeSameServiceOnly},
	{"ruby_mixin_constants", scopeSameServiceOnly},
	{"ruby_sole_definer_calls", scopeSameServiceOnly},
	{"ruby_wrapper_url_call_sites", scopeSameServiceOnly},
	{"ruby_http_hosts", scopeSameServiceOnly},
	{"ruby_poly_path_sites", scopeSameServiceOnly},
	{"go_http_hosts", scopeSameServiceOnly},
	{"js_http_hosts", scopeSameServiceOnly},
	{"config_baseurl", scopeSameServiceOnly},
	{"route_handlers", scopeSameServiceOnly},
	{"ws_upgrade_route", scopeSameServiceOnly},
	{"grpc_handlers", scopeSameServiceOnly},
	{"rails_devise_default_routes", scopeSameServiceOnly},
	{"rails_route_actions", scopeSameServiceOnly},
	{"route_components", scopeSameServiceOnly},
	{"templ_components", scopeSameServiceOnly},
	{"templ_scripts", scopeSameServiceOnly},
	{"dom_definitions", scopeSameServiceOnly},
	{"dom_contracts", scopeSameServiceOnly},
	{"containment", scopeSameServiceOnly},
	{"ensure_scanned_files", scopeSameServiceOnly},
	{"js_lazy_import_calls", scopeSameServiceOnly},
	{"js_import_edges", scopeSameServiceOnly},
	{"js_api_wrapper_calls", scopeSameServiceOnly},
	{"stylesheet_imports", scopeSameServiceOnly},
	{"sprockets_assets", scopeSameServiceOnly},
	{"rails_views", scopeSameServiceOnly},
	{"ruby_import_edges", scopeSameServiceOnly},
	{"shell_invocation_edges", scopeSameServiceOnly},
	{"sql_reference_edges", scopeSameServiceOnly},
	{"datastores", scopeSameServiceOnly},
	{"tables", scopeSameServiceOnly},
	{"response_shapes", scopeCrossService},
	{"resource_signals", scopeSameServiceOnly},
	{"sse_clients", scopeSameServiceOnly},
	{"broker_hints", scopeCrossService},
	{"rails_nav_helpers", scopeSameServiceOnly},
	{"file_route_synthesis", scopeSameServiceOnly},
	{"load_contract_rules", scopeCrossService},
	{"apply_hints_and_enrich", scopeCrossService},
	{"gin_middleware", scopeSameServiceOnly},
	{"express_middleware", scopeSameServiceOnly},
	{"enrich_aliases", scopeCrossService},
	{"amqp_handshake", scopeCrossService},
	{"amqp_message_type_dispatch", scopeCrossService},
	{"contract_engine", scopeCrossService},
	{"sse_push", scopeCrossService},
	{"contract_coverage", scopeCrossService},
}

// TestBuildLinkPasses_MatchesPipelineShape proves buildLinkPasses builds the
// exact ordered, classified list Run() expects, without needing a real
// index run — the closures are constructed but never executed here.
func TestBuildLinkPasses_MatchesPipelineShape(t *testing.T) {
	passes := buildLinkPasses(&linkPipelineState{})
	got := make([]struct {
		name  string
		scope passScope
	}, len(passes))
	for i, p := range passes {
		got[i] = struct {
			name  string
			scope passScope
		}{p.name, p.scope}
	}
	assert.Equal(t, wantLinkPasses, got)
}

func node(id, service string) graph.Node {
	return graph.Node{ID: id, Service: service}
}

func edge(id, from, to string) graph.Edge {
	return graph.Edge{ID: id, From: from, To: to}
}

// TestFilterEdgesByService_NilTargetsIsNoOp proves the default (nil
// targetServices, what Run() sets today) leaves every scopeCrossService
// pass's output unchanged — the FR.5b regression guard.
func TestFilterEdgesByService_NilTargetsIsNoOp(t *testing.T) {
	edges := []graph.Edge{edge("e1", "n-a", "n-b"), edge("e2", "n-b", "n-c")}
	svc := map[string]string{"n-a": "frontend", "n-b": "backend", "n-c": "worker"}

	assert.Equal(t, edges, filterEdgesByService(edges, svc, nil))
	assert.Equal(t, edges, filterEdgesByService(edges, svc, []string{}))
}

// TestFilterEdgesByService_KeepsOnlyEdgesTouchingTarget proves a non-empty
// target set keeps an edge iff at least one endpoint's service is in the
// set, and drops edges entirely between two other, untouched services —
// this is what lets a future scoped relink (FR.5c) leave FR.3's merged rows
// for unrelated services untouched.
func TestFilterEdgesByService_KeepsOnlyEdgesTouchingTarget(t *testing.T) {
	svc := map[string]string{"n-a": "frontend", "n-b": "backend", "n-c": "worker"}
	edges := []graph.Edge{
		edge("frontend-backend", "n-a", "n-b"), // touches "backend": kept
		edge("backend-worker", "n-b", "n-c"),   // touches "backend": kept
		edge("frontend-only", "n-a", "n-a"),    // does not touch "backend": dropped
	}

	got := filterEdgesByService(edges, svc, []string{"backend"})

	assert.Equal(t, []graph.Edge{
		edge("frontend-backend", "n-a", "n-b"),
		edge("backend-worker", "n-b", "n-c"),
	}, got)
}

// TestLinkPipelineState_FilterByTargetServices proves the state-bound
// wrapper builds its node→service lookup from the pipeline's current
// allNodes (not a separately-passed map) and defers to
// filterEdgesByService.
func TestLinkPipelineState_FilterByTargetServices(t *testing.T) {
	st := &linkPipelineState{
		allNodes: []graph.Node{node("n-a", "frontend"), node("n-b", "backend"), node("n-c", "worker")},
	}
	edges := []graph.Edge{edge("e1", "n-a", "n-b"), edge("e2", "n-b", "n-c")}

	assert.Equal(t, edges, st.filterByTargetServices(edges), "nil targetServices must be a no-op")

	st.targetServices = []string{"worker"}
	assert.Equal(t, []graph.Edge{edge("e2", "n-b", "n-c")}, st.filterByTargetServices(edges))
}
