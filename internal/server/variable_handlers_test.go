package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func variableFixture() ([]*graph.Node, []*graph.Edge) {
	nodes := []*graph.Node{
		{ID: "v1", Type: graph.NodeTypeVariable, Label: "counter", Service: "svc", File: "svc/state.go", Line: 3,
			Meta: map[string]string{"data_type": "int", "scope": "package"}},
		{ID: "f1", Type: graph.NodeTypeFunction, Label: "bump", Service: "svc", File: "svc/state.go", Line: 10},
		{ID: "f2", Type: graph.NodeTypeFunction, Label: "report", Service: "svc", File: "svc/report.go", Line: 5},
	}
	edges := []*graph.Edge{
		{ID: "e1", From: "f1", To: "v1", Type: graph.EdgeTypeWrites, Meta: map[string]string{"op": "assign"}},
		{ID: "e2", From: "f2", To: "v1", Type: graph.EdgeTypeReads},
		{ID: "e3", From: "v1", To: "f2", Type: graph.EdgeTypeFlowsTo, Meta: map[string]string{"mode": "value"}},
	}
	return nodes, edges
}

func TestHandleVariableFlow_OK(t *testing.T) {
	nodes, edges := variableFixture()
	srv := buildTestServer(t, nodes, edges)

	req := httptest.NewRequest("GET", "/api/variable/"+url.PathEscape("v1")+"/flow", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Variable   graph.Node `json:"variable"`
		Readers    []flowRef  `json:"readers"`
		Writers    []flowRef  `json:"writers"`
		CapturedBy []flowRef  `json:"captured_by"`
		FlowsTo    []flowRef  `json:"flows_to"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Variable.Label != "counter" {
		t.Errorf("wrong variable: %+v", resp.Variable)
	}
	if len(resp.Writers) != 1 || resp.Writers[0].Label != "bump" {
		t.Errorf("writers wrong: %+v", resp.Writers)
	}
	if len(resp.Readers) != 1 || resp.Readers[0].Label != "report" {
		t.Errorf("readers wrong: %+v", resp.Readers)
	}
	if len(resp.FlowsTo) != 1 || resp.FlowsTo[0].Meta["mode"] != "value" {
		t.Errorf("flows_to wrong: %+v", resp.FlowsTo)
	}
}

func TestHandleVariableFlow_NotAVariable(t *testing.T) {
	nodes, edges := variableFixture()
	srv := buildTestServer(t, nodes, edges)

	req := httptest.NewRequest("GET", "/api/variable/f1/flow", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for non-variable, got %d", w.Code)
	}
}

func TestHandleVariableFlow_NotFound(t *testing.T) {
	nodes, edges := variableFixture()
	srv := buildTestServer(t, nodes, edges)

	req := httptest.NewRequest("GET", "/api/variable/nope/flow", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

func TestHandleSearch_KindFilter(t *testing.T) {
	nodes, edges := variableFixture()
	srv := buildTestServer(t, nodes, edges)

	req := httptest.NewRequest("GET", "/api/graph/search?q=counter&kind=variable", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var results []graph.Node
	decodeJSON(t, w.Body.Bytes(), &results)
	if len(results) != 1 || results[0].Type != graph.NodeTypeVariable {
		t.Fatalf("kind filter failed: %+v", results)
	}

	// Wrong kind excludes the match.
	req = httptest.NewRequest("GET", "/api/graph/search?q=counter&kind=function", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	decodeJSON(t, w.Body.Bytes(), &results)
	if len(results) != 0 {
		t.Fatalf("kind=function should exclude variable match: %+v", results)
	}
}
