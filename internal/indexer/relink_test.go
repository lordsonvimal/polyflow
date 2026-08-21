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
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// threeServiceFleet builds a fixture with a real cross-service HTTP edge
// (web's fetch('/api/users') -> api's http.HandleFunc("/api/users"), the
// same shape indexer_test.go's TestRun_CrossLinksCountsOnlyBoundaryCrossings
// already proves produces CrossLinks > 0) plus a third, unrelated service
// with no path to either — the "changed service that must not disturb an
// established cross-service edge" scenario FR.5's acceptance test names.
func threeServiceFleet(t *testing.T) (*workspace.WorkspaceConfig, string) {
	t.Helper()
	dir := t.TempDir()

	apiSvc := filepath.Join(dir, "api")
	require.NoError(t, os.MkdirAll(apiSvc, 0o755))
	writeFile(t, apiSvc, "go.mod", "module example.com/api\n\ngo 1.22\n")
	writeFile(t, apiSvc, "main.go", `package main

import "net/http"

func main() {
	http.HandleFunc("/api/users", listUsers)
}

func listUsers(w http.ResponseWriter, r *http.Request) {}
`)

	webSvc := filepath.Join(dir, "web")
	require.NoError(t, os.MkdirAll(webSvc, 0o755))
	writeFile(t, webSvc, "app.js", `async function load() {
  const res = await fetch('/api/users');
  return res;
}
`)

	workerSvc := filepath.Join(dir, "worker")
	require.NoError(t, os.MkdirAll(workerSvc, 0o755))
	writeFile(t, workerSvc, "go.mod", "module example.com/worker\n\ngo 1.22\n")
	writeFile(t, workerSvc, "main.go", `package main

func main() { run() }

func run() {}
`)

	cfg := &workspace.WorkspaceConfig{
		Name: "fleet", Version: "1",
		Services: []workspace.Service{
			{Name: "api", Path: apiSvc, Language: "go"},
			{Name: "web", Path: webSvc, Language: "javascript"},
			{Name: "worker", Path: workerSvc, Language: "go"},
		},
	}
	return cfg, dir
}

// nodeServiceOf builds a nodeID -> Service lookup from a built index.
func nodeServiceOf(idx *graph.AdjacencyIndex) map[string]string {
	m := make(map[string]string, len(idx.Nodes))
	for id, n := range idx.Nodes {
		m[id] = n.Service
	}
	return m
}

// edgesByID builds an edgeID -> Edge lookup from a built index.
func edgesByID(idx *graph.AdjacencyIndex) map[string]graph.Edge {
	m := make(map[string]graph.Edge)
	for _, e := range idx.AllEdges() {
		m[e.ID] = e
	}
	return m
}

// edgesTouching returns the subset of edges with at least one endpoint in svc.
func edgesTouching(edges map[string]graph.Edge, nodeService map[string]string, svc string) map[string]graph.Edge {
	out := make(map[string]graph.Edge)
	for id, e := range edges {
		if nodeService[e.From] == svc || nodeService[e.To] == svc {
			out[id] = e
		}
	}
	return out
}

// TestRelink_LeavesUntouchedServicesAlone is FR.5's original acceptance
// test: relinking a service unrelated to an established cross-service edge
// must not change that edge (same ID, same content) or touch any other
// row belonging to either of its endpoints' services.
func TestRelink_LeavesUntouchedServicesAlone(t *testing.T) {
	cfg, dir := threeServiceFleet(t)
	rootDBDir := filepath.Join(dir, meta.DBDir)

	// Baseline: a full index over all three services, establishing the
	// web -> api cross-service edge.
	baseline, err := Run(context.Background(), Options{
		Config:      cfg,
		DBDir:       rootDBDir,
		PatternsDir: "../../patterns",
		Workers:     2,
	})
	require.NoError(t, err)
	require.Greater(t, baseline.CrossLinks, 0, "fixture must produce a web<->api cross-service edge")

	finalDBPath := filepath.Join(rootDBDir, meta.DBFile)
	preStore, err := graph.NewSQLiteStore(finalDBPath)
	require.NoError(t, err)
	before, err := preStore.BuildIndex(context.Background())
	require.NoError(t, err)
	require.NoError(t, preStore.Close())

	svcBefore := nodeServiceOf(before)
	edgesBefore := edgesByID(before)

	var crossID string
	for id, e := range edgesBefore {
		from, to := svcBefore[e.From], svcBefore[e.To]
		if (from == "web" && to == "api") || (from == "api" && to == "web") {
			crossID = id
			break
		}
	}
	require.NotEmpty(t, crossID, "must find the web<->api cross-service edge in the baseline")

	webOrAPIBefore := make(map[string]graph.Edge)
	for id, e := range edgesTouching(edgesBefore, svcBefore, "web") {
		webOrAPIBefore[id] = e
	}
	for id, e := range edgesTouching(edgesBefore, svcBefore, "api") {
		webOrAPIBefore[id] = e
	}

	// FR.2: index worker alone into its own per-service DB.
	workerDBDir := filepath.Join(rootDBDir, "services", "worker")
	_, err = Run(context.Background(), Options{
		Config:        cfg,
		DBDir:         workerDBDir,
		PatternsDir:   "../../patterns",
		Workers:       2,
		ServiceFilter: []string{"worker"},
		Full:          true,
	})
	require.NoError(t, err)

	// FR.5c: relink only worker.
	relinkStats, err := Relink(context.Background(), RelinkOptions{
		Config:  cfg,
		Service: "worker",
		DBDir:   rootDBDir,
	})
	require.NoError(t, err)
	require.NotNil(t, relinkStats)

	postStore, err := graph.NewSQLiteStore(finalDBPath)
	require.NoError(t, err)
	defer postStore.Close()
	after, err := postStore.BuildIndex(context.Background())
	require.NoError(t, err)

	svcAfter := nodeServiceOf(after)
	edgesAfter := edgesByID(after)

	afterEdge, ok := edgesAfter[crossID]
	require.True(t, ok, "web<->api edge must still exist after relinking worker")
	assert.Equal(t, edgesBefore[crossID], afterEdge, "web<->api edge must be byte-identical (same sources) after relinking worker")

	webOrAPIAfter := make(map[string]graph.Edge)
	for id, e := range edgesTouching(edgesAfter, svcAfter, "web") {
		webOrAPIAfter[id] = e
	}
	for id, e := range edgesTouching(edgesAfter, svcAfter, "api") {
		webOrAPIAfter[id] = e
	}
	assert.Equal(t, webOrAPIBefore, webOrAPIAfter, "no edge touching web or api should change when relinking worker")
}

// TestRelink_NoServiceDB proves Relink fails loudly (not a silent no-op)
// when the named service was never indexed on its own — bug-class #12,
// exhaustive intake.
func TestRelink_NoServiceDB(t *testing.T) {
	cfg, dir := threeServiceFleet(t)
	rootDBDir := filepath.Join(dir, meta.DBDir)

	_, err := Run(context.Background(), Options{
		Config:      cfg,
		DBDir:       rootDBDir,
		PatternsDir: "../../patterns",
		Workers:     2,
	})
	require.NoError(t, err)

	_, err = Relink(context.Background(), RelinkOptions{
		Config:  cfg,
		Service: "worker",
		DBDir:   rootDBDir,
	})
	assert.Error(t, err, "relink must fail when worker was never indexed on its own")
}

// TestRelink_UnknownService proves Relink validates the service name against
// the workspace config rather than merging an empty/garbage path.
func TestRelink_UnknownService(t *testing.T) {
	cfg, dir := threeServiceFleet(t)
	rootDBDir := filepath.Join(dir, meta.DBDir)

	_, err := Relink(context.Background(), RelinkOptions{
		Config:  cfg,
		Service: "does-not-exist",
		DBDir:   rootDBDir,
	})
	assert.Error(t, err)
}
