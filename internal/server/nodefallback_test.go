package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// TestHandleNode_BridgeMergedNode_FallsBackToIndex reproduces the bug found
// while auditing GR.6's web endpoints: buildFleetAwareIndex unions a
// fleet's bridge.db nodes/edges directly into the in-memory idx (they are
// never written into any member's own graph.db), but handleNode/
// handleNodeSource queried s.db.GetNode directly — a plain SQLite lookup
// that always missed a bridge-merged node, 404ing for any node reached via
// a cross-service edge regardless of which fleet member was active.
func TestHandleNode_BridgeMergedNode_FallsBackToIndex(t *testing.T) {
	local := &graph.Node{ID: "local:n1", Type: graph.NodeTypeFunction, Label: "f", Service: "willow", File: "main.go", Line: 1}
	srv := buildTestServer(t, []*graph.Node{local}, nil)

	// Simulate a cross-service bridge merge: a node present only in idx,
	// never written to s.db, tagged owner_service like GR.2's bridge build
	// tags every copied endpoint node.
	bridgeNode := &graph.Node{
		ID: "maple-agent:build.go:http_client:Health:10", Type: graph.NodeTypeHTTPClient,
		Label: "GET /health", Service: "maple-agent", File: "build.go", Line: 10,
		Meta: map[string]string{"owner_service": "maple-agent"},
	}
	bridgeEdge := &graph.Edge{ID: "e1", From: local.ID, To: bridgeNode.ID, Type: graph.EdgeTypeCalls}
	srv.idxMu.Lock()
	srv.idx.AddNode(bridgeNode)
	srv.idx.AddEdge(bridgeEdge)
	srv.idxMu.Unlock()

	req := httptest.NewRequest("GET", "/api/node/"+bridgeNode.ID, nil)
	req.SetPathValue("id", bridgeNode.ID)
	w := httptest.NewRecorder()
	srv.handleNode(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Node      graph.Node    `json:"node"`
		EdgesTo   []*graph.Edge `json:"edges_to"`
		EdgesFrom []*graph.Edge `json:"edges_from"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Node.ID != bridgeNode.ID {
		t.Fatalf("expected node %q, got %+v", bridgeNode.ID, body.Node)
	}
	if len(body.EdgesTo) != 1 || body.EdgesTo[0].ID != bridgeEdge.ID {
		t.Fatalf("expected the bridge edge in edges_to, got %+v", body.EdgesTo)
	}
}

// TestGetNodeWithFallback_MissEverywhere proves a genuinely unknown id
// still 404s rather than the fallback masking real not-found cases.
func TestGetNodeWithFallback_MissEverywhere(t *testing.T) {
	srv := buildTestServer(t, nil, nil)
	_, ok := srv.getNodeWithFallback(context.Background(), "nope")
	if ok {
		t.Fatal("expected no node found")
	}
}
