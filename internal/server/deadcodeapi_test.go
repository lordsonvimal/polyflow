package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/deadcode"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func deadcodeNodes() []*graph.Node {
	return []*graph.Node{
		{ID: "n1", Type: graph.NodeTypeHTTPHandler, Label: "handleLogin", Service: "auth", File: "auth/handler.go", Line: 20, Language: "go"},
		{ID: "n2", Type: graph.NodeTypeFunction, Label: "usedHelper", Service: "auth", File: "auth/helper.go", Line: 5, Language: "go"},
		{ID: "n3", Type: graph.NodeTypeFunction, Label: "orphanHelper", Service: "auth", File: "auth/orphan.go", Line: 8, Language: "go"},
	}
}

func deadcodeEdges() []*graph.Edge {
	return []*graph.Edge{
		{ID: "e1", From: "n1", To: "n2", Type: graph.EdgeTypeCalls},
	}
}

func TestHandleDeadcode_OK(t *testing.T) {
	srv := buildTestServer(t, deadcodeNodes(), deadcodeEdges())
	req := httptest.NewRequest("GET", "/api/deadcode", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp deadcode.Result
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Total != 1 || resp.Functions[0].ID != "n3" {
		t.Fatalf("want single orphan n3, got %+v", resp.Functions)
	}
}

func TestHandleDeadcode_ServiceFilter(t *testing.T) {
	nodes := deadcodeNodes()
	nodes = append(nodes, &graph.Node{ID: "n4", Type: graph.NodeTypeFunction, Label: "otherOrphan", Service: "billing", File: "billing/orphan.go", Line: 3, Language: "go"})
	srv := buildTestServer(t, nodes, deadcodeEdges())

	req := httptest.NewRequest("GET", "/api/deadcode?service=billing", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp deadcode.Result
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Total != 1 || resp.Functions[0].ID != "n4" {
		t.Fatalf("want single orphan n4, got %+v", resp.Functions)
	}
}
