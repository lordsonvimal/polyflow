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

// testWorkspace builds a small two-language workspace in a temp dir and
// returns the config plus the dir.
func testWorkspace(t *testing.T) (*workspace.WorkspaceConfig, string) {
	t.Helper()
	dir := t.TempDir()

	goSvc := filepath.Join(dir, "backend")
	require.NoError(t, os.MkdirAll(goSvc, 0o755))
	writeFile(t, goSvc, "go.mod", "module example.com/backend\n\ngo 1.22\n")
	writeFile(t, goSvc, "main.go", `package main

import "net/http"

func main() {
	http.HandleFunc("/api/users", listUsers)
}

func listUsers(w http.ResponseWriter, r *http.Request) {}
`)

	jsSvc := filepath.Join(dir, "frontend")
	require.NoError(t, os.MkdirAll(jsSvc, 0o755))
	writeFile(t, jsSvc, "app.js", `async function load() {
  const res = await fetch('/api/users');
  return res;
}
`)

	cfg := &workspace.WorkspaceConfig{
		Name: "test", Version: "1",
		Services: []workspace.Service{
			{Name: "backend", Path: goSvc, Language: "go"},
			{Name: "frontend", Path: jsSvc, Language: "javascript"},
		},
	}
	return cfg, dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func runIndexer(t *testing.T, cfg *workspace.WorkspaceConfig, dbDir string, full bool) *Stats {
	t.Helper()
	stats, err := Run(context.Background(), Options{
		Config:      cfg,
		DBDir:       dbDir,
		PatternsDir: "../../patterns",
		Workers:     2,
		Full:        full,
	})
	require.NoError(t, err)
	return stats
}

func TestRun_IncrementalSkipsUnchangedFiles(t *testing.T) {
	cfg, dir := testWorkspace(t)
	dbDir := filepath.Join(dir, ".polyflow")

	// Cold run: everything parses.
	first := runIndexer(t, cfg, dbDir, false)
	assert.Equal(t, 2, first.TotalFiles)
	assert.Equal(t, 2, first.ParsedFiles)
	assert.Equal(t, 0, first.SkippedFiles)
	assert.Greater(t, first.Nodes, 0)

	// No changes: zero files parse, graph identical.
	second := runIndexer(t, cfg, dbDir, false)
	assert.Equal(t, 0, second.ParsedFiles, "unchanged files must not re-parse")
	assert.Equal(t, 2, second.SkippedFiles)
	assert.Equal(t, first.Nodes, second.Nodes, "carried-over graph must be identical")
	assert.Equal(t, first.Edges, second.Edges)

	// Edit one file: only that file re-parses.
	writeFile(t, cfg.Services[1].Path, "app.js", `async function load() {
  const res = await fetch('/api/users');
  const extra = await fetch('/api/games');
  return res;
}
`)
	third := runIndexer(t, cfg, dbDir, false)
	assert.Equal(t, 1, third.ParsedFiles, "only the edited file re-parses")
	assert.Equal(t, 1, third.SkippedFiles)
	// The new fetch adds its client node, and /api/games has no matching
	// handler so the shared "unresolved" sink node appears too.
	assert.Equal(t, first.Nodes+2, third.Nodes, "new fetch call adds client + unresolved sink")
}

func TestRun_FullForcesReparse(t *testing.T) {
	cfg, dir := testWorkspace(t)
	dbDir := filepath.Join(dir, ".polyflow")

	runIndexer(t, cfg, dbDir, false)
	full := runIndexer(t, cfg, dbDir, true)
	assert.Equal(t, 2, full.ParsedFiles, "--full ignores the cache")
	assert.Equal(t, 0, full.SkippedFiles)
}

func TestRun_DeletedFileDropsNodes(t *testing.T) {
	cfg, dir := testWorkspace(t)
	dbDir := filepath.Join(dir, ".polyflow")

	first := runIndexer(t, cfg, dbDir, false)
	require.NoError(t, os.Remove(filepath.Join(cfg.Services[1].Path, "app.js")))
	second := runIndexer(t, cfg, dbDir, false)
	assert.Equal(t, 1, second.TotalFiles)
	assert.Less(t, second.Nodes, first.Nodes, "deleted file's nodes must disappear")
}

func TestRun_CrossServiceLinkSurvivesIncremental(t *testing.T) {
	cfg, dir := testWorkspace(t)
	dbDir := filepath.Join(dir, ".polyflow")

	countCrossEdges := func() int {
		store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
		require.NoError(t, err)
		defer store.Close()
		idx, err := store.BuildIndex(context.Background())
		require.NoError(t, err)
		n := 0
		for _, edges := range idx.OutEdges {
			for _, e := range edges {
				from, to := idx.Nodes[e.From], idx.Nodes[e.To]
				if from != nil && to != nil && from.Service != to.Service {
					n++
				}
			}
		}
		return n
	}

	first := runIndexer(t, cfg, dbDir, false)
	require.Greater(t, first.CrossLinks, 0, "fetch('/api/users') should link to the Go handler")
	wantCross := countCrossEdges()
	require.Greater(t, wantCross, 0)

	// Unchanged re-run takes the no-change fast path: nothing parses and the
	// stored cross-service links are untouched.
	second := runIndexer(t, cfg, dbDir, false)
	assert.Equal(t, 0, second.ParsedFiles)
	assert.Equal(t, wantCross, countCrossEdges(), "cross-service links survive the fast path")

	// Touching a file bypasses the fast path; linking re-runs on the merged
	// set and produces the same cross-service links.
	writeFile(t, cfg.Services[1].Path, "app.js", `async function load() {
  const res = await fetch('/api/users');
  return res; // touched
}
`)
	third := runIndexer(t, cfg, dbDir, false)
	assert.Equal(t, first.CrossLinks, third.CrossLinks, "linking passes re-run on cached results")
	assert.Equal(t, wantCross, countCrossEdges())
}
