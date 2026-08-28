package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// threeServiceFleet builds a fixture with a real cross-service HTTP edge
// (web's fetch('/api/users') -> api's http.HandleFunc("/api/users"), the
// same shape indexer_test.go's TestRun_CrossLinksCountsOnlyBoundaryCrossings
// already proves produces CrossLinks > 0) plus a third, unrelated service
// with no path to either — proving a bridge build recovers the former while
// keeping the latter out entirely. Originally relink_test.go's fixture
// (FR.5c); moved here when GR.2's bridge build superseded Relink.
func threeServiceFleet(t *testing.T) (*workspace.WorkspaceConfig, string) {
	t.Helper()
	dir := t.TempDir()

	apiSvc := filepath.Join(dir, "api")
	require.NoError(t, os.MkdirAll(apiSvc, 0o755))
	writeFile(t, apiSvc, "go.mod", "module example.com/api\n\ngo 1.22\n")
	writeFile(t, apiSvc, "main.go", `package main

import "net/http"

func main() {
	http.HandleFunc("/api/users", listUsers)
}

func listUsers(w http.ResponseWriter, r *http.Request) {}
`)

	webSvc := filepath.Join(dir, "web")
	require.NoError(t, os.MkdirAll(webSvc, 0o755))
	writeFile(t, webSvc, "app.js", `async function load() {
  const res = await fetch('/api/users');
  return res;
}
`)

	workerSvc := filepath.Join(dir, "worker")
	require.NoError(t, os.MkdirAll(workerSvc, 0o755))
	writeFile(t, workerSvc, "go.mod", "module example.com/worker\n\ngo 1.22\n")
	writeFile(t, workerSvc, "main.go", `package main

func main() { run() }

func run() {}
`)

	cfg := &workspace.WorkspaceConfig{
		Name: "fleet", Version: "1",
		Services: []workspace.Service{
			{Name: "api", Path: apiSvc, Language: "go"},
			{Name: "web", Path: webSvc, Language: "javascript"},
			{Name: "worker", Path: workerSvc, Language: "go"},
		},
	}
	return cfg, dir
}

// indexServiceStandalone runs FR.2's per-service pipeline for svc alone
// (the same call shape `polyflow index <service>` makes) and returns its
// own graph.db's full node/edge set — never merged with any sibling.
func indexServiceStandalone(t *testing.T, cfg *workspace.WorkspaceConfig, dir, svc string) *graph.AdjacencyIndex {
	t.Helper()
	dbDir := filepath.Join(dir, "services", svc)
	_, err := Run(context.Background(), Options{
		Config:        cfg,
		DBDir:         dbDir,
		PatternsDir:   "../../patterns",
		Workers:       2,
		ServiceFilter: []string{svc},
		Full:          true,
	})
	require.NoError(t, err)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()
	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)
	return idx
}

// TestBuildBridge_ProducesCrossServiceEdgeFromIndependentMemberDBs proves
// GR.2's core claim: BuildBridge, given only each fleet member's own
// standalone-indexed node/edge set (as if resolved from three independent
// repos, never merged into one workspace DB), still recovers the
// cross-service web -> api edge relink_test.go's fixture establishes, and
// keeps worker (unrelated to either) out of the bridge entirely.
func TestBuildBridge_ProducesCrossServiceEdgeFromIndependentMemberDBs(t *testing.T) {
	cfg, dir := threeServiceFleet(t)

	apiIdx := indexServiceStandalone(t, cfg, dir, "api")
	webIdx := indexServiceStandalone(t, cfg, dir, "web")
	workerIdx := indexServiceStandalone(t, cfg, dir, "worker")

	var allNodes []graph.Node
	var allEdges []graph.Edge
	for _, idx := range []*graph.AdjacencyIndex{apiIdx, webIdx, workerIdx} {
		for _, n := range idx.Nodes {
			allNodes = append(allNodes, *n)
		}
		allEdges = append(allEdges, idx.AllEdges()...)
	}

	result, err := BuildBridge(context.Background(), cfg.Links, "", allNodes, allEdges)
	require.NoError(t, err)
	require.NotEmpty(t, result.Edges, "must recover at least the web -> api cross-service edge")

	nodeByID := make(map[string]graph.Node, len(result.Nodes))
	for _, n := range result.Nodes {
		nodeByID[n.ID] = n
		assert.NotEmpty(t, n.Meta["owner_service"], "every bridge node must carry owner_service")
	}

	var foundWebToAPI bool
	for _, e := range result.Edges {
		from, fromOK := nodeByID[e.From]
		to, toOK := nodeByID[e.To]
		require.True(t, fromOK, "edge %s: from-endpoint %s must have a bridge node stub", e.ID, e.From)
		require.True(t, toOK, "edge %s: to-endpoint %s must have a bridge node stub", e.ID, e.To)
		assert.NotEqual(t, from.Service, to.Service, "a bridge edge must be cross-service")
		assert.NotContains(t, []string{from.Service, to.Service}, "worker", "worker is unrelated to the web<->api edge and must not appear in the bridge")
		if (from.Service == "web" && to.Service == "api") || (from.Service == "api" && to.Service == "web") {
			foundWebToAPI = true
		}
		assert.NotEmpty(t, e.VerificationState, "bridge edge %s must be reconciled (Tier CSC Phase 1): verification_state must never be empty", e.ID)
		assert.Equal(t, graph.StateCandidate, e.VerificationState, "with only a static provider, a bridge edge must land at candidate, never verified")
	}
	assert.True(t, foundWebToAPI, "bridge must contain the web<->api cross-service edge")
}
