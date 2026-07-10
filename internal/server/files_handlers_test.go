package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// --- /api/files ---

func TestHandleFiles_OK(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/files", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Files []graph.FileSummary `json:"files"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Files) != 3 {
		t.Fatalf("want 3 files, got %d", len(resp.Files))
	}
	if resp.Files[0].File != "auth/crypto.go" {
		t.Errorf("want sorted files, first = %s", resp.Files[0].File)
	}
}

func TestHandleFiles_Query(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/files?q=handler", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp struct {
		Files []graph.FileSummary `json:"files"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Files) != 1 || resp.Files[0].File != "auth/handler.go" {
		t.Fatalf("want auth/handler.go only, got %+v", resp.Files)
	}
}

// --- /api/file ---

func TestHandleFile_OK(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/file?path=auth/handler.go", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		File    string        `json:"file"`
		Service string        `json:"service"`
		Nodes   []fileNodeRef `json:"nodes"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.File != "auth/handler.go" || resp.Service != "auth" {
		t.Errorf("unexpected file/service: %s / %s", resp.File, resp.Service)
	}
	if len(resp.Nodes) != 1 || resp.Nodes[0].ID != "n2" {
		t.Fatalf("want [n2], got %+v", resp.Nodes)
	}
}

func TestHandleFile_SuffixMatch(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/file?path=handler.go", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleFile_MissingPath(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/file", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestHandleFile_NotFound(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/file?path=nope.go", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}

// --- /api/file/impact ---

func TestHandleFileImpact_Forward(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/file/impact?path=auth/handler.go&direction=forward", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		File     string                  `json:"file"`
		Impacted []graph.FileImpactEntry `json:"impacted"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	// n2 (handler.go) -> n1 (user.go) -> n3 (crypto.go)
	if len(resp.Impacted) != 2 {
		t.Fatalf("want 2 impacted files, got %+v", resp.Impacted)
	}
	if resp.Impacted[0].File != "auth/user.go" || resp.Impacted[0].MinDepth != 1 {
		t.Errorf("want auth/user.go at depth 1 first, got %+v", resp.Impacted[0])
	}
}

func TestHandleFileImpact_Backward(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/file/impact?path=auth/crypto.go&direction=backward", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp struct {
		Impacted []graph.FileImpactEntry `json:"impacted"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Impacted) != 2 {
		t.Fatalf("want 2 impacted files upstream, got %+v", resp.Impacted)
	}
}

func TestHandleFileImpact_MissingPath(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/file/impact", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
}

func TestHandleFileImpact_NotFound(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/file/impact?path=nope.go", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}
}
