package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// validTestConfigYAML creates a "svc" subdirectory under dir (workspace.Load
// requires every service path to exist) and returns a minimal valid
// polyflow.yml referencing it.
func validTestConfigYAML(t *testing.T, dir string) string {
	t.Helper()
	svcDir := filepath.Join(dir, "svc")
	if err := os.Mkdir(svcDir, 0o755); err != nil {
		t.Fatalf("mkdir svc: %v", err)
	}
	return "name: test-ws\nversion: \"1\"\nservices:\n  - name: svc\n    path: svc\n    language: go\n"
}

// writeTestConfig writes raw to a new temp dir's polyflow.yml and returns a
// server wired to it via SetConfigPath, plus the config's path.
func writeTestConfig(t *testing.T, dir, raw string) (*Server, string) {
	t.Helper()
	path := filepath.Join(dir, "polyflow.yml")
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	srv := buildTestServer(t, testNodes(), testEdges())
	srv.SetConfigPath(path)
	return srv, path
}

func TestHandleGetConfig(t *testing.T) {
	dir := t.TempDir()
	raw := validTestConfigYAML(t, dir)
	srv, path := writeTestConfig(t, dir, raw)

	req := httptest.NewRequest("GET", "/api/config", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Path   string         `json:"path"`
		Raw    string         `json:"raw"`
		Parsed map[string]any `json:"parsed"`
		ETag   string         `json:"etag"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Raw != raw {
		t.Fatalf("raw mismatch: got %q want %q", resp.Raw, raw)
	}
	if resp.Parsed == nil || resp.Parsed["name"] != "test-ws" {
		t.Fatalf("parsed missing/wrong: %+v", resp.Parsed)
	}
	if resp.ETag == "" {
		t.Fatalf("etag empty")
	}
	if resp.Path != path {
		absPath, _ := filepath.Abs(path)
		if resp.Path != absPath {
			t.Fatalf("path mismatch: got %q want %q or %q", resp.Path, path, absPath)
		}
	}
}

func TestHandleGetConfig_NotFound(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	srv.SetConfigPath(filepath.Join(t.TempDir(), "missing.yml"))

	req := httptest.NewRequest("GET", "/api/config", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", w.Code, w.Body)
	}
}

func getConfigEtag(t *testing.T, srv *Server) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/config", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/config: want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		ETag string `json:"etag"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.ETag
}

func TestHandlePutConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	raw := validTestConfigYAML(t, dir)
	srv, path := writeTestConfig(t, dir, raw)
	etag := getConfigEtag(t, srv)

	newRaw := raw + "patterns: []\n"
	body, _ := json.Marshal(map[string]string{"raw": newRaw, "etag": etag})
	req := httptest.NewRequest("PUT", "/api/config", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var putResp struct {
		ETag string `json:"etag"`
		OK   bool   `json:"ok"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &putResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !putResp.OK {
		t.Fatalf("ok=false")
	}

	on, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(on) != newRaw {
		t.Fatalf("file mismatch: got %q want %q", on, newRaw)
	}
	if putResp.ETag != configEtag([]byte(newRaw)) {
		t.Fatalf("etag mismatch: got %q", putResp.ETag)
	}
}

func TestHandlePutConfig_StaleEtag_409(t *testing.T) {
	dir := t.TempDir()
	raw := validTestConfigYAML(t, dir)
	srv, _ := writeTestConfig(t, dir, raw)

	body, _ := json.Marshal(map[string]string{"raw": raw, "etag": "stale"})
	req := httptest.NewRequest("PUT", "/api/config", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Error       string `json:"error"`
		CurrentETag string `json:"current_etag"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CurrentETag != configEtag([]byte(raw)) {
		t.Fatalf("current_etag mismatch: got %q", resp.CurrentETag)
	}
}

func TestHandlePutConfig_ExternalEditBetweenGetAndPut_409(t *testing.T) {
	dir := t.TempDir()
	raw := validTestConfigYAML(t, dir)
	srv, path := writeTestConfig(t, dir, raw)
	etag := getConfigEtag(t, srv)

	// Simulate an external edit landing between the client's GET and PUT.
	if err := os.WriteFile(path, []byte(raw+"\n# external edit\n"), 0o644); err != nil {
		t.Fatalf("external edit: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"raw": raw + "patterns: []\n", "etag": etag})
	req := httptest.NewRequest("PUT", "/api/config", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body)
	}
}

func TestHandlePutConfig_InvalidYAML_422(t *testing.T) {
	dir := t.TempDir()
	raw := validTestConfigYAML(t, dir)
	srv, _ := writeTestConfig(t, dir, raw)
	etag := getConfigEtag(t, srv)

	body, _ := json.Marshal(map[string]string{"raw": "not: valid: yaml: [", "etag": etag})
	req := httptest.NewRequest("PUT", "/api/config", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body)
	}

	on, err := os.ReadFile(srv.configPathOrDefault())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(on) != raw {
		t.Fatalf("config was written despite 422: %q", on)
	}
}

func TestHandlePutConfig_UnknownField_422(t *testing.T) {
	dir := t.TempDir()
	raw := validTestConfigYAML(t, dir)
	srv, _ := writeTestConfig(t, dir, raw)
	etag := getConfigEtag(t, srv)

	body, _ := json.Marshal(map[string]string{"raw": raw + "totally_unknown_field: true\n", "etag": etag})
	req := httptest.NewRequest("PUT", "/api/config", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body)
	}
}

func TestHandlePutConfig_NonexistentServicePath_422(t *testing.T) {
	dir := t.TempDir()
	raw := validTestConfigYAML(t, dir)
	srv, _ := writeTestConfig(t, dir, raw)
	etag := getConfigEtag(t, srv)

	badRaw := "name: test-ws\nversion: \"1\"\nservices:\n  - name: svc\n    path: does-not-exist\n    language: go\n"
	body, _ := json.Marshal(map[string]string{"raw": badRaw, "etag": etag})
	req := httptest.NewRequest("PUT", "/api/config", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body)
	}
}

func TestHandlePutConfig_ConcurrentRace_SecondGets409(t *testing.T) {
	dir := t.TempDir()
	raw := validTestConfigYAML(t, dir)
	srv, _ := writeTestConfig(t, dir, raw)
	etag := getConfigEtag(t, srv)

	putOnce := func(raw string) int {
		body, _ := json.Marshal(map[string]string{"raw": raw, "etag": etag})
		req := httptest.NewRequest("PUT", "/api/config", bytes.NewReader(body))
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}

	first := putOnce(raw + "patterns: []\n")
	second := putOnce(raw + "patterns: [\"x\"]\n")
	if first != http.StatusOK {
		t.Fatalf("first PUT: want 200, got %d", first)
	}
	if second != http.StatusConflict {
		t.Fatalf("second PUT with same stale etag: want 409, got %d", second)
	}
}
