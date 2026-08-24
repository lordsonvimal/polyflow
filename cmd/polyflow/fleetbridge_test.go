package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/fleetsync"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/registry"
	"github.com/lordsonvimal/polyflow/internal/semantic"
)

// TestBuildFleetAwareIndex_MergesBridgeNodesAndEdges proves GR.3's core CLI/
// MCP wiring: when the current directory is a registered fleet member with
// an already-built bridge.db, buildFleetAwareIndex's plain idx returned by
// store.BuildIndex gains that bridge's cross-service nodes/edges — the same
// merge point every query command (impact/context/trace/deadcode) and the
// MCP server share.
func TestBuildFleetAwareIndex_MergesBridgeNodesAndEdges(t *testing.T) {
	repoDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, ".polyflow"), 0o755))
	localDBPath := filepath.Join(repoDir, ".polyflow", "graph.db")

	localStore, err := graph.NewSQLiteStore(localDBPath)
	require.NoError(t, err)
	bw := graph.NewFreshBatchWriter(localStore)
	require.NoError(t, bw.AddNode(context.Background(), &graph.Node{ID: "n1", Type: "function", Label: "DoThing", Service: "svc", File: "app/foo.go", Line: 1, EndLine: 5, Language: "go"}))
	require.NoError(t, bw.Flush(context.Background()))
	localStore.Close()

	// t.TempDir()'s path and what os.Getwd() reports after os.Chdir() into
	// it can differ on macOS (/tmp is a symlink to /private/tmp) — the
	// registry's LocalPath must match the resolved form buildFleetAwareIndex
	// will derive from "." below, or the reverse-lookup exact-path match
	// misses.
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

	bridgePath, err := fleetsync.DefaultBridgePath("myfleet")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(bridgePath), 0o755))
	bridgeStore, err := graph.NewSQLiteStore(bridgePath)
	require.NoError(t, err)
	bbw := graph.NewFreshBatchWriter(bridgeStore)
	require.NoError(t, bbw.AddNode(context.Background(), &graph.Node{ID: "remote1", Type: "function", Label: "RemoteCaller", Service: "other", File: "other/bar.go", Line: 1, EndLine: 5, Language: "go"}))
	require.NoError(t, bbw.AddNode(context.Background(), &graph.Node{ID: "n1", Type: "function", Label: "DoThing", Service: "svc", File: "app/foo.go", Line: 1, EndLine: 5, Language: "go", Meta: map[string]string{"owner_service": "svc"}}))
	require.NoError(t, bbw.Flush(context.Background()))
	require.NoError(t, bbw.AddEdge(context.Background(), &graph.Edge{ID: "e1", From: "remote1", To: "n1", Type: "http_call"}))
	require.NoError(t, bbw.Flush(context.Background()))
	bridgeStore.Close()

	store, err := graph.NewSQLiteStore(localDBPath)
	require.NoError(t, err)
	defer store.Close()

	idx, err := buildFleetAwareIndex(context.Background(), store)
	require.NoError(t, err)

	require.Contains(t, idx.Nodes, "n1")
	require.Contains(t, idx.Nodes, "remote1")
	callers := idx.InEdges["n1"]
	require.Len(t, callers, 1)
	require.Equal(t, "remote1", callers[0].From)
}

// TestBuildFleetAwareIndex_MergesSiblingMembersFullGraph proves the
// federate-everywhere upgrade: a sibling fleet member's own local graph.db
// (not just the cross-service edge endpoints copied into bridge.db) is
// unioned into idx wholesale, so a node the sibling never exposed via any
// cross-service edge — no meta.owner_service, nothing in bridge.db at all —
// is still reachable from here. This is what makes `polyflow impact`/
// `context`/`trace` from inside one member browse another member's whole
// graph, not just its cross-service touchpoints.
func TestBuildFleetAwareIndex_MergesSiblingMembersFullGraph(t *testing.T) {
	repoA := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoA, ".polyflow"), 0o755))
	dbA := filepath.Join(repoA, ".polyflow", "graph.db")
	storeA, err := graph.NewSQLiteStore(dbA)
	require.NoError(t, err)
	bwA := graph.NewFreshBatchWriter(storeA)
	require.NoError(t, bwA.AddNode(context.Background(), &graph.Node{ID: "a:n1", Type: "function", Label: "Local", Service: "svcA", File: "a.go", Line: 1, EndLine: 5, Language: "go"}))
	require.NoError(t, bwA.Flush(context.Background()))
	storeA.Close()

	repoB := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoB, ".polyflow"), 0o755))
	dbB := filepath.Join(repoB, ".polyflow", "graph.db")
	storeB, err := graph.NewSQLiteStore(dbB)
	require.NoError(t, err)
	bwB := graph.NewFreshBatchWriter(storeB)
	// This node has no meta.owner_service and is never referenced by any
	// cross-service edge — bridge.db would never carry it. It must still
	// show up in the merged idx purely because svcB is a locally-resolved
	// fleet member.
	require.NoError(t, bwB.AddNode(context.Background(), &graph.Node{ID: "b:n1", Type: "function", Label: "NeverBridged", Service: "svcB", File: "b.go", Line: 1, EndLine: 5, Language: "go"}))
	require.NoError(t, bwB.Flush(context.Background()))
	storeB.Close()

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoA))
	t.Cleanup(func() { os.Chdir(origWD) })
	resolvedRepoA, err := os.Getwd()
	require.NoError(t, err)
	resolvedRepoB, err := filepath.EvalSymlinks(repoB)
	require.NoError(t, err)

	home := t.TempDir()
	t.Setenv("POLYFLOW_HOME", home)
	regPath := filepath.Join(home, "registry.yml")
	require.NoError(t, registry.Save(regPath, &registry.Registry{
		Version: "1",
		Entries: []registry.Entry{
			{Service: "svcA", LocalPath: resolvedRepoA, Fleets: []string{"myfleet"}},
			{Service: "svcB", LocalPath: resolvedRepoB, Fleets: []string{"myfleet"}},
		},
	}))

	store, err := graph.NewSQLiteStore(dbA)
	require.NoError(t, err)
	defer store.Close()

	idx, err := buildFleetAwareIndex(context.Background(), store)
	require.NoError(t, err)

	require.Contains(t, idx.Nodes, "a:n1")
	require.Contains(t, idx.Nodes, "b:n1")
	require.Equal(t, "svcB", idx.Nodes["b:n1"].Service)
}

// seedSearchableNode inserts a node plus the minimal entities_fts/embeddings
// rows internal/semantic.Searcher needs to find and describe it via FTS —
// mirrors internal/semantic's own (unexported) test helpers, reimplemented
// here since cmd/polyflow can't import them.
func seedSearchableNode(t *testing.T, dbPath, id, label, service, file string) {
	t.Helper()
	store, err := graph.NewSQLiteStore(dbPath)
	require.NoError(t, err)
	defer store.Close()
	bw := graph.NewFreshBatchWriter(store)
	require.NoError(t, bw.AddNode(context.Background(), &graph.Node{ID: id, Type: "function", Label: label, Service: service, File: file, Line: 1, EndLine: 5, Language: "go"}))
	require.NoError(t, bw.Flush(context.Background()))

	cardText := label + " function " + service + " " + file
	_, err = store.DB().Exec(`INSERT INTO entities_fts (entity_id, entity_type, text) VALUES (?, 'node', ?)`, id, cardText)
	require.NoError(t, err)
	_, err = store.DB().Exec(
		`INSERT INTO embeddings (entity_id, entity_type, content_hash, embedder_id, dims, vector, meta) VALUES (?, 'node', 'h', 'test', 0, x'', '{}')`,
		id)
	require.NoError(t, err)
}

// TestBuildFleetSearchers_FederatesAcrossLocallyKnownMembers proves the
// deferred half of GR.3 (search's full multi-member federation): with two
// registered fleet members' own local graph.dbs on disk, a search from
// inside one of them returns hits from both, each tagged with the service
// that produced it.
func TestBuildFleetSearchers_FederatesAcrossLocallyKnownMembers(t *testing.T) {
	repoA := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoA, ".polyflow"), 0o755))
	dbA := filepath.Join(repoA, ".polyflow", "graph.db")
	seedSearchableNode(t, dbA, "a:fn:getUser", "getUser", "svcA", "user.go")

	repoB := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repoB, ".polyflow"), 0o755))
	dbB := filepath.Join(repoB, ".polyflow", "graph.db")
	seedSearchableNode(t, dbB, "b:fn:getUserProfile", "getUserProfile", "svcB", "user.go")

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(repoA))
	t.Cleanup(func() { os.Chdir(origWD) })
	resolvedRepoA, err := os.Getwd()
	require.NoError(t, err)
	resolvedRepoB, err := filepath.EvalSymlinks(repoB)
	require.NoError(t, err)

	home := t.TempDir()
	t.Setenv("POLYFLOW_HOME", home)
	regPath := filepath.Join(home, "registry.yml")
	require.NoError(t, registry.Save(regPath, &registry.Registry{
		Version: "1",
		Entries: []registry.Entry{
			{Service: "svcA", LocalPath: resolvedRepoA, Fleets: []string{"myfleet"}},
			{Service: "svcB", LocalPath: resolvedRepoB, Fleets: []string{"myfleet"}},
		},
	}))

	fleet, closeFleet, err := buildFleetSearchers(nil, nil)
	require.NoError(t, err)
	defer closeFleet()
	require.Len(t, fleet, 2)
	require.Contains(t, fleet, "svcA")
	require.Contains(t, fleet, "svcB")

	resp, err := semantic.FederatedSearch(context.Background(), fleet, "user", 10)
	require.NoError(t, err)

	byID := map[string]semantic.Hit{}
	for _, h := range resp.Nodes {
		byID[h.Entity.ID] = h
	}
	require.Contains(t, byID, "a:fn:getUser")
	require.Equal(t, "svcA", byID["a:fn:getUser"].Entity.Service)
	require.Contains(t, byID, "b:fn:getUserProfile")
	require.Equal(t, "svcB", byID["b:fn:getUserProfile"].Entity.Service)
}
