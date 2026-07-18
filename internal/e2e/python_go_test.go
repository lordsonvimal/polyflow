package e2e_test

// TestPythonGo_CrossServiceHTTPCall proves the L.P2 core promise:
// a Python FastAPI service calling a Go gin service produces cross-service
// http_call edges with only YAML pattern files added — zero contract-engine
// changes required.

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

const pythonGoPatternsDir = "../../patterns"
const pythonGoFixtureWS = "testdata/python_go"

// indexPythonGo runs the full index pipeline against the python_go fixture
// workspace. Each service gets its own version-filtered matcher so that
// package-gated patterns (fastapi, requests, gin) activate only for the
// service that has the dep. This mirrors what the production indexer does.
func indexPythonGo(t *testing.T) (store *graph.SQLiteStore, cfg *workspace.WorkspaceConfig) {
	t.Helper()

	cfg, err := workspace.Load(filepath.Join(pythonGoFixtureWS, "workspace.yaml"))
	require.NoError(t, err)

	reg, err := patterns.DefaultRegistry(pythonGoPatternsDir)
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
		svcPath := filepath.Join(pythonGoFixtureWS, svc.Path)

		// Resolve deps so version-gated patterns activate correctly.
		svcDeps, err := deps.Resolve(svcPath)
		require.NoError(t, err)

		matcher := patterns.NewTreeSitterMatcherForService(reg, svcDeps)

		var files []string
		err = filepath.WalkDir(svcPath, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			// Skip non-source files (go.mod, requirements.txt are for deps only).
			ext := filepath.Ext(path)
			if ext != ".go" && ext != ".py" {
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

	// Route→handler linking.
	routeEdges := linker.LinkRouteHandlers(allNodes)
	bwRoute := graph.NewBatchWriter(store)
	for i := range routeEdges {
		e := routeEdges[i]
		require.NoError(t, bwRoute.AddEdge(ctx, &e))
		allEdges = append(allEdges, e)
	}
	require.NoError(t, bwRoute.Flush(ctx))

	// Apply workspace link hints (base_url → target_service + path strip).
	hintedNodes := linker.ApplyHints(cfg.Links, allNodes, allEdges)

	// Contract engine: cross-service http_call edges.
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

// TestPythonGo_CrossServiceHTTPCall is the L.P2 acceptance test:
// the Python FastAPI service's requests.get("http://svc-go/users") links
// to the Go gin service's GET /users handler via a cross-service http_call
// edge, with only YAML pattern files added.
func TestPythonGo_CrossServiceHTTPCall(t *testing.T) {
	store, _ := indexPythonGo(t)
	ctx := context.Background()

	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	// Find cross-service http_call edges: from svc-python to svc-go.
	var crossEdges []*graph.Edge
	for _, edges := range idx.OutEdges {
		for _, e := range edges {
			if e.Type != graph.EdgeTypeHTTPCall {
				continue
			}
			fromNode := idx.Nodes[e.From]
			toNode := idx.Nodes[e.To]
			if fromNode == nil || toNode == nil {
				continue
			}
			if fromNode.Service == "svc-python" && toNode.Service == "svc-go" {
				crossEdges = append(crossEdges, e)
			}
		}
	}

	assert.NotEmpty(t, crossEdges,
		"expected cross-service http_call edge from svc-python (requests.get) to svc-go (gin GET /users)")

	// Determinism check: run twice, same result.
	store2, _ := indexPythonGo(t)
	idx2, err := store2.BuildIndex(ctx)
	require.NoError(t, err)

	var crossEdges2 []*graph.Edge
	for _, edges := range idx2.OutEdges {
		for _, e := range edges {
			if e.Type != graph.EdgeTypeHTTPCall {
				continue
			}
			fromNode := idx2.Nodes[e.From]
			toNode := idx2.Nodes[e.To]
			if fromNode == nil || toNode == nil {
				continue
			}
			if fromNode.Service == "svc-python" && toNode.Service == "svc-go" {
				crossEdges2 = append(crossEdges2, e)
			}
		}
	}

	assert.Equal(t, len(crossEdges), len(crossEdges2),
		"cross-service edge count must be deterministic across two runs (rule 2)")
}

// TestPythonGo_PythonHandlerNodes verifies that the FastAPI handler decorators
// produce http_handler nodes in the svc-python service.
func TestPythonGo_PythonHandlerNodes(t *testing.T) {
	store, _ := indexPythonGo(t)
	ctx := context.Background()

	nodes, err := store.SearchNodes(ctx, "items", 50)
	require.NoError(t, err)

	found := false
	for _, n := range nodes {
		if n.Service == "svc-python" && n.Type == graph.NodeTypeHTTPHandler {
			found = true
			break
		}
	}
	assert.True(t, found, "expected http_handler node for @app.get('/items') in svc-python")
}

// TestPythonGo_GoHandlerNodes verifies that gin routes produce http_handler
// nodes in the svc-go service.
func TestPythonGo_GoHandlerNodes(t *testing.T) {
	store, _ := indexPythonGo(t)
	ctx := context.Background()

	nodes, err := store.SearchNodes(ctx, "users", 50)
	require.NoError(t, err)

	found := false
	for _, n := range nodes {
		if n.Service == "svc-go" && n.Type == graph.NodeTypeHTTPHandler {
			found = true
			break
		}
	}
	assert.True(t, found, "expected http_handler node for r.GET('/users') in svc-go")
}
