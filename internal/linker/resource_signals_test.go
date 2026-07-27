package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// TestLinkResourceSignals verifies the http_client → signal flows_to join: a
// createResource accessor (meta.resource_fn) is connected to the fetch its
// loader function issues, resolved through the loader's fn→http_client calls edge.
func TestLinkResourceSignals(t *testing.T) {
	nodes := []graph.Node{
		{ID: "web:d.tsx:function:loadNode:3", Type: graph.NodeTypeFunction, Label: "loadNode", Service: "web", File: "d.tsx"},
		{ID: "web:d.tsx:http_client:GET:4", Type: graph.NodeTypeHTTPClient, Label: "GET /api/node", Service: "web", File: "d.tsx"},
		{ID: "web:d.tsx:variable:d:9", Type: graph.NodeTypeVariable, Label: "d", Service: "web", File: "d.tsx",
			Meta: map[string]string{"reactive": "resource", "resource_fn": "loadNode"}},
		// A signal whose loader isn't a resolvable fn — must be ledgered (no edge).
		{ID: "web:d.tsx:variable:e:10", Type: graph.NodeTypeVariable, Label: "e", Service: "web", File: "d.tsx",
			Meta: map[string]string{"reactive": "resource", "resource_fn": "missingLoader"}},
	}
	edges := []graph.Edge{
		{ID: "calls:loadNode->http", From: "web:d.tsx:function:loadNode:3", To: "web:d.tsx:http_client:GET:4", Type: graph.EdgeTypeCalls},
	}

	out := LinkResourceSignals(nodes, edges)

	if len(out) != 1 {
		t.Fatalf("want exactly 1 flows_to edge, got %d: %+v", len(out), out)
	}
	e := out[0]
	if e.Type != graph.EdgeTypeFlowsTo {
		t.Errorf("edge type = %q, want flows_to", e.Type)
	}
	if e.From != "web:d.tsx:http_client:GET:4" {
		t.Errorf("edge from = %q, want the http_client node", e.From)
	}
	if e.To != "web:d.tsx:variable:d:9" {
		t.Errorf("edge to = %q, want the resource signal node", e.To)
	}
	if e.Meta["via"] != "resource" {
		t.Errorf("edge via = %q, want resource", e.Meta["via"])
	}
}
