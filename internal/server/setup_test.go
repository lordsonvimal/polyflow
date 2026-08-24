package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/registry"
)

// withTestRegistry points $POLYFLOW_HOME at a fresh temp dir for the
// duration of the test, isolating it from the real machine registry —
// mirrors registry.polyflowHomeEnv's own test-isolation reasoning.
func withTestRegistry(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("POLYFLOW_HOME", home)
	return home
}

func TestHandleSetupRegistry_ListsExistingEntriesOnly(t *testing.T) {
	withTestRegistry(t)
	regPath, err := registry.DefaultPath()
	if err != nil {
		t.Fatalf("registry path: %v", err)
	}

	live := t.TempDir()
	if err := registry.Sync(regPath, "alive-svc", live); err != nil {
		t.Fatalf("sync alive entry: %v", err)
	}
	if err := registry.Sync(regPath, "gone-svc", filepath.Join(live, "does-not-exist")); err != nil {
		t.Fatalf("sync stale entry: %v", err)
	}

	srv := buildTestServer(t, testNodes(), testEdges())
	req := httptest.NewRequest("GET", "/api/setup/registry", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}

	var resp struct {
		Entries []struct {
			Service   string `json:"service"`
			LocalPath string `json:"local_path"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Entries) != 1 {
		t.Fatalf("want 1 entry (stale one dropped), got %d: %+v", len(resp.Entries), resp.Entries)
	}
	if resp.Entries[0].Service != "alive-svc" || resp.Entries[0].LocalPath != live {
		t.Fatalf("unexpected entry: %+v", resp.Entries[0])
	}
}

func TestHandleSetupSelect_NotWiredReports501(t *testing.T) {
	dir := t.TempDir()
	srv := buildTestServer(t, testNodes(), testEdges())

	body, _ := json.Marshal(map[string]string{"path": dir})
	req := httptest.NewRequest("POST", "/api/setup/select", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("want 501 when no SelectWorkspaceFunc is wired, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleSetupSelect_RejectsNonexistentPath(t *testing.T) {
	srv := buildTestServer(t, testNodes(), testEdges())
	srv.SetSelectWorkspace(func(string) error { return nil })

	body, _ := json.Marshal(map[string]string{"path": "/definitely/not/a/real/path"})
	req := httptest.NewRequest("POST", "/api/setup/select", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a nonexistent path, got %d: %s", w.Code, w.Body)
	}
}

func TestHandleSetupSelect_DelegatesToWiredFunc(t *testing.T) {
	dir := t.TempDir()
	srv := buildTestServer(t, testNodes(), testEdges())

	var got string
	srv.SetSelectWorkspace(func(localPath string) error {
		got = localPath
		return nil
	})

	body, _ := json.Marshal(map[string]string{"path": dir})
	req := httptest.NewRequest("POST", "/api/setup/select", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	if got != dir {
		t.Fatalf("selectWorkspace called with %q, want %q", got, dir)
	}
}

func TestHandleSetupSelect_PropagatesFuncError(t *testing.T) {
	dir := t.TempDir()
	srv := buildTestServer(t, testNodes(), testEdges())
	srv.SetSelectWorkspace(func(string) error { return os.ErrPermission })

	body, _ := json.Marshal(map[string]string{"path": dir})
	req := httptest.NewRequest("POST", "/api/setup/select", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 when selectWorkspace errors, got %d: %s", w.Code, w.Body)
	}
}
