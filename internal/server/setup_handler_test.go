package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/workspace"
)

func TestHandleSetupStatus_NeedsConfigAndIndex(t *testing.T) {
	dir := t.TempDir()
	srv := buildTestServer(t, nil, nil)
	srv.SetConfigPath(filepath.Join(dir, "polyflow.yml")) // does not exist
	srv.SetDBPath(filepath.Join(dir, ".polyflow", "graph.db"))

	req := httptest.NewRequest("GET", "/api/setup/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		NeedsConfig bool `json:"needs_config"`
		NeedsIndex  bool `json:"needs_index"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if !resp.NeedsConfig || !resp.NeedsIndex {
		t.Fatalf("want both needs_config and needs_index true, got %+v", resp)
	}
}

func TestHandleSetupStatus_ReadyOnceConfigAndDBExist(t *testing.T) {
	dir := t.TempDir()
	raw := validTestConfigYAML(t, dir)
	srv, configPath := writeTestConfig(t, dir, raw)

	dbPath := filepath.Join(dir, "graph.db")
	if err := os.WriteFile(dbPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write fake db: %v", err)
	}
	srv.SetDBPath(dbPath)
	_ = configPath

	req := httptest.NewRequest("GET", "/api/setup/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	var resp struct {
		NeedsConfig bool `json:"needs_config"`
		NeedsIndex  bool `json:"needs_index"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if resp.NeedsConfig || resp.NeedsIndex {
		t.Fatalf("want both false once config+db exist, got %+v", resp)
	}
}

func TestHandleSetupApply_WritesConfig(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "svc")
	if err := os.Mkdir(svcDir, 0o755); err != nil {
		t.Fatalf("mkdir svc: %v", err)
	}
	srv := buildTestServer(t, nil, nil)
	configPath := filepath.Join(dir, "polyflow.yml")
	srv.SetConfigPath(configPath)

	cfg := workspace.WorkspaceConfig{
		Name:    "discovered-ws",
		Version: "1",
		Services: []workspace.Service{
			{Name: "svc", Path: "svc", Language: "go"},
		},
	}
	buf, _ := json.Marshal(cfg)
	req := httptest.NewRequest("POST", "/api/setup/apply", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	written, err := workspace.Load(configPath)
	if err != nil {
		t.Fatalf("load written config: %v", err)
	}
	if len(written.Services) != 1 || written.Services[0].Name != "svc" {
		t.Fatalf("want 1 service named svc, got %+v", written.Services)
	}
}

func TestHandleSetupApply_NoServices_422(t *testing.T) {
	dir := t.TempDir()
	srv := buildTestServer(t, nil, nil)
	srv.SetConfigPath(filepath.Join(dir, "polyflow.yml"))

	buf, _ := json.Marshal(workspace.WorkspaceConfig{Name: "empty", Version: "1"})
	req := httptest.NewRequest("POST", "/api/setup/apply", bytes.NewReader(buf))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d: %s", w.Code, w.Body)
	}
}
