package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/fleetsync"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/registry"
)

// TestPrintUnknownEdges_LocalOnly proves the plain, non-fleet case:
// `polyflow status --unknown-edges` lists edges at or below the requested
// confidence, and excludes edges above it.
func TestPrintUnknownEdges_LocalOnly(t *testing.T) {
	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".polyflow"), 0o755))
	dbPath := filepath.Join(repoDir, ".polyflow", "graph.db")

	store, err := graph.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer store.Close()

	bw := graph.NewFreshBatchWriter(store)
	require.NoError(t, bw.AddNode(context.Background(), &graph.Node{
		ID: "prod1", Type: "http_client", Label: "GET /foo", Service: "svc", File: "client.go", Line: 10, Language: "go",
	}))
	require.NoError(t, bw.AddNode(context.Background(), &graph.Node{
		ID: "unresolved", Type: "service", Label: "unresolved", Service: "",
	}))
	require.NoError(t, bw.AddNode(context.Background(), &graph.Node{
		ID: "prod2", Type: "http_client", Label: "GET /bar", Service: "svc", File: "client.go", Line: 20, Language: "go",
	}))
	require.NoError(t, bw.AddNode(context.Background(), &graph.Node{
		ID: "handler2", Type: "http_handler", Label: "GET /bar", Service: "svc",
	}))
	require.NoError(t, bw.Flush(context.Background()))
	require.NoError(t, bw.AddEdge(context.Background(), &graph.Edge{
		ID: "e1", From: "prod1", To: "unresolved", Type: "http_call", Confidence: "unknown",
	}))
	require.NoError(t, bw.AddEdge(context.Background(), &graph.Edge{
		ID: "e2", From: "prod2", To: "handler2", Type: "http_call", Confidence: "static",
	}))
	require.NoError(t, bw.Flush(context.Background()))

	out := captureStdout(t, func() {
		require.NoError(t, printUnknownEdges(context.Background(), store, "unknown"))
	})

	assert.Contains(t, out, "Edges at or below \"unknown\" confidence: 1 (1 unknown)")
	assert.Contains(t, out, "GET /foo -> unresolved")
	assert.NotContains(t, out, "GET /bar", "the static-confidence edge must not appear at the unknown threshold")
}

// TestPrintUnknownEdges_ProducerResolvedElsewhereIsExcluded proves the fix
// for the overcounting bug found while building this: a producer whose own
// local store recorded "unknown" (a single repo alone can't see another
// service's routes) must not be reported once a better-resolved edge for
// the SAME producer exists elsewhere in the fleet-merged graph (bridge.db,
// in the real case) — otherwise the report double-counts a producer that is
// actually resolved fleet-wide.
func TestPrintUnknownEdges_ProducerResolvedElsewhereIsExcluded(t *testing.T) {
	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".polyflow"), 0o755))
	dbPath := filepath.Join(repoDir, ".polyflow", "graph.db")

	store, err := graph.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer store.Close()

	bw := graph.NewFreshBatchWriter(store)
	require.NoError(t, bw.AddNode(context.Background(), &graph.Node{
		ID: "prod1", Type: "http_client", Label: "GET /cross-svc", Service: "svc", File: "client.go", Line: 30, Language: "go",
	}))
	require.NoError(t, bw.AddNode(context.Background(), &graph.Node{
		ID: "unresolved", Type: "service", Label: "unresolved", Service: "",
	}))
	require.NoError(t, bw.Flush(context.Background()))
	require.NoError(t, bw.AddEdge(context.Background(), &graph.Edge{
		ID: "link:prod1->unresolved", From: "prod1", To: "unresolved", Type: "http_call", Confidence: "unknown",
	}))
	require.NoError(t, bw.Flush(context.Background()))

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoDir))
	t.Cleanup(func() { os.Chdir(origWD) })
	resolvedRepoDir, err := os.Getwd()
	require.NoError(t, err)

	home := t.TempDir()
	t.Setenv("POLYFLOW_HOME", home)
	regPath := filepath.Join(home, "registry.yml")
	require.NoError(t, registry.Save(regPath, &registry.Registry{
		Version: "1",
		Entries: []registry.Entry{{Service: "svc", LocalPath: resolvedRepoDir, Fleets: []string{"myfleet"}}},
	}))

	// bridge.db: the same producer, this time resolved (another fleet
	// member's route is visible from the pooled view bridge-build uses).
	bridgePath, err := fleetsync.DefaultBridgePath("myfleet")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(bridgePath), 0o755))
	bridgeStore, err := graph.NewSQLiteStore(bridgePath)
	require.NoError(t, err)
	bbw := graph.NewFreshBatchWriter(bridgeStore)
	require.NoError(t, bbw.AddNode(context.Background(), &graph.Node{
		ID: "prod1", Type: "http_client", Label: "GET /cross-svc", Service: "svc", File: "client.go", Line: 30, Language: "go",
		Meta: map[string]string{"owner_service": "svc"},
	}))
	require.NoError(t, bbw.AddNode(context.Background(), &graph.Node{
		ID: "remote_handler", Type: "http_handler", Label: "GET /cross-svc", Service: "other",
		Meta: map[string]string{"owner_service": "other"},
	}))
	require.NoError(t, bbw.Flush(context.Background()))
	require.NoError(t, bbw.AddEdge(context.Background(), &graph.Edge{
		ID: "link:prod1->remote_handler", From: "prod1", To: "remote_handler", Type: "http_call", Confidence: "inferred",
	}))
	require.NoError(t, bbw.Flush(context.Background()))
	require.NoError(t, bridgeStore.Close())

	out := captureStdout(t, func() {
		require.NoError(t, printUnknownEdges(context.Background(), store, "unknown"))
	})

	assert.Contains(t, out, "Edges at or below \"unknown\" confidence: 0 ()")
	assert.NotContains(t, out, "GET /cross-svc -> unresolved",
		"prod1 is resolved fleet-wide via bridge.db; its stale local-only unknown edge must not be reported")
}
