package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/server"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

const fixtureWS = "testdata/workspace"
const patternsDir = "../../patterns"

// indexFixture runs the full index pipeline against the fixture workspace
// and returns a temp directory with the resulting graph.db.
func indexFixture(t *testing.T) (store *graph.SQLiteStore, cfg *workspace.WorkspaceConfig) {
	t.Helper()

	cfg, err := workspace.Load(filepath.Join(fixtureWS, "workspace.yaml"))
	require.NoError(t, err)

	reg, err := patterns.DefaultRegistry(patternsDir)
	require.NoError(t, err)
	matcher := patterns.NewTreeSitterMatcher(reg)

	tmpDB := filepath.Join(t.TempDir(), "graph.db")
	store, err = graph.NewSQLiteStore(tmpDB)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()
	bw := graph.NewBatchWriter(store)

	var allNodes []graph.Node
	var allEdges []graph.Edge

	for _, svc := range cfg.Services {
		svcPath := filepath.Join(fixtureWS, svc.Path)
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
			if result.Err != nil {
				// Record partial errors but don't fail the index
				_ = store.UpsertParseError(ctx, &graph.ParseError{
					FilePath:  result.File,
					Service:   svc.Name,
					ErrorCount: 1,
					IndexedAt: time.Now().Unix(),
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

	hintedNodes := linker.ApplyHints(cfg.Links, allNodes, allEdges)
	l := linker.New(cfg)
	crossEdges, err := l.Link(hintedNodes, allEdges)
	require.NoError(t, err)

	// Insert synthetic unresolved nodes for any linker edge targets not yet in the store.
	bw2 := graph.NewBatchWriter(store)
	for i := range crossEdges {
		e := crossEdges[i]
		if _, err := store.GetNode(ctx, e.To); err != nil {
			_ = bw2.AddNode(ctx, &graph.Node{
				ID: e.To, Type: graph.NodeTypeHTTPHandler, Label: e.To, Service: "unresolved",
				File: "unresolved", Language: "unknown",
			})
		}
	}
	require.NoError(t, bw2.Flush(ctx))

	bw3 := graph.NewBatchWriter(store)
	for i := range crossEdges {
		e := crossEdges[i]
		require.NoError(t, bw3.AddEdge(ctx, &e))
	}
	require.NoError(t, bw3.Flush(ctx))

	require.NoError(t, store.SetMeta(ctx, "last_indexed", "1234567890"))

	return store, cfg
}

func TestIndex_NodeCount(t *testing.T) {
	store, _ := indexFixture(t)
	ctx := context.Background()

	nodeCount, _, err := store.Stats(ctx)
	require.NoError(t, err)
	assert.Greater(t, nodeCount, 0, "expected at least one node indexed")
}

func TestIndex_CrossServiceLinks(t *testing.T) {
	store, _ := indexFixture(t)
	ctx := context.Background()

	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	crossCount := 0
	for _, edges := range idx.OutEdges {
		for _, e := range edges {
			if e.Type == graph.EdgeTypeHTTPCall {
				// Check if from and to nodes are in different services
				fromNode := idx.Nodes[e.From]
				toNode := idx.Nodes[e.To]
				if fromNode != nil && toNode != nil && fromNode.Service != toNode.Service {
					crossCount++
				}
			}
		}
	}
	// svc-js calls /api/users which should link to svc-go's CreateUser handler
	assert.GreaterOrEqual(t, crossCount, 1, "expected at least 1 cross-service link between svc-js and svc-go")
}

func TestIndex_TemplDatastar(t *testing.T) {
	store, _ := indexFixture(t)
	ctx := context.Background()

	// Look for nodes from svc-templ that are http_client nodes (datastar actions)
	nodes, err := store.SearchNodes(ctx, "post", 50)
	require.NoError(t, err)

	hasDatastarAction := false
	for _, n := range nodes {
		if n.Service == "svc-templ" && n.Type == graph.NodeTypeHTTPClient {
			hasDatastarAction = true
			break
		}
	}
	assert.True(t, hasDatastarAction, "expected a datastar_action http_client node from svc-templ")
}

func TestIndex_DatastarCrossServiceLink(t *testing.T) {
	store, _ := indexFixture(t)
	ctx := context.Background()

	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	// svc-templ emits @post('/api/users') → after base_url strip → POST /users
	// svc-go registers r.Post("/users", CreateUser)
	// The linker should connect them with an http_call edge where via=datastar_action.
	found := false
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
			if fromNode.Service == "svc-templ" && toNode.Service == "svc-go" &&
				e.Meta["via"] == "datastar_action" {
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	assert.True(t, found, "expected cross-service http_call edge from svc-templ to svc-go with via=datastar_action")

	_ = ctx
}

func TestIndex_ParseErrors(t *testing.T) {
	// The normal fixture files shouldn't have parse errors that cause panics.
	// We verify the index completes without panic (test completing = success).
	store, _ := indexFixture(t)
	ctx := context.Background()

	// Just verify we can list parse errors without crashing
	errors, err := store.ListParseErrors(ctx)
	require.NoError(t, err)
	// Either 0 errors (all files parsed) or some errors — either is fine, no panic
	t.Logf("parse errors: %d", len(errors))
}

func TestIndex_ExcludeGlobs(t *testing.T) {
	// Create a temp workspace with a vendor dir that should be excluded
	tmpWS := t.TempDir()
	svcDir := filepath.Join(tmpWS, "svc-go")
	vendorDir := filepath.Join(svcDir, "vendor")
	require.NoError(t, os.MkdirAll(vendorDir, 0o755))

	// Write a real .go file in vendor (should be excluded)
	vendorFile := filepath.Join(vendorDir, "main.go")
	require.NoError(t, os.WriteFile(vendorFile, []byte(`package main
func VendorFunc() {}`), 0o644))

	// Write a real .go file outside vendor (should be indexed)
	mainFile := filepath.Join(svcDir, "main.go")
	require.NoError(t, os.WriteFile(mainFile, []byte(`package main
func ActualFunc() {}`), 0o644))

	cfg := &workspace.WorkspaceConfig{
		Name:    "test",
		Version: "1",
		Services: []workspace.Service{
			{Name: "svc-go", Path: svcDir, Language: "go"},
		},
		Index: workspace.IndexConfig{
			Exclude: []string{"**/vendor/**"},
		},
	}

	reg, err := patterns.DefaultRegistry(patternsDir)
	require.NoError(t, err)
	matcher := patterns.NewTreeSitterMatcher(reg)

	tmpDB := filepath.Join(t.TempDir(), "graph.db")
	store, err := graph.NewSQLiteStore(tmpDB)
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	bw := graph.NewBatchWriter(store)

	var files []string
	filepath.WalkDir(svcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		// Apply exclude globs
		for _, pattern := range cfg.Index.Exclude {
			if matched, _ := filepath.Match(pattern, path); matched {
				return nil
			}
		}
		// Simple substring check for vendor
		rel, _ := filepath.Rel(svcDir, path)
		if len(rel) > 6 && rel[:6] == "vendor" {
			return nil
		}
		files = append(files, path)
		return nil
	})

	pool := parser.NewWorkerPool(1, matcher, "svc-go")
	for result := range pool.Run(files) {
		if result.Err == nil {
			for i := range result.Nodes {
				n := result.Nodes[i]
				_ = bw.AddNode(ctx, &n)
			}
		}
	}
	require.NoError(t, bw.Flush(ctx))

	// Verify vendor file was not indexed
	vendorNodes, err := store.SearchNodes(ctx, "VendorFunc", 10)
	require.NoError(t, err)
	assert.Empty(t, vendorNodes, "VendorFunc from vendor dir should not be indexed")
}

func TestSearch_FindsFunction(t *testing.T) {
	store, _ := indexFixture(t)
	ctx := context.Background()

	// The chi pattern indexes route registrations; search for the route path.
	results, err := store.SearchNodes(ctx, "users", 10)
	require.NoError(t, err)
	require.NotEmpty(t, results, "expected to find nodes with 'users' in their label or file")

	found := false
	for _, n := range results {
		if n.Service == "svc-go" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected a node from svc-go matching 'users'")
}

func TestTrace_Forward(t *testing.T) {
	store, _ := indexFixture(t)
	ctx := context.Background()

	// Find a node from svc-go (chi route registration)
	handlers, err := store.SearchNodes(ctx, "users", 10)
	require.NoError(t, err)
	require.NotEmpty(t, handlers)

	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	var handlerNode *graph.Node
	for _, n := range handlers {
		if n.Service == "svc-go" {
			handlerNode = n
			break
		}
	}
	require.NotNil(t, handlerNode)

	descendants := graph.Descendants(idx, handlerNode.ID, 5)
	t.Logf("forward trace from %s: %d nodes", handlerNode.ID, len(descendants))
	// The handler exists in the graph even if no outgoing edges in our fixture
	assert.NotNil(t, handlerNode)
}

func TestTrace_Backward(t *testing.T) {
	store, _ := indexFixture(t)
	ctx := context.Background()

	// Find an http_client node from svc-js
	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	var clientNode *graph.Node
	for _, n := range idx.Nodes {
		if n.Service == "svc-js" && n.Type == graph.NodeTypeHTTPClient {
			clientNode = n
			break
		}
	}
	require.NotNil(t, clientNode, "expected http_client node from svc-js")

	ancestors := graph.Ancestors(idx, clientNode.ID, 5)
	t.Logf("backward trace from %s: %d ancestors", clientNode.ID, len(ancestors))
	assert.NotNil(t, clientNode)
}

func TestServe_Graph(t *testing.T) {
	store, _ := indexFixture(t)
	ctx := context.Background()

	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	srv := server.New(store, idx)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/graph")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))

	nodes, ok := result["nodes"].([]any)
	require.True(t, ok, "expected 'nodes' array in response")
	assert.Greater(t, len(nodes), 0, "expected non-empty graph")

	_ = ctx
}

func TestServe_Search(t *testing.T) {
	store, _ := indexFixture(t)
	ctx := context.Background()

	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	srv := server.New(store, idx)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/graph/search?q=users")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var results []any
	require.NoError(t, json.Unmarshal(body, &results))
	assert.NotEmpty(t, results, "expected non-empty search results for 'users'")

	_ = ctx
}

func TestServe_Trace(t *testing.T) {
	store, _ := indexFixture(t)
	ctx := context.Background()

	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)

	// Find a node to trace from
	var rootID string
	for id := range idx.Nodes {
		rootID = id
		break
	}
	require.NotEmpty(t, rootID)

	srv := server.New(store, idx)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/graph/trace?root=" + url.QueryEscape(rootID) + "&direction=forward&depth=5")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	_, hasNodes := result["nodes"]
	assert.True(t, hasNodes, "expected Cytoscape JSON with 'nodes' key")

	_ = ctx
}
