package indexer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

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
	}
	assert.True(t, foundWebToAPI, "bridge must contain the web<->api cross-service edge")
}
