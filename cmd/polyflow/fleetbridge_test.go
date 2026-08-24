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
