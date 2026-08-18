package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleReconcilePropose_MissingParams_400(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/reconcile/propose", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleReconcilePropose_ReturnsYAML(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/reconcile/propose?kind=http_call&key=GET+/orders&from=svc-a&to=svc-b", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Filename != "http-call-get-orders.yaml" {
		t.Fatalf("unexpected filename: %q", resp.Filename)
	}
	if !strings.Contains(resp.Content, "proposed: true") || !strings.Contains(resp.Content, "kind: http_call") {
		t.Fatalf("unexpected content: %s", resp.Content)
	}

	// Deterministic: calling again produces byte-identical output.
	req2 := httptest.NewRequest("GET", "/api/reconcile/propose?kind=http_call&key=GET+/orders&from=svc-a&to=svc-b", nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)
	var resp2 struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	decodeJSON(t, w2.Body.Bytes(), &resp2)
	if resp.Content != resp2.Content {
		t.Fatalf("expected deterministic output, got two different results")
	}
}
