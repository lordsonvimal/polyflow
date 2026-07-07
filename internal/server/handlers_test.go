package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// buildTestServer creates an in-memory test server from the given nodes and edges.
func buildTestServer(t *testing.T, nodes []*graph.Node, edges []*graph.Edge) *Server {
	t.Helper()
	store, err := graph.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	for _, n := range nodes {
		if err := store.UpsertNode(ctx, n); err != nil {
			t.Fatalf("upsert node: %v", err)
		}
	}
	for _, e := range edges {
		if err := store.UpsertEdge(ctx, e); err != nil {
			t.Fatalf("upsert edge: %v", err)
		}
	}

	idx := graph.NewAdjacencyIndex()
	for _, n := range nodes {
		idx.AddNode(n)
	}
	for _, e := range edges {
		idx.AddEdge(e)
	}

	return New(store, idx)
}

func testNodes() []*graph.Node {
	return []*graph.Node{
		{ID: "n1", Type: graph.NodeTypeFunction, Label: "createUser", Service: "auth", File: "auth/user.go", Line: 10, Language: "go"},
		{ID: "n2", Type: graph.NodeTypeHTTPHandler, Label: "handleLogin", Service: "auth", File: "auth/handler.go", Line: 20, Language: "go"},
		{ID: "n3", Type: graph.NodeTypeFunction, Label: "hashPassword", Service: "auth", File: "auth/crypto.go", Line: 5, Language: "go"},
	}
}

func testEdges() []*graph.Edge {
	return []*graph.Edge{
		{ID: "e1", From: "n2", To: "n1", Type: graph.EdgeTypeCalls, Label: ""},
		{ID: "e2", From: "n1", To: "n3", Type: graph.EdgeTypeCalls, Label: ""},
	}
}

func decodeJSON(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decode JSON: %v\nbody: %s", err, body)
	}
}

// --- /api/graph ---

func TestHandleGraph_OK(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/graph", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var g CytoscapeGraph
	decodeJSON(t, w.Body.Bytes(), &g)
	if len(g.Nodes) == 0 {
		t.Error("expected non-empty nodes")
	}
	if len(g.Edges) == 0 {
		t.Error("expected non-empty edges")
	}
}

func TestHandleGraph_Pagination(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/graph?page=2&limit=2", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var g CytoscapeGraph
	decodeJSON(t, w.Body.Bytes(), &g)
	// page 2 of 3 nodes with limit 2 → 1 node
	if len(g.Nodes) != 1 {
		t.Errorf("want 1 node on page 2, got %d", len(g.Nodes))
	}
}

// --- /api/graph/search ---

func TestHandleSearch_OK(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/graph/search?q=create", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var nodes []*graph.Node
	decodeJSON(t, w.Body.Bytes(), &nodes)
	if len(nodes) == 0 {
		t.Error("expected at least one matching node")
	}
}

func TestHandleSearch_MissingQ(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/graph/search", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

// --- /api/node/{id} ---

func TestHandleNode_OK(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/node/n1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp map[string]any
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp["node"] == nil {
		t.Error("expected 'node' field")
	}
	if resp["edges_from"] == nil {
		t.Error("expected 'edges_from' field")
	}
	if resp["edges_to"] == nil {
		t.Error("expected 'edges_to' field")
	}
}

func TestHandleNode_NotFound(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/node/nope", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

// --- /api/node/{id}/source ---

func TestHandleNodeSource_OK(t *testing.T) {
	// Use a real file that exists on disk — go.mod is always present.
	nodes := []*graph.Node{
		{ID: "n1", Type: graph.NodeTypeFunction, Label: "createUser", Service: "auth", File: "../../go.mod", Line: 1, Language: "go"},
	}
	srv := buildTestServer(t, nodes, nil)
	req := httptest.NewRequest("GET", "/api/node/n1/source", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp map[string]string
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp["source"] == "" {
		t.Error("expected non-empty source")
	}
}

// --- /api/graph/trace ---

func TestHandleTrace_Forward(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	// n2 → n1 → n3; forward from n2 should include n1 and n3
	req := httptest.NewRequest("GET", "/api/graph/trace?root=n2&direction=forward&depth=5", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var g CytoscapeGraph
	decodeJSON(t, w.Body.Bytes(), &g)
	ids := make(map[string]bool)
	for _, n := range g.Nodes {
		ids[n.Data.ID] = true
	}
	if !ids["n2"] || !ids["n1"] || !ids["n3"] {
		t.Errorf("expected n2, n1, n3 in forward trace; got %v", ids)
	}
}

func TestHandleTrace_Backward(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	// n3 is called by n1, which is called by n2; backward from n3 should include n1, n2
	req := httptest.NewRequest("GET", "/api/graph/trace?root=n3&direction=backward&depth=5", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var g CytoscapeGraph
	decodeJSON(t, w.Body.Bytes(), &g)
	ids := make(map[string]bool)
	for _, n := range g.Nodes {
		ids[n.Data.ID] = true
	}
	if !ids["n3"] || !ids["n1"] || !ids["n2"] {
		t.Errorf("expected n3, n1, n2 in backward trace; got %v", ids)
	}
}

func TestHandleTrace_Both(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	// n1 is in the middle; both should include n2 (ancestor) and n3 (descendant)
	req := httptest.NewRequest("GET", "/api/graph/trace?root=n1&direction=both&depth=5", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var g CytoscapeGraph
	decodeJSON(t, w.Body.Bytes(), &g)
	ids := make(map[string]bool)
	for _, n := range g.Nodes {
		ids[n.Data.ID] = true
	}
	if !ids["n2"] || !ids["n3"] {
		t.Errorf("expected n2 and n3 in both-direction trace; got %v", ids)
	}
}

func TestHandleTrace_MissingRoot(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/graph/trace?direction=forward", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestHandleTrace_UnknownRoot(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/graph/trace?root=zzz", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

// --- /api/stats ---

func TestHandleStats_OK(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp map[string]int
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp["nodes"] != 3 {
		t.Errorf("want 3 nodes, got %d", resp["nodes"])
	}
	if resp["edges"] != 2 {
		t.Errorf("want 2 edges, got %d", resp["edges"])
	}
}
