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
)

func mergeFixture(t *testing.T) (backendDB, frontendDB string) {
	t.Helper()
	cfg, dir := testWorkspace(t)
	rootDBDir := filepath.Join(dir, meta.DBDir)

	backendDB = filepath.Join(rootDBDir, "services", "backend")
	_, err := Run(context.Background(), Options{
		Config:        cfg,
		DBDir:         backendDB,
		PatternsDir:   "../../patterns",
		Workers:       2,
		ServiceFilter: []string{"backend"},
	})
	require.NoError(t, err)
	backendDB = filepath.Join(backendDB, meta.DBFile)

	frontendDB = filepath.Join(rootDBDir, "services", "frontend")
	_, err = Run(context.Background(), Options{
		Config:        cfg,
		DBDir:         frontendDB,
		PatternsDir:   "../../patterns",
		Workers:       2,
		ServiceFilter: []string{"frontend"},
	})
	require.NoError(t, err)
	frontendDB = filepath.Join(frontendDB, meta.DBFile)

	return backendDB, frontendDB
}

func TestMergeServiceDBs_SumsNodesAndEdges(t *testing.T) {
	backendDB, frontendDB := mergeFixture(t)

	backendStore, err := graph.NewSQLiteStore(backendDB)
	require.NoError(t, err)
	backendNodes, backendEdges, err := backendStore.Stats(context.Background())
	require.NoError(t, err)
	require.NoError(t, backendStore.Close())

	frontendStore, err := graph.NewSQLiteStore(frontendDB)
	require.NoError(t, err)
	frontendNodes, frontendEdges, err := frontendStore.Stats(context.Background())
	require.NoError(t, err)
	require.NoError(t, frontendStore.Close())

	dstPath := filepath.Join(t.TempDir(), "graph.db")
	dst, err := graph.NewSQLiteStore(dstPath)
	require.NoError(t, err)
	defer dst.Close()

	stats, err := MergeServiceDBs(context.Background(), dst, map[string]string{
		"backend":  backendDB,
		"frontend": frontendDB,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, stats.Services)
	assert.Equal(t, backendNodes+frontendNodes, stats.Nodes)
	assert.Equal(t, backendEdges+frontendEdges, stats.Edges)

	nodeCount, edgeCount, err := dst.Stats(context.Background())
	require.NoError(t, err)
	assert.Equal(t, backendNodes+frontendNodes, nodeCount)
	assert.Equal(t, backendEdges+frontendEdges, edgeCount)

	// FTS was rebuilt from the merged nodes, not left empty/stale: a search
	// for each service's distinctive symbol must find it.
	backendHits, err := dst.SearchNodes(context.Background(), "listUsers", 10)
	require.NoError(t, err)
	assert.NotEmpty(t, backendHits, "search must find the merged backend node")

	frontendHits, err := dst.SearchNodes(context.Background(), "load", 10)
	require.NoError(t, err)
	assert.NotEmpty(t, frontendHits, "search must find the merged frontend node")
}

func TestMergeServiceDBs_NoIDCollisions(t *testing.T) {
	backendDB, frontendDB := mergeFixture(t)

	dstPath := filepath.Join(t.TempDir(), "graph.db")
	dst, err := graph.NewSQLiteStore(dstPath)
	require.NoError(t, err)
	defer dst.Close()

	_, err = MergeServiceDBs(context.Background(), dst, map[string]string{
		"backend":  backendDB,
		"frontend": frontendDB,
	})
	require.NoError(t, err)

	idx, err := dst.BuildIndex(context.Background())
	require.NoError(t, err)
	byService := map[string][]*graph.Node{}
	for _, n := range idx.Nodes {
		byService[n.Service] = append(byService[n.Service], n)
	}
	_, _, _, ok := graph.AssertServiceScopedIDs(byService)
	assert.True(t, ok, "merged DB must not contain any cross-service ID collision")
}

func TestMergeServiceDBs_EmptyServices(t *testing.T) {
	dstPath := filepath.Join(t.TempDir(), "graph.db")
	dst, err := graph.NewSQLiteStore(dstPath)
	require.NoError(t, err)
	defer dst.Close()

	stats, err := MergeServiceDBs(context.Background(), dst, nil)
	require.NoError(t, err)
	assert.Equal(t, &MergeStats{}, stats)
}

func TestMergeServiceDBs_ReMergeAfterFileDeleted(t *testing.T) {
	cfg, dir := testWorkspace(t)
	rootDBDir := filepath.Join(dir, meta.DBDir)
	backendDir := filepath.Join(rootDBDir, "services", "backend")

	_, err := Run(context.Background(), Options{
		Config:        cfg,
		DBDir:         backendDir,
		PatternsDir:   "../../patterns",
		Workers:       2,
		ServiceFilter: []string{"backend"},
	})
	require.NoError(t, err)
	backendDB := filepath.Join(backendDir, meta.DBFile)

	dstPath := filepath.Join(t.TempDir(), "graph.db")
	dst, err := graph.NewSQLiteStore(dstPath)
	require.NoError(t, err)
	defer dst.Close()

	_, err = MergeServiceDBs(context.Background(), dst, map[string]string{"backend": backendDB})
	require.NoError(t, err)

	before, err := dst.SearchNodes(context.Background(), "listUsers", 10)
	require.NoError(t, err)
	require.NotEmpty(t, before, "listUsers must exist before the source file is deleted")

	nodeCountBefore, _, err := dst.Stats(context.Background())
	require.NoError(t, err)

	// Delete backend's only file and re-index (Full, so the incremental
	// cache doesn't hide the deletion), then re-merge into the same dst.
	backendSvcDir := cfg.Services[0].Path
	require.NoError(t, os.Remove(filepath.Join(backendSvcDir, "main.go")))
	require.NoError(t, os.MkdirAll(backendSvcDir, 0o755))

	_, err = Run(context.Background(), Options{
		Config:        cfg,
		DBDir:         backendDir,
		PatternsDir:   "../../patterns",
		Workers:       2,
		ServiceFilter: []string{"backend"},
		Full:          true,
	})
	require.NoError(t, err)

	_, err = MergeServiceDBs(context.Background(), dst, map[string]string{"backend": backendDB})
	require.NoError(t, err)

	after, err := dst.SearchNodes(context.Background(), "listUsers", 10)
	require.NoError(t, err)
	assert.Empty(t, after, "re-merging after the source file was deleted must remove its nodes from dst")

	nodeCount, _, err := dst.Stats(context.Background())
	require.NoError(t, err)
	assert.Less(t, nodeCount, nodeCountBefore, "deleting the source file must shrink the merged node count")
}
