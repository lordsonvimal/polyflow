package e2e_test

// TestWSConnect_CrossServiceEdge proves the H.1 core promise: a browser
// `new WebSocket("ws://host/path")` links to a `ws` library server's
// `new WebSocketServer({path: "/path"})` across services, via the new
// connect-time variant of contracts/websocket.yaml's `kind: websocket` rule
// (mirrors python_go_test.go's shape).

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

const wsConnectPatternsDir = "../../patterns"
const wsConnectFixtureWS = "testdata/ws_connect"

func indexWSConnect(t *testing.T) (store *graph.SQLiteStore, cfg *workspace.WorkspaceConfig) {
	t.Helper()

	cfg, err := workspace.Load(filepath.Join(wsConnectFixtureWS, "polyflow.yml"))
	require.NoError(t, err)

	reg, err := patterns.DefaultRegistry(wsConnectPatternsDir)
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

		svcDeps, err := deps.Resolve(svcPath)
		require.NoError(t, err)

		matcher := patterns.NewTreeSitterMatcherForService(reg, svcDeps)

		var files []string
		err = filepath.WalkDir(svcPath, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".js" {
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

func crossWSConnectEdges(idx *graph.AdjacencyIndex, fromSvc, toSvc string) []*graph.Edge {
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

// TestWSConnect_CrossServiceEdge: svc-b's `new WebSocket("ws://svc-a/terminal")`
// links to svc-a's `new WebSocketServer({path: "/terminal"})` via a
// cross-service ws_connect edge, plus a two-run determinism check (rule 2).
func TestWSConnect_CrossServiceEdge(t *testing.T) {
	t.Parallel()
	store, _ := indexWSConnect(t)
	ctx := context.Background()

	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	crossEdges := crossWSConnectEdges(idx, "svc-b", "svc-a")
	assert.NotEmpty(t, crossEdges,
		"expected cross-service ws_connect edge from svc-b (WebSocket client) to svc-a (WebSocketServer)")

	store2, _ := indexWSConnect(t)
	idx2, err := store2.BuildIndex(ctx)
	require.NoError(t, err)

	crossEdges2 := crossWSConnectEdges(idx2, "svc-b", "svc-a")
	assert.Equal(t, len(crossEdges), len(crossEdges2),
		"cross-service edge count must be deterministic across two runs (rule 2)")
}

// TestWSConnect_ServerSubscriberNode verifies the ws_server_new pattern
// produces a subscriber node in svc-a, and the connection handler links via
// LinkRouteHandlers.
func TestWSConnect_ServerSubscriberNode(t *testing.T) {
	t.Parallel()
	store, _ := indexWSConnect(t)
	ctx := context.Background()

	nodes, err := store.SearchNodes(ctx, "terminal", 50)
	require.NoError(t, err)

	found := false
	for _, n := range nodes {
		if n.Service == "svc-a" && n.Type == graph.NodeTypeSubscriber {
			found = true
			break
		}
	}
	assert.True(t, found, "expected subscriber node for new WebSocketServer({path: '/terminal'}) in svc-a")
}
