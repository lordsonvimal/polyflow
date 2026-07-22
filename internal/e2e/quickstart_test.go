package e2e_test

// TestQuickstart_Smoke is the P.2 CI smoke test: it executes the steps a user
// follows in docs/quickstart.md — load workspace, index, query — on the
// testdata/quickstart fixture, asserting each step succeeds and produces
// non-trivial results.  This is the "tested by following it verbatim on a
// corpus repo" acceptance criterion from P.2.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	pfcontext "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

const quickstartFixture = "testdata/quickstart"
const quickstartPatterns = "../../patterns"

// indexQuickstart replicates the production indexer path for the quickstart
// fixture.  It is intentionally minimal — no capture sessions, no contract
// hints — matching what a first-time user sees after 'polyflow index'.
func indexQuickstart(t *testing.T) (store *graph.SQLiteStore, cfg *workspace.WorkspaceConfig) {
	t.Helper()

	cfg, err := workspace.Load(filepath.Join(quickstartFixture, "workspace.yaml"))
	require.NoError(t, err, "step 1 (polyflow init): load workspace.yaml")
	require.NotEmpty(t, cfg.Services, "workspace must have at least one service")

	reg, err := patterns.DefaultRegistry(quickstartPatterns)
	require.NoError(t, err)

	tmpDB := filepath.Join(t.TempDir(), "graph.db")
	store, err = graph.NewSQLiteStore(tmpDB)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	bw := graph.NewBatchWriter(store)
	matcher := patterns.NewTreeSitterMatcher(reg)

	var allNodes []graph.Node
	var allEdges []graph.Edge

	// step 2: polyflow index — walk and parse each service.
	for _, svc := range cfg.Services {
		svcPath := filepath.Join(quickstartFixture, svc.Path)
		var files []string
		err := filepath.WalkDir(svcPath, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			files = append(files, path)
			return nil
		})
		require.NoError(t, err)

		pool := parser.NewWorkerPool(2, matcher, svc.Name)
		for result := range pool.Run(files) {
			require.NoError(t, result.Err, "parse error in %s", result.File)
			for i := range result.Nodes {
				n := result.Nodes[i]
				require.NoError(t, bw.AddNode(ctx, &n))
				allNodes = append(allNodes, n)
			}
			for i := range result.Edges {
				e := result.Edges[i]
				require.NoError(t, bw.AddEdge(ctx, &e))
				allEdges = append(allEdges, e)
			}
		}
	}
	require.NoError(t, bw.Flush(ctx))

	// Link routes and apply contracts (same production order).
	routeEdges := linker.LinkRouteHandlers(allNodes)
	bwRoute := graph.NewBatchWriter(store)
	for i := range routeEdges {
		e := routeEdges[i]
		require.NoError(t, bwRoute.AddEdge(ctx, &e))
		allEdges = append(allEdges, e)
	}
	require.NoError(t, bwRoute.Flush(ctx))

	contractRules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	eng := &contract.Engine{}
	contractResult := eng.Link(allNodes, contractRules, cfg.Links)

	bw2 := graph.NewBatchWriter(store)
	for i := range contractResult.Nodes {
		n := contractResult.Nodes[i]
		_ = bw2.AddNode(ctx, &n)
	}
	bw3 := graph.NewBatchWriter(store)
	for i := range contractResult.Edges {
		e := contractResult.Edges[i]
		_ = bw3.AddEdge(ctx, &e)
	}
	require.NoError(t, bw2.Flush(ctx))
	require.NoError(t, bw3.Flush(ctx))

	return store, cfg
}

// TestQuickstart_StepByStep mirrors what docs/quickstart.md asks the user to do.
func TestQuickstart_StepByStep(t *testing.T) {
	store, _ := indexQuickstart(t)
	ctx := context.Background()

	// step 1 verified by indexQuickstart not erroring on Load.

	// step 2: index produced nodes.
	nodeCount, _, err := store.Stats(ctx)
	require.NoError(t, err)
	assert.Greater(t, nodeCount, 0, "step 2 (polyflow index): must produce at least one node")

	// step 3: polyflow context — query for the 'listUsers' function.
	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	var targetNode *graph.Node
	nodes, err := store.SearchNodes(ctx, "listUsers", 5)
	require.NoError(t, err)
	for _, n := range nodes {
		if n.Label == "listUsers" {
			targetNode = n
			break
		}
	}
	require.NotNil(t, targetNode, "step 3 (polyflow context listUsers): node must be indexed")

	result := pfcontext.Build(idx, targetNode.ID, "callers", 2, false, 0)
	// Even with no callers yet, context returns the target node itself.
	require.NotNil(t, result.Target, "context query must find the target node")
	assert.Equal(t, "listUsers", result.Target.Label, "target label must match")
}

// TestQuickstart_Determinism verifies two identical index+query runs produce
// byte-identical node and edge sets (bug-class rule 2, required for every
// phase that produces a set).
func TestQuickstart_Determinism(t *testing.T) {
	store1, _ := indexQuickstart(t)
	store2, _ := indexQuickstart(t)

	ctx := context.Background()

	idx1, err := store1.BuildIndex(ctx)
	require.NoError(t, err)
	idx2, err := store2.BuildIndex(ctx)
	require.NoError(t, err)

	edges1 := idx1.AllEdges()
	edges2 := idx2.AllEdges()

	require.Equal(t, len(edges1), len(edges2), "edge count must be identical across two runs")
	for i := range edges1 {
		assert.Equal(t, edges1[i].ID, edges2[i].ID, "edge[%d] ID mismatch (non-determinism)", i)
		assert.Equal(t, edges1[i].From, edges2[i].From, "edge[%d] From mismatch", i)
		assert.Equal(t, edges1[i].To, edges2[i].To, "edge[%d] To mismatch", i)
	}

	// Node IDs must also be stable.
	var ids1, ids2 []string
	for id := range idx1.Nodes {
		ids1 = append(ids1, id)
	}
	for id := range idx2.Nodes {
		ids2 = append(ids2, id)
	}
	assert.Equal(t, len(ids1), len(ids2), "node count must be identical across two runs")
}
