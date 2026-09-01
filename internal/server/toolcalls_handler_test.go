package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/ops"
)

func buildTestServerWithOps(t *testing.T) (*Server, *ops.Store) {
	t.Helper()
	srv := buildTestServer(t, testNodes(), testEdges())
	o, err := ops.Open(":memory:")
	if err != nil {
		t.Fatalf("open ops store: %v", err)
	}
	t.Cleanup(func() { o.Close() })
	srv.SetOps(o)
	return srv, o
}

func TestAudit_RecordsUICall(t *testing.T) {
	srv, _ := buildTestServerWithOps(t)

	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}

	// The toolcalls list endpoint is itself audited, so query it and check
	// for the /api/stats row rather than reaching into the store directly.
	req2 := httptest.NewRequest("GET", "/api/toolcalls?tool=GET+/api/stats", nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w2.Code, w2.Body)
	}
	var resp struct {
		Calls []map[string]any `json:"calls"`
		Total int              `json:"total"`
	}
	decodeJSON(t, w2.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Fatalf("want 1 recorded /api/stats call, got %d: %+v", resp.Total, resp.Calls)
	}
	if resp.Calls[0]["source"] != "ui" {
		t.Errorf("want source=ui, got %v", resp.Calls[0]["source"])
	}
	if resp.Calls[0]["status"] != "ok" {
		t.Errorf("want status=ok, got %v", resp.Calls[0]["status"])
	}
}

func TestAudit_SkipsCaptureStatusPoll(t *testing.T) {
	srv, _ := buildTestServerWithOps(t)

	// The SPA polls this endpoint every 2s; it must not land in tool_calls.
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/api/capture/status", nil)
		srv.ServeHTTP(httptest.NewRecorder(), req)
	}

	req := httptest.NewRequest("GET", "/api/toolcalls", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var resp struct {
		Calls []map[string]any `json:"calls"`
		Total int              `json:"total"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	for _, c := range resp.Calls {
		if c["tool"] == "GET /api/capture/status" {
			t.Fatalf("capture/status poll should not be audited, got: %+v", resp.Calls)
		}
	}
}

func TestAudit_RecordsErrorStatus(t *testing.T) {
	srv, _ := buildTestServerWithOps(t)

	req := httptest.NewRequest("GET", "/api/node/nope", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", w.Code)
	}

	req2 := httptest.NewRequest("GET", "/api/toolcalls?status=error", nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)
	var resp struct {
		Total int `json:"total"`
	}
	decodeJSON(t, w2.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Fatalf("want 1 error-status call recorded, got %d", resp.Total)
	}
}

func TestHandleListToolCalls_NoOpsStore(t *testing.T) {
	srv := buildTestServer(t, nil, nil) // no SetOps
	req := httptest.NewRequest("GET", "/api/toolcalls", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", w.Code)
	}
}

func TestHandleDeleteToolCalls_ClearsAll(t *testing.T) {
	srv, _ := buildTestServerWithOps(t)

	// Generate a couple of recorded rows.
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/api/stats", nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
	}

	req := httptest.NewRequest("DELETE", "/api/toolcalls", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Deleted int64 `json:"deleted"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Deleted != 2 {
		t.Fatalf("want 2 deleted, got %d", resp.Deleted)
	}

	// The DELETE call itself is recorded after it runs, so exactly one row
	// (the delete call) should remain.
	req2 := httptest.NewRequest("GET", "/api/toolcalls", nil)
	w2 := httptest.NewRecorder()
	srv.ServeHTTP(w2, req2)
	var list struct {
		Total int `json:"total"`
	}
	decodeJSON(t, w2.Body.Bytes(), &list)
	if list.Total != 1 {
		t.Fatalf("want 1 row (the DELETE call's own record), got %d", list.Total)
	}
}

func TestHandleGetSettings_Default(t *testing.T) {
	srv, _ := buildTestServerWithOps(t)
	req := httptest.NewRequest("GET", "/api/settings", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp map[string]int
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp["tool_call_retention"] != ops.DefaultRetention {
		t.Errorf("want default retention %d, got %d", ops.DefaultRetention, resp["tool_call_retention"])
	}
}

func TestHandlePutSettings_ValidUpdatesAndTrims(t *testing.T) {
	srv, o := buildTestServerWithOps(t)

	// Seed 5 rows directly.
	for i := 0; i < 5; i++ {
		o.RecordCall(context.Background(), ops.Call{Source: "cli", Tool: "t", Params: "{}", Status: "ok"})
	}

	body, _ := json.Marshal(map[string]int{"tool_call_retention": 2})
	req := httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}

	list, err := o.ListCalls(context.Background(), ops.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if list.Total != 2 {
		t.Fatalf("want retention trimmed to 2, got %d", list.Total)
	}
}

func TestHandlePutSettings_InvalidValue(t *testing.T) {
	srv, _ := buildTestServerWithOps(t)

	for _, v := range []int{0, -1, 10001} {
		body, _ := json.Marshal(map[string]int{"tool_call_retention": v})
		req := httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(body))
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("value %d: want 422, got %d", v, w.Code)
		}
	}
}

// TestAudit_ToleratesRecordFailure: a closed ops.Store makes RecordCall fail;
// the request must still be served correctly (UB.2 item 3, "never fail the call").
func TestAudit_ToleratesRecordFailure(t *testing.T) {
	srv, o := buildTestServerWithOps(t)
	o.Close() // subsequent RecordCall calls now error

	req := httptest.NewRequest("GET", "/api/stats", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 despite ops record failure, got %d: %s", w.Code, w.Body)
	}
	var resp map[string]int
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp["nodes"] != 3 {
		t.Errorf("want 3 nodes, got %d", resp["nodes"])
	}
}

func TestHandlePutSettings_NoOpsStore(t *testing.T) {
	srv := buildTestServer(t, nil, nil)
	body, _ := json.Marshal(map[string]int{"tool_call_retention": 50})
	req := httptest.NewRequest("PUT", "/api/settings", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", w.Code)
	}
}
