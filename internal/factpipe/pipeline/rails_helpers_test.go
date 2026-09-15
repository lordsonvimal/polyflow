package pipeline_test

// FX.8.7 (2026-09-15): rails_helpers — internal/linker/rails_helpers.go's
// retired ResolveRailsNavHelpers, migrated onto patterns/ruby/
// rails_helpers.yaml + rules/ruby/rails_helpers.dl, driven by the
// rails_helper_routes hub provider (internal/factpipe/hub_rails_helpers.go).
// Hand-built graph fixtures (no file I/O, no tree-sitter parse needed — the
// hub reads node_meta straight off the graph-so-far), porting the retired
// Go test's own fixtures. Two documented, deliberate divergences from the
// retired Go (see patterns/ruby/rails_helpers.yaml's package doc): the
// unresolved/collision ledger dedups per client-site here, not per
// (service, helper) name, since no cedar corpus fixture pins this pass's
// ledger byte-for-byte.

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func rhActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("rails_helpers")
	if fw == nil {
		t.Fatal("rails_helpers framework not embedded")
	}
	return []*pipeline.Framework{fw}
}

func rhRouteNode(id, svc, file string, line int, helper, method, path string) graph.Node {
	return graph.Node{
		ID: id, Type: graph.NodeTypeHTTPHandler, Service: svc, File: file, Line: line, Language: "ruby",
		Meta: map[string]string{"pattern": "rest_resource_route", "route_helper": helper, "method": method, "path": path},
	}
}

func rhNavNode(id, svc, file string, line int, helper string) graph.Node {
	return graph.Node{
		ID: id, Type: graph.NodeTypeHTTPClient, Service: svc, File: file, Line: line, Language: "ruby",
		Label: "nav_link_rails_helper",
		Meta:  map[string]string{"pattern": "nav_link_rails_helper", "helper": helper, "nav_link": "true", "method": "GET"},
	}
}

func TestRailsHelpersRule_Basic(t *testing.T) {
	nodes := []graph.Node{
		rhRouteNode("r1", "svc", "routes.rb", 1, "reports", "GET", "/app/reports"),
		rhNavNode("c1", "svc", "views/index.erb", 5, "reports_path"),
	}
	res, err := pipeline.Run(rhActive(t), nil, graph.Snapshot{Nodes: nodes})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Unresolved) != 0 {
		t.Errorf("expected 0 unresolved, got %d: %+v", len(res.Unresolved), res.Unresolved)
	}
	var found bool
	for _, n := range res.Nodes {
		if n.ID == "c1" && n.Meta["path"] == "/app/reports" && n.Meta["method"] == "GET" {
			found = true
			if n.Meta["via"] == "rails_helper_candidate" {
				t.Error("a single match must not be marked as a fan-out candidate")
			}
			if _, ok := n.Meta["helper"]; ok {
				t.Error("resolved node must not carry the unresolved helper marker")
			}
			if n.Label != "GET /app/reports" {
				t.Errorf("label = %q, want GET /app/reports", n.Label)
			}
		}
	}
	if !found {
		t.Errorf("no resolved node for reports_path; got: %+v", res.Nodes)
	}
	if len(res.Nodes) != 1 {
		t.Errorf("expected exactly 1 resolved node, got %d: %+v", len(res.Nodes), res.Nodes)
	}
}

func TestRailsHelpersRule_Unresolved(t *testing.T) {
	nodes := []graph.Node{
		rhNavNode("c1", "svc", "views/index.erb", 5, "unknown_helper_path"),
	}
	res, err := pipeline.Run(rhActive(t), nil, graph.Snapshot{Nodes: nodes})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Nodes) != 0 {
		t.Errorf("expected 0 resolved nodes for unknown helper, got %d", len(res.Nodes))
	}
	if len(res.Unresolved) != 1 {
		t.Fatalf("expected 1 unresolved entry, got %d: %+v", len(res.Unresolved), res.Unresolved)
	}
	if res.Unresolved[0].Kind != "rails_helper_unresolved" {
		t.Errorf("kind = %q, want rails_helper_unresolved", res.Unresolved[0].Kind)
	}
	if res.Unresolved[0].Name != "unknown_helper_path" {
		t.Errorf("name = %q, want unknown_helper_path", res.Unresolved[0].Name)
	}
}

func TestRailsHelpersRule_FanOutKeepsOriginalID(t *testing.T) {
	nodes := []graph.Node{
		rhRouteNode("r1", "svc", "routes.rb", 1, "session", "POST", "/session"),
		rhRouteNode("r2", "svc", "routes.rb", 2, "session", "DELETE", "/session"),
		rhNavNode("c1", "svc", "views/new.erb", 11, "session_path"),
	}
	res, err := pipeline.Run(rhActive(t), nil, graph.Snapshot{Nodes: nodes})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	ids := map[string]graph.Node{}
	for _, n := range res.Nodes {
		if _, dup := ids[n.ID]; dup {
			t.Errorf("duplicate candidate ID %q", n.ID)
		}
		ids[n.ID] = n
	}
	if _, ok := ids["c1"]; !ok {
		t.Fatalf("original node c1 was not among the candidates; got %v", nodeIDs(res.Nodes))
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 candidates for the two /session routes, got %d: %v", len(ids), nodeIDs(res.Nodes))
	}

	methods := map[string]bool{}
	for _, n := range ids {
		methods[n.Meta["method"]] = true
		if n.Meta["path"] != "/session" {
			t.Errorf("candidate %s has path %q, want /session", n.ID, n.Meta["path"])
		}
		if n.Meta["via"] != "rails_helper_candidate" {
			t.Errorf("candidate %s not marked as a fan-out candidate", n.ID)
		}
	}
	if !methods["POST"] || !methods["DELETE"] {
		t.Errorf("expected one candidate per method, got %v", methods)
	}

	var collisions int
	for _, u := range res.Unresolved {
		if u.Kind == "rails_helper_collision" {
			collisions++
		}
	}
	if collisions == 0 {
		t.Error("expected at least 1 rails_helper_collision ledger entry")
	}
}

func nodeIDs(nodes []graph.Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}
