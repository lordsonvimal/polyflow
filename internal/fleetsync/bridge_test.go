package fleetsync_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/fleetconfig"
	"github.com/lordsonvimal/polyflow/internal/fleetsync"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

const apiPolyflowYML = `
name: api
version: "1"
services:
  - name: api
    path: .
    language: go
`

const apiMainGo = `package main

import "net/http"

func main() {
	http.HandleFunc("/api/users", listUsers)
}

func listUsers(w http.ResponseWriter, r *http.Request) {}
`

const webPolyflowYML = `
name: web
version: "1"
services:
  - name: web
    path: .
    language: javascript
`

const webAppJS = `async function load() {
  const res = await fetch('/api/users');
  return res;
}
`

// newBareServiceRepo creates a bare remote for a single-service repo (its
// polyflow.yml plus one source file), pushed to "main". Returns the bare
// repo path, usable directly as a fleetconfig.Service.Git URL.
func newBareServiceRepo(t *testing.T, polyflowYML, srcName, srcContent string) string {
	t.Helper()
	bareDir := t.TempDir()
	runGit(t, "", "init", "--bare", bareDir)

	workDir := t.TempDir()
	runGit(t, workDir, "init")
	runGit(t, workDir, "checkout", "-b", "main")
	runGit(t, workDir, "config", "user.email", "test@example.com")
	runGit(t, workDir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "polyflow.yml"), []byte(polyflowYML), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, srcName), []byte(srcContent), 0o644))
	runGit(t, workDir, "add", ".")
	runGit(t, workDir, "commit", "-m", "init")
	runGit(t, workDir, "remote", "add", "origin", bareDir)
	runGit(t, workDir, "push", "origin", "main")
	return bareDir
}

// TestSync_ResolvesClonesAndBuildsBridge is GR.2's end-to-end acceptance
// test: two fleet members, each only ever available as a bare git remote
// (never as a sibling directory), resolve via GR.1's ResolveService (clone +
// index, since neither has a local registry entry), and fleetsync.Sync
// recovers their cross-service edge into a real bridge.db on disk.
func TestSync_ResolvesClonesAndBuildsBridge(t *testing.T) {
	apiBareURL := newBareServiceRepo(t, apiPolyflowYML, "main.go", apiMainGo)
	webBareURL := newBareServiceRepo(t, webPolyflowYML, "app.js", webAppJS)

	cfg := &fleetconfig.Config{
		Name:    "testfleet",
		Version: "1",
		Services: []fleetconfig.Service{
			{Name: "api", Git: apiBareURL, Ref: "main", Language: "go"},
			{Name: "web", Git: webBareURL, Ref: "main", Language: "javascript"},
		},
	}

	bridgePath := filepath.Join(t.TempDir(), "bridge.db")
	stats, err := fleetsync.Sync(context.Background(), cfg, fleetsync.SyncOptions{
		RegistryPath: filepath.Join(t.TempDir(), "registry.yml"),
		ScratchDir:   t.TempDir(),
		BridgePath:   bridgePath,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, stats.Services)
	assert.Greater(t, stats.Edges, 0, "must recover the web -> api cross-service edge")
	assert.Equal(t, stats.Nodes, len(nodeIDsOf(t, bridgePath)))

	store, err := graph.NewSQLiteStore(bridgePath)
	require.NoError(t, err)
	defer store.Close()
	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)

	var foundCross bool
	for _, e := range idx.AllEdges() {
		from, to := idx.Nodes[e.From], idx.Nodes[e.To]
		require.NotNil(t, from, "edge %s: from-endpoint must exist in bridge.db", e.ID)
		require.NotNil(t, to, "edge %s: to-endpoint must exist in bridge.db", e.ID)
		assert.Equal(t, from.Service, from.Meta["owner_service"])
		assert.Equal(t, to.Service, to.Meta["owner_service"])
		if (from.Service == "api" && to.Service == "web") || (from.Service == "web" && to.Service == "api") {
			foundCross = true
		}
	}
	assert.True(t, foundCross, "bridge.db must contain the api<->web cross-service edge")
}

func nodeIDsOf(t *testing.T, dbPath string) map[string]bool {
	t.Helper()
	store, err := graph.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer store.Close()
	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)
	ids := make(map[string]bool, len(idx.Nodes))
	for id := range idx.Nodes {
		ids[id] = true
	}
	return ids
}
