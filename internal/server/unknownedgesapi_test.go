package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestHandleUnknownEdges_DefaultsToUnknownConfidence(t *testing.T) {
	nodes := append(testNodes(), &graph.Node{
		ID: "n4", Type: graph.NodeTypeHTTPClient, Label: "fetchStatus", Service: "auth", File: "auth/client.go", Line: 30, Language: "go",
	}, &graph.Node{
		ID: "unresolved", Type: graph.NodeTypeService, Label: "unresolved", Service: "",
	})
	edges := append(testEdges(), &graph.Edge{
		ID: "e_unknown", From: "n4", To: "unresolved", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceUnknown,
	})
	srv := buildTestServer(t, nodes, edges)

	req := httptest.NewRequest("GET", "/api/unknown-edges", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp map[string]any
	decodeJSON(t, w.Body.Bytes(), &resp)
	if int(resp["total"].(float64)) != 1 {
		t.Fatalf("want total=1 (only the unknown-confidence edge), got %+v", resp)
	}
	edgesOut := resp["edges"].([]any)
	entry := edgesOut[0].(map[string]any)
	if entry["confidence"] != "unknown" || entry["from_id"] != "n4" || entry["to"] != "unresolved" {
		t.Fatalf("unexpected edge entry: %+v", entry)
	}
	// testEdges()'s plain "calls" edges (no confidence) must never appear.
	for _, e := range edgesOut {
		if e.(map[string]any)["type"] == "calls" {
			t.Fatalf("a plain calls edge (no confidence) must not appear: %+v", e)
		}
	}
}

func TestHandleUnknownEdges_MinConfidenceAndServiceFilter(t *testing.T) {
	nodes := append(testNodes(),
		&graph.Node{ID: "n4", Type: graph.NodeTypeHTTPClient, Label: "fetchStatus", Service: "auth", File: "auth/client.go", Line: 30, Language: "go"},
		&graph.Node{ID: "n5", Type: graph.NodeTypeHTTPClient, Label: "fetchOther", Service: "billing", File: "billing/client.go", Line: 5, Language: "go"},
		&graph.Node{ID: "unresolved", Type: graph.NodeTypeService, Label: "unresolved", Service: ""},
	)
	edges := append(testEdges(),
		&graph.Edge{ID: "e_unknown_auth", From: "n4", To: "unresolved", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceUnknown},
		&graph.Edge{ID: "e_unknown_billing", From: "n5", To: "unresolved", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceUnknown},
	)
	srv := buildTestServer(t, nodes, edges)

	req := httptest.NewRequest("GET", "/api/unknown-edges?service=auth", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp map[string]any
	decodeJSON(t, w.Body.Bytes(), &resp)
	if int(resp["total"].(float64)) != 1 {
		t.Fatalf("want total=1 for service=auth filter, got %+v", resp)
	}
}

// TestHandleUnknownEdges_ProducerResolvedElsewhereExcluded proves the web
// API shares contract.FilterEdgesByConfidence's overcounting fix with the
// CLI and MCP tool: a producer with a stale local-only unknown edge must
// not be reported once a better-resolved edge exists for the same producer
// elsewhere in the fleet-merged index.
func TestHandleUnknownEdges_ProducerResolvedElsewhereExcluded(t *testing.T) {
	nodes := append(testNodes(),
		&graph.Node{ID: "n4", Type: graph.NodeTypeHTTPClient, Label: "fetchStatus", Service: "auth", File: "auth/client.go", Line: 30, Language: "go"},
		&graph.Node{ID: "unresolved", Type: graph.NodeTypeService, Label: "unresolved", Service: ""},
		&graph.Node{ID: "remote_handler", Type: graph.NodeTypeHTTPHandler, Label: "remote handler", Service: "other"},
	)
	edges := append(testEdges(),
		&graph.Edge{ID: "local_unknown", From: "n4", To: "unresolved", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceUnknown},
		&graph.Edge{ID: "bridge_resolved", From: "n4", To: "remote_handler", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceInferred},
	)
	srv := buildTestServer(t, nodes, edges)

	req := httptest.NewRequest("GET", "/api/unknown-edges", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp map[string]any
	decodeJSON(t, w.Body.Bytes(), &resp)
	if int(resp["total"].(float64)) != 0 {
		t.Fatalf("n4 is resolved elsewhere in idx; its stale unknown edge must not be reported, got %+v", resp)
	}
}
