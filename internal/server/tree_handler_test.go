package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// --- /api/tree ---

func TestHandleTree_OK(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/tree?service=auth", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp graph.TreeResult
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Service != "auth" {
		t.Errorf("want service=auth, got %s", resp.Service)
	}
	if len(resp.Tree) != 1 || resp.Tree[0].Kind != "folder" || resp.Tree[0].Name != "auth" {
		t.Fatalf("want single root folder 'auth', got %+v", resp.Tree)
	}
}

func TestHandleTree_MissingService(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/tree", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestHandleTree_UnknownService(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/tree?service=no-such-service", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}
