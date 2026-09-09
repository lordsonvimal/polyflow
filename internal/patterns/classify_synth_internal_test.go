package patterns

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier PS synthesizes two non-call shapes: a declaration that receives a
// package-typed parameter and a call that passes a package value across a
// function boundary. Both are bookkeeping — a declaration is not a call — and
// both must classify as inert nodes, the same discipline X.9 applies to the
// hand-written gin registrar patterns.
//
// The name matters because classifyPattern is name-driven and the generic
// heuristics below the explicit cases are hungry: `gin_routergroup_param_decl`
// contains "route", so without an explicit case it would mint an http_handler
// for every function that merely accepts a *gin.RouterGroup.
func TestClassifyPattern_SynthesizedBookkeepingIsInert(t *testing.T) {
	for _, name := range []string{
		"gin_routergroup_param_decl",
		"gin_context_param_decl",
		"gin_handler_method_param_decl",
		"router_router_param_decl",
		"gin_binding_arg_call",
		"router_binding_arg_call",
	} {
		nodeType, _ := classifyPattern(name)
		if nodeType != graph.NodeTypeVariable {
			t.Errorf("classifyPattern(%q) = %v, want %v — a synthesized declaration must not mint an endpoint",
				name, nodeType, graph.NodeTypeVariable)
		}
	}

	// The hand-written X.9 patterns keep their existing classification: the new
	// suffix rule generalizes them, it does not replace them.
	for _, name := range []string{"gin_group_registrar_func", "gin_group_registrar_call"} {
		if nodeType, _ := classifyPattern(name); nodeType != graph.NodeTypeVariable {
			t.Errorf("classifyPattern(%q) = %v, want %v", name, nodeType, graph.NodeTypeVariable)
		}
	}

	// And a real route registration is still a route: the suffix rule must not
	// have swallowed the shapes it sits next to.
	if nodeType, _ := classifyPattern("gin_route"); nodeType != graph.NodeTypeHTTPHandler {
		t.Errorf("classifyPattern(\"gin_route\") = %v, want %v", nodeType, graph.NodeTypeHTTPHandler)
	}
	if nodeType, _ := classifyPattern("router_route_group"); nodeType != graph.NodeTypeRouteGroup {
		t.Errorf("classifyPattern(\"router_route_group\") = %v, want %v", nodeType, graph.NodeTypeRouteGroup)
	}
}
