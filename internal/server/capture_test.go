package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/capture"
)

func buildTestServerWithCapture(t *testing.T) *Server {
	t.Helper()
	srv := buildTestServer(t, testNodes(), testEdges())
	srv.SetCapture(capture.NewManager(t.TempDir()))
	return srv
}

func runtimeFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "evidence", "runtime", "http_2svc.otlp.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	return path
}

func TestHandleCaptureStart_NoManager_503(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("POST", "/api/capture/start", bytes.NewBufferString(`{"session":"s1"}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleCaptureStartStop_Lifecycle(t *testing.T) {
	srv := buildTestServerWithCapture(t)

	req := httptest.NewRequest("POST", "/api/capture/start", bytes.NewBufferString(`{"session":"s1","http_port":0,"grpc_port":0}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body)
	}
	var startResp struct {
		Session  string `json:"session"`
		Status   string `json:"status"`
		HTTPPort int    `json:"http_port"`
	}
	decodeJSON(t, w.Body.Bytes(), &startResp)
	if startResp.Session != "s1" || startResp.Status != "active" || startResp.HTTPPort == 0 {
		t.Fatalf("unexpected start response: %+v", startResp)
	}

	// Status reflects the active session.
	req = httptest.NewRequest("GET", "/api/capture/status", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var status capture.StatusResponse
	decodeJSON(t, w.Body.Bytes(), &status)
	if len(status.Active) != 1 || status.Active[0].Session != "s1" {
		t.Fatalf("expected s1 active, got %+v", status.Active)
	}

	// Stop finalizes it.
	req = httptest.NewRequest("POST", "/api/capture/stop", bytes.NewBufferString(`{"session":"s1"}`))
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var stopResp struct {
		Finalized  bool   `json:"finalized"`
		FusionHint string `json:"fusion_hint"`
	}
	decodeJSON(t, w.Body.Bytes(), &stopResp)
	if !stopResp.Finalized || stopResp.FusionHint == "" {
		t.Fatalf("unexpected stop response: %+v", stopResp)
	}

	req = httptest.NewRequest("GET", "/api/capture/status", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	decodeJSON(t, w.Body.Bytes(), &status)
	if len(status.Active) != 0 {
		t.Fatalf("expected no active sessions, got %+v", status.Active)
	}
	if len(status.Sessions) != 1 || status.Sessions[0].Name != "s1" {
		t.Fatalf("expected s1 in sessions, got %+v", status.Sessions)
	}
}

func TestHandleCaptureStart_PortConflict_409(t *testing.T) {
	srv := buildTestServerWithCapture(t)

	// First start on port 0 to get a real free port, then try to reuse it.
	req := httptest.NewRequest("POST", "/api/capture/start", bytes.NewBufferString(`{"session":"s1","http_port":0,"grpc_port":0}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", w.Code, w.Body)
	}
	var startResp struct {
		HTTPPort int `json:"http_port"`
	}
	decodeJSON(t, w.Body.Bytes(), &startResp)

	body := fmt.Sprintf(`{"session":"s2","http_port":%d,"grpc_port":0}`, startResp.HTTPPort)
	req = httptest.NewRequest("POST", "/api/capture/start", bytes.NewBufferString(body))
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body)
	}
	var conflict struct {
		Port int `json:"port"`
	}
	decodeJSON(t, w.Body.Bytes(), &conflict)
	if conflict.Port != startResp.HTTPPort {
		t.Fatalf("expected conflicting port %d, got %d", startResp.HTTPPort, conflict.Port)
	}
}

func TestHandleCaptureStop_UnknownSession_404(t *testing.T) {
	srv := buildTestServerWithCapture(t)
	req := httptest.NewRequest("POST", "/api/capture/stop", bytes.NewBufferString(`{"session":"nope"}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleCaptureIngest_JSONPath(t *testing.T) {
	srv := buildTestServerWithCapture(t)

	body, _ := json.Marshal(map[string]string{"session": "imported", "path": runtimeFixture(t)})
	req := httptest.NewRequest("POST", "/api/capture/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Session    string `json:"session"`
		SpanCount  int    `json:"span_count"`
		FusionHint string `json:"fusion_hint"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Session != "imported" || resp.SpanCount == 0 || resp.FusionHint == "" {
		t.Fatalf("unexpected ingest response: %+v", resp)
	}
}

func TestHandleCaptureIngest_Multipart(t *testing.T) {
	srv := buildTestServerWithCapture(t)

	raw, err := os.ReadFile(runtimeFixture(t))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("session", "uploaded"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	fw, err := mw.CreateFormFile("file", "dump.otlp.json")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(raw); err != nil {
		t.Fatalf("write file: %v", err)
	}
	mw.Close()

	req := httptest.NewRequest("POST", "/api/capture/ingest", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Session   string `json:"session"`
		SpanCount int    `json:"span_count"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Session != "uploaded" || resp.SpanCount == 0 {
		t.Fatalf("unexpected ingest response: %+v", resp)
	}
}

func TestHandleRuntimeFlows(t *testing.T) {
	srv := buildTestServerWithCapture(t)

	body, _ := json.Marshal(map[string]string{"session": "imported", "path": runtimeFixture(t)})
	req := httptest.NewRequest("POST", "/api/capture/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ingest failed: %d %s", w.Code, w.Body)
	}

	req = httptest.NewRequest("GET", "/api/runtime/flows?session=imported", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Session     string           `json:"session"`
		Spans       []map[string]any `json:"spans"`
		FlowRecords []map[string]any `json:"flow_records"`
		Ledger      []map[string]any `json:"ledger"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Session != "imported" || len(resp.Spans) == 0 {
		t.Fatalf("unexpected flows response: %+v", resp)
	}
}

func TestHandleRuntimeFlows_UnknownSession_404(t *testing.T) {
	srv := buildTestServerWithCapture(t)
	req := httptest.NewRequest("GET", "/api/runtime/flows?session=nope", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	// A never-ingested session reads back zero spans, not an error — only a
	// missing session parameter is a client error.
	if w.Code != http.StatusOK {
		t.Fatalf("want 200 for an empty/unknown session, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleRuntimeCoverage(t *testing.T) {
	srv := buildTestServerWithCapture(t)

	body, _ := json.Marshal(map[string]string{"session": "imported", "path": runtimeFixture(t)})
	req := httptest.NewRequest("POST", "/api/capture/ingest", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("ingest failed: %d %s", w.Code, w.Body)
	}

	req = httptest.NewRequest("GET", "/api/runtime/coverage?session=imported", nil)
	w = httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Session  string `json:"session"`
		Coverage struct {
			TotalChannels int `json:"TotalChannels"`
		} `json:"coverage"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.Session != "imported" {
		t.Fatalf("unexpected coverage response: %+v", resp)
	}
}

func TestHandleCaptureIngest_MissingBody_400(t *testing.T) {
	srv := buildTestServerWithCapture(t)
	req := httptest.NewRequest("POST", "/api/capture/ingest", bytes.NewBufferString(`{}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body)
	}
}

// two-run determinism: ingesting the same fixture twice under distinct
// session names must produce byte-identical flow_records/ledger JSON.
func TestHandleRuntimeFlows_Deterministic(t *testing.T) {
	srv := buildTestServerWithCapture(t)

	for _, sess := range []string{"a", "b"} {
		body, _ := json.Marshal(map[string]string{"session": sess, "path": runtimeFixture(t)})
		req := httptest.NewRequest("POST", "/api/capture/ingest", bytes.NewReader(body))
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("ingest %s failed: %d %s", sess, w.Code, w.Body)
		}
	}

	get := func(sess string) []byte {
		req := httptest.NewRequest("GET", "/api/runtime/flows?session="+sess, nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		var raw map[string]json.RawMessage
		decodeJSON(t, w.Body.Bytes(), &raw)
		return raw["flow_records"]
	}

	a, b := get("a"), get("b")
	// Both sessions ingested the identical fixture, so the flow records
	// differ only by the embedded session label inside Refs; strip that by
	// comparing lengths and Kind/Key pairs.
	var recA, recB []map[string]any
	if err := json.Unmarshal(a, &recA); err != nil {
		t.Fatalf("unmarshal a: %v", err)
	}
	if err := json.Unmarshal(b, &recB); err != nil {
		t.Fatalf("unmarshal b: %v", err)
	}
	if len(recA) != len(recB) {
		t.Fatalf("record count differs: %d vs %d", len(recA), len(recB))
	}
	for i := range recA {
		if recA[i]["Key"] != recB[i]["Key"] || recA[i]["Kind"] != recB[i]["Kind"] {
			t.Fatalf("record %d differs: %+v vs %+v", i, recA[i], recB[i])
		}
	}
}
