package e2e_test

// Tests for the PW.1 core promise: a browser `new WebSocket("ws://host/path")`
// links to a route-style WS server (Go gorilla, Python FastAPI @app.websocket)
// via contracts/websocket.yaml's connect-time rule variant, proving the fix
// is general across both languages — not the rule 2 `ws` library shape
// ws_connect_test.go already covers.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/deps"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

const wsUpgradeRoutePatternsDir = "../../patterns"
const wsUpgradeRouteFixtureWS = "testdata/ws_upgrade_route"

func indexWSUpgradeRoute(t *testing.T) (store *graph.SQLiteStore, cfg *workspace.WorkspaceConfig) {
	t.Helper()

	cfg, err := workspace.Load(filepath.Join(wsUpgradeRouteFixtureWS, "polyflow.yml"))
	require.NoError(t, err)

	reg, err := patterns.DefaultRegistry(wsUpgradeRoutePatternsDir)
	require.NoError(t, err)

	tmpDB := filepath.Join(t.TempDir(), "graph.db")
	store, err = graph.NewSQLiteStore(tmpDB)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	bw := graph.NewBatchWriter(store)

	var allNodes []graph.Node
	var allEdges []graph.Edge

	for _, svc := range cfg.Services {
		svcPath := svc.Path

		svcDeps, err := deps.Resolve(svcPath, "")
		require.NoError(t, err)

		matcher := patterns.NewTreeSitterMatcherForService(reg, svcDeps)

		var files []string
		err = filepath.WalkDir(svcPath, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			ext := filepath.Ext(path)
			if ext != ".js" && ext != ".go" && ext != ".py" {
				return nil
			}
			files = append(files, path)
			return nil
		})
		require.NoError(t, err)

		pool := parser.NewWorkerPool(2, matcher, svc.Name)
		for result := range pool.Run(files) {
			if result.Err != nil {
				_ = store.UpsertParseError(ctx, &graph.ParseError{
					FilePath:   result.File,
					Service:    svc.Name,
					ErrorCount: 1,
					IndexedAt:  time.Now().Unix(),
				})
				continue
			}
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

	routeEdges := linker.LinkRouteHandlers(allNodes)
	bwRoute := graph.NewBatchWriter(store)
	for i := range routeEdges {
		e := routeEdges[i]
		require.NoError(t, bwRoute.AddEdge(ctx, &e))
		allEdges = append(allEdges, e)
	}
	require.NoError(t, bwRoute.Flush(ctx))

	// PW.1: stamp the registering route's path/method onto Go's bare
	// ws_upgrade node before ApplyHints/the contract engine run.
	wsUpdated := linker.LinkWSUpgradeRoute(allNodes)
	if len(wsUpdated) > 0 {
		nodeByID := make(map[string]int, len(allNodes))
		for i, n := range allNodes {
			nodeByID[n.ID] = i
		}
		bwWS := graph.NewBatchWriter(store)
		for i := range wsUpdated {
			n := wsUpdated[i]
			require.NoError(t, bwWS.AddNode(ctx, &n))
			if idx, ok := nodeByID[n.ID]; ok {
				allNodes[idx] = n
			}
		}
		require.NoError(t, bwWS.Flush(ctx))
	}

	hintedNodes := linker.ApplyHints(cfg.Links, allNodes, allEdges)

	contractRules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	eng := &contract.Engine{}
	contractResult := eng.Link(hintedNodes, contractRules, cfg.Links)

	bwN := graph.NewBatchWriter(store)
	for i := range contractResult.Nodes {
		n := contractResult.Nodes[i]
		_ = bwN.AddNode(ctx, &n)
	}
	require.NoError(t, bwN.Flush(ctx))

	bwE := graph.NewBatchWriter(store)
	for i := range contractResult.Edges {
		e := contractResult.Edges[i]
		require.NoError(t, bwE.AddEdge(ctx, &e))
	}
	require.NoError(t, bwE.Flush(ctx))

	require.NoError(t, store.SetMeta(ctx, "last_indexed", "1234567890"))
	return store, cfg
}

func crossWSUpgradeEdges(idx *graph.AdjacencyIndex, fromSvc, toSvc string) []*graph.Edge {
	var out []*graph.Edge
	for _, edges := range idx.OutEdges {
		for _, e := range edges {
			if e.Type != graph.EdgeTypeWSConnect {
				continue
			}
			fromNode := idx.Nodes[e.From]
			toNode := idx.Nodes[e.To]
			if fromNode == nil || toNode == nil {
				continue
			}
			if fromNode.Service == fromSvc && toNode.Service == toSvc {
				out = append(out, e)
			}
		}
	}
	return out
}

// TestWSUpgradeRoute_GoCrossServiceEdge is PW.1 gate 2: svc-client's
// `new WebSocket("ws://svc-go/notifications")` links to svc-go's gorilla
// `upgrader.Upgrade` handler (registered by gin at "/notifications") via a
// cross-service ws_connect edge — the route-style-WS-server case rule 2
// (Node.js `ws` library only) cannot reach.
func TestWSUpgradeRoute_GoCrossServiceEdge(t *testing.T) {
	t.Parallel()
	store, _ := indexWSUpgradeRoute(t)
	ctx := context.Background()

	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	crossEdges := crossWSUpgradeEdges(idx, "svc-client", "svc-go")
	assert.NotEmpty(t, crossEdges,
		"expected cross-service ws_connect edge from svc-client to svc-go's gorilla ws_upgrade handler")

	store2, _ := indexWSUpgradeRoute(t)
	idx2, err := store2.BuildIndex(ctx)
	require.NoError(t, err)
	crossEdges2 := crossWSUpgradeEdges(idx2, "svc-client", "svc-go")
	assert.Equal(t, len(crossEdges), len(crossEdges2), "cross-service edge count must be deterministic")
}

// TestWSUpgradeRoute_PythonCrossServiceEdge is PW.1 gate 1: svc-client's
// `new WebSocket("ws://svc-py/updates")` links to svc-py's FastAPI
// `@app.websocket("/updates")` handler via a cross-service ws_connect edge.
func TestWSUpgradeRoute_PythonCrossServiceEdge(t *testing.T) {
	t.Parallel()
	store, _ := indexWSUpgradeRoute(t)
	ctx := context.Background()

	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	crossEdges := crossWSUpgradeEdges(idx, "svc-client", "svc-py")
	assert.NotEmpty(t, crossEdges,
		"expected cross-service ws_connect edge from svc-client to svc-py's @app.websocket('/updates') handler")
}

// TestWSUpgradeRoute_PlainHandlerNotJoined is PW.1 gate 4: svc-go's plain
// GET /health handler (not ws_upgrade*) must never be treated as a
// WebSocket connect-time consumer, even though it is an ordinary
// http_handler like the real target.
func TestWSUpgradeRoute_PlainHandlerNotJoined(t *testing.T) {
	t.Parallel()
	store, _ := indexWSUpgradeRoute(t)
	ctx := context.Background()

	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	for _, edges := range idx.OutEdges {
		for _, e := range edges {
			if e.Type != graph.EdgeTypeWSConnect {
				continue
			}
			toNode := idx.Nodes[e.To]
			require.NotNil(t, toNode)
			assert.NotEqual(t, "healthCheck", toNode.Label, "plain HTTP handler must not be a ws_connect target")
		}
	}
}
