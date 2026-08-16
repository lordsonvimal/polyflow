package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// --- /api/scope?kind=file ---

func scopeTestNodes() []*graph.Node {
	return []*graph.Node{
		{ID: "n1", Type: graph.NodeTypeFunction, Label: "createUser", Service: "auth", File: "auth/user.go", Line: 10, Language: "go"},
		{ID: "n2", Type: graph.NodeTypeFunction, Label: "validateUser", Service: "auth", File: "auth/user.go", Line: 20, Language: "go"},
		{ID: "n3", Type: graph.NodeTypeFunction, Label: "hashPassword", Service: "auth", File: "auth/crypto.go", Line: 5, Language: "go"},
	}
}

func scopeTestEdges() []*graph.Edge {
	return []*graph.Edge{
		{ID: "e1", From: "n1", To: "n2", Type: graph.EdgeTypeCalls},
		{ID: "e2", From: "n1", To: "n3", Type: graph.EdgeTypeCalls}, // boundary: leaves auth/user.go
	}
}

func TestHandleScope_File_OK(t *testing.T) {
	srv := buildTestServer(t, scopeTestNodes(), scopeTestEdges())
	req := httptest.NewRequest("GET", "/api/scope?kind=file&service=auth&path=auth/user.go", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		File    string          `json:"file"`
		Service string          `json:"service"`
		Nodes   []CytoscapeNode `json:"nodes"`
		Edges   []CytoscapeEdge `json:"edges"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)

	if resp.File != "auth/user.go" || resp.Service != "auth" {
		t.Fatalf("want file=auth/user.go service=auth, got %+v", resp)
	}
	// Positive: both in-file symbols present, plus the boundary node n3.
	if len(resp.Nodes) != 3 {
		t.Fatalf("want 3 nodes (n1, n2, boundary n3), got %d: %+v", len(resp.Nodes), resp.Nodes)
	}
	var stubs, nonStubs int
	for _, n := range resp.Nodes {
		if n.Data.Meta["stub"] == "true" {
			stubs++
			if n.Data.ID != "n3" {
				t.Errorf("unexpected stub node id %s", n.Data.ID)
			}
		} else {
			nonStubs++
		}
	}
	if stubs != 1 || nonStubs != 2 {
		t.Fatalf("want 1 stub + 2 non-stub nodes, got %d stub, %d non-stub", stubs, nonStubs)
	}
	// Negative: the boundary edge is present but its target (n3) is only a
	// stub — the external subgraph is never expanded past that one node.
	if len(resp.Edges) != 2 {
		t.Fatalf("want 2 edges (intra-file + boundary), got %d: %+v", len(resp.Edges), resp.Edges)
	}

	// Original index node must not be mutated by the stub-flagging copy.
	srv.idxMu.RLock()
	if srv.idx.Nodes["n3"].Meta != nil && srv.idx.Nodes["n3"].Meta["stub"] == "true" {
		t.Error("handleScope must not mutate the shared index node's Meta map")
	}
	srv.idxMu.RUnlock()
}

func TestHandleScope_MissingPath(t *testing.T) {
	srv := buildTestServer(t, scopeTestNodes(), scopeTestEdges())
	req := httptest.NewRequest("GET", "/api/scope?kind=file&service=auth", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestHandleScope_UnsupportedKind(t *testing.T) {
	srv := buildTestServer(t, scopeTestNodes(), scopeTestEdges())
	req := httptest.NewRequest("GET", "/api/scope?kind=folder&service=auth&path=auth", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestHandleScope_UnknownFile(t *testing.T) {
	srv := buildTestServer(t, scopeTestNodes(), scopeTestEdges())
	req := httptest.NewRequest("GET", "/api/scope?kind=file&service=auth&path=no/such/file.go", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

// A file's `contains` edge to its lone declared symbol must be visible — a
// single-component file (e.g. a SolidJS component) shouldn't render as two
// disconnected islands just because the general contains-backbone filter
// hides it. But once a file/struct has multiple children, contains edges
// stay hidden — showing all of them is what the filter exists to avoid.
func TestHandleScope_ContainsEdge_SingleChildVisible(t *testing.T) {
	nodes := []*graph.Node{
		{ID: "file1", Type: graph.NodeTypeFile, Label: "App.tsx", Service: "web", File: "web/src/App.tsx"},
		{ID: "comp1", Type: graph.NodeTypeFunction, Label: "App", Service: "web", File: "web/src/App.tsx", Line: 18},
	}
	edges := []*graph.Edge{
		{ID: "e1", From: "file1", To: "comp1", Type: graph.EdgeTypeContains},
	}
	srv := buildTestServer(t, nodes, edges)
	req := httptest.NewRequest("GET", "/api/scope?kind=file&service=web&path=web/src/App.tsx", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Edges []CytoscapeEdge `json:"edges"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Edges) != 1 || resp.Edges[0].Data.ID != "e1" {
		t.Fatalf("want the single contains edge visible, got %+v", resp.Edges)
	}
}

func TestHandleScope_ContainsEdge_MultiChildHidden(t *testing.T) {
	nodes := []*graph.Node{
		{ID: "file1", Type: graph.NodeTypeFile, Label: "user.go", Service: "auth", File: "auth/user.go"},
		{ID: "n1", Type: graph.NodeTypeFunction, Label: "createUser", Service: "auth", File: "auth/user.go", Line: 10},
		{ID: "n2", Type: graph.NodeTypeFunction, Label: "validateUser", Service: "auth", File: "auth/user.go", Line: 20},
	}
	edges := []*graph.Edge{
		{ID: "e1", From: "file1", To: "n1", Type: graph.EdgeTypeContains},
		{ID: "e2", From: "file1", To: "n2", Type: graph.EdgeTypeContains},
	}
	srv := buildTestServer(t, nodes, edges)
	req := httptest.NewRequest("GET", "/api/scope?kind=file&service=auth&path=auth/user.go", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Edges []CytoscapeEdge `json:"edges"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Edges) != 0 {
		t.Fatalf("want contains edges hidden when a node has multiple children, got %+v", resp.Edges)
	}
}

// Two-run determinism (bug-class rule 2): identical requests must produce
// byte-identical bodies.
func TestHandleScope_Deterministic(t *testing.T) {
	srv := buildTestServer(t, scopeTestNodes(), scopeTestEdges())
	run := func() []byte {
		req := httptest.NewRequest("GET", "/api/scope?kind=file&service=auth&path=auth/user.go", nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Body.Bytes()
	}
	a, b := run(), run()
	if string(a) != string(b) {
		t.Fatalf("non-deterministic response:\n%s\nvs\n%s", a, b)
	}
}
