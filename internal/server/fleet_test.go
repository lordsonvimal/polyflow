package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
)

// twoServiceTestConfig writes a polyflow.yml with two services ("api",
// "web") and returns a server wired to it, so handleFleetStatus has more
// than one row to report on.
func twoServiceTestConfig(t *testing.T, dir string) (*Server, string) {
	t.Helper()
	for _, name := range []string{"api", "web"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}
	raw := "name: test-fleet\nversion: \"1\"\nservices:\n" +
		"  - name: api\n    path: api\n    language: go\n" +
		"  - name: web\n    path: web\n    language: javascript\n"
	return writeTestConfig(t, dir, raw)
}

// TestHandleFleetStatus_MixOfIndexedAndNot is FR.7's acceptance shape: a
// service with a per-service DB on disk (FR.2's `polyflow index <service>`)
// reports its own node/edge/indexed_at independent of the merged fleet DB;
// a service that was never indexed on its own still gets a row, just with
// indexed:false, rather than being silently dropped.
func TestHandleFleetStatus_MixOfIndexedAndNot(t *testing.T) {
	dir := t.TempDir()
	srv, configPath := twoServiceTestConfig(t, dir)

	dbDir := filepath.Join(filepath.Dir(configPath), meta.DBDir)
	apiDBDir := filepath.Join(dbDir, "services", "api")
	if err := os.MkdirAll(apiDBDir, 0o755); err != nil {
		t.Fatalf("mkdir api db dir: %v", err)
	}
	apiDBPath := filepath.Join(apiDBDir, meta.DBFile)
	store, err := graph.NewSQLiteStore(apiDBPath)
	if err != nil {
		t.Fatalf("open api store: %v", err)
	}
	ctx := context.Background()
	if err := store.UpsertNode(ctx, &graph.Node{ID: "n1", Type: graph.NodeTypeFunction, Label: "f", Service: "api"}); err != nil {
		t.Fatalf("upsert node: %v", err)
	}
	if err := store.SetMeta(ctx, "last_indexed", "1700000000"); err != nil {
		t.Fatalf("set meta: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close api store: %v", err)
	}

	srv.SetDBPath(filepath.Join(dbDir, meta.DBFile))

	req := httptest.NewRequest("GET", "/api/fleet/status", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body)
	}
	var resp struct {
		Services []fleetServiceStatus `json:"services"`
	}
	decodeJSON(t, w.Body.Bytes(), &resp)
	if len(resp.Services) != 2 {
		t.Fatalf("want 2 service rows, got %d: %+v", len(resp.Services), resp.Services)
	}

	byName := map[string]fleetServiceStatus{}
	for _, s := range resp.Services {
		byName[s.Service] = s
	}

	api, ok := byName["api"]
	if !ok || !api.Indexed {
		t.Fatalf("want api indexed, got %+v", api)
	}
	if api.NodeCount != 1 {
		t.Fatalf("want api node_count=1, got %d", api.NodeCount)
	}
	if api.IndexedAt == "" {
		t.Fatalf("want api indexed_at set, got empty")
	}

	web, ok := byName["web"]
	if !ok {
		t.Fatalf("want a web row")
	}
	if web.Indexed {
		t.Fatalf("want web not indexed (no per-service DB written), got indexed=true")
	}
	if web.IndexedAt != "" {
		t.Fatalf("want web indexed_at empty, got %q", web.IndexedAt)
	}
}
