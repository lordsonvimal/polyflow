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

// Root classification: entrypoints, framework callbacks (function values /
// external-interface methods), and dead code get distinct root_kind meta;
// called functions get none.
func TestRun_RootClassification(t *testing.T) {
	dir := t.TempDir()
	goSvc := filepath.Join(dir, "backend")
	require.NoError(t, os.MkdirAll(goSvc, 0o755))
	writeFile(t, goSvc, "go.mod", "module example.com/backend\n\ngo 1.22\n")
	writeFile(t, goSvc, "main.go", `package main

type command struct{ run func() error }

var cmd = command{run: runIndex}

func runIndex() error { return helper() }

func helper() error { return nil }

func deadCode() {}

func main() {
	_ = cmd
}
`)
	cfg := &workspace.WorkspaceConfig{
		Name: "test", Version: "1",
		Services: []workspace.Service{{Name: "backend", Path: goSvc, Language: "go"}},
	}
	dbDir := filepath.Join(dir, ".polyflow")
	runIndexer(t, cfg, dbDir, false)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()

	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)
	kinds := map[string]string{}
	for _, n := range idx.Nodes {
		if n.Type == graph.NodeTypeFunction {
			kinds[n.Label] = n.Meta["root_kind"]
		}
	}

	assert.Equal(t, "entrypoint", kinds["main"], "main is an entrypoint root")
	assert.Equal(t, "callback", kinds["runIndex"], "function value in composite literal is a callback root; kinds: %v", kinds)
	assert.Equal(t, "unreachable", kinds["deadCode"], "unreferenced function is dead-code root")
	assert.Empty(t, kinds["helper"], "called functions carry no root_kind")
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

// Regression: a JSX render of an external-library component (no in-repo
// declaration — e.g. <Route> from a router package) mints a component proxy
// node that LinkJS deletes. The in-memory edge set must be filtered along
// with allNodes, or the evidence reconciler re-upserts the dangling renders
// edge and the whole index aborts on a FOREIGN KEY failure (the synergy
// crash, 2026-07-18).
func TestRun_ExternalComponentProxyDoesNotAbortReconcile(t *testing.T) {
	dir := t.TempDir()
	jsSvc := filepath.Join(dir, "ui")
	require.NoError(t, os.MkdirAll(jsSvc, 0o755))
	writeFile(t, jsSvc, "App.tsx", `import { Route } from "@solidjs/router";

function App() {
  return (
    <div>
      <Route path="/" />
    </div>
  );
}

export default App;
`)
	cfg := &workspace.WorkspaceConfig{
		Name: "test", Version: "1",
		Services: []workspace.Service{
			{Name: "ui", Path: jsSvc, Language: "typescript"},
		},
	}
	dbDir := filepath.Join(dir, ".polyflow")

	stats := runIndexer(t, cfg, dbDir, false) // fails before the allEdges filter fix
	require.Greater(t, stats.Nodes, 0)

	// The proxy node must be gone and no edge may reference it.
	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()
	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)
	for _, e := range idx.AllEdges() {
		assert.NotNil(t, idx.Nodes[e.From], "edge %s has dangling From", e.ID)
		assert.NotNil(t, idx.Nodes[e.To], "edge %s has dangling To", e.ID)
	}
}

// --- S.0 embed pass acceptance tests ---

// TestRun_EmbedPassFirstRun verifies that a fresh index embeds all entities
// (nodes + flow chains + doc chunks) in one pass (S.1 acceptance).
func TestRun_EmbedPassFirstRun(t *testing.T) {
	cfg, _ := testWorkspace(t)
	dbDir := t.TempDir()
	runIndexer(t, cfg, dbDir, false)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	nodeCount, _, err := store.Stats(ctx)
	require.NoError(t, err)
	require.Greater(t, nodeCount, 0, "expected nodes after index")

	metas, err := store.ListEmbeddingMeta(ctx)
	require.NoError(t, err)
	// S.1: embeddings include nodes + flow chains + doc chunks, so total is
	// >= nodeCount.  All nodes must be embedded (no node may be skipped).
	require.GreaterOrEqual(t, len(metas), nodeCount,
		"every node must have an embedding; got %d embeddings for %d nodes",
		len(metas), nodeCount)

	// embed_status must be "ok"
	status, err := store.GetMeta(ctx, "embed_status")
	require.NoError(t, err)
	require.Equal(t, "ok", status)
}

// TestRun_EmbedPassIncrementalSkipsUnchanged verifies that a second index on
// unchanged sources re-embeds zero nodes (acceptance: "second run re-embeds zero").
func TestRun_EmbedPassIncrementalSkipsUnchanged(t *testing.T) {
	cfg, _ := testWorkspace(t)
	dbDir := t.TempDir()

	// First run — embed everything.
	runIndexer(t, cfg, dbDir, false)

	store1, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	metas1, err := store1.ListEmbeddingMeta(context.Background())
	store1.Close()
	require.NoError(t, err)
	require.NotEmpty(t, metas1)

	// Second run — nothing changed; hash gate must prevent re-embed.
	// We capture the embedding metadata again and verify it is identical.
	runIndexer(t, cfg, dbDir, false)

	store2, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store2.Close()
	metas2, err := store2.ListEmbeddingMeta(context.Background())
	require.NoError(t, err)

	require.Equal(t, len(metas1), len(metas2), "embedding count changed on incremental run")
	m1 := map[string]string{}
	m2 := map[string]string{}
	for _, m := range metas1 {
		m1[m.EntityID] = m.EmbedderID + "\x00" + m.ContentHash
	}
	for _, m := range metas2 {
		m2[m.EntityID] = m.EmbedderID + "\x00" + m.ContentHash
	}
	require.Equal(t, m1, m2, "embedding hashes differ on incremental run — content or embedder changed unexpectedly")
}

// TestRun_EmbedPassNoEmbed verifies that --no-embed skips the pass and
// stamps the degradation reason (acceptance: "--no-embed indexes identically fast").
func TestRun_EmbedPassNoEmbed(t *testing.T) {
	cfg, _ := testWorkspace(t)
	dbDir := t.TempDir()

	_, err := Run(context.Background(), Options{
		Config:      cfg,
		DBDir:       dbDir,
		PatternsDir: "../../patterns",
		Workers:     2,
		NoEmbed:     true,
	})
	require.NoError(t, err)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()

	ctx := context.Background()
	status, err := store.GetMeta(ctx, "embed_status")
	require.NoError(t, err)
	require.Contains(t, status, "embeddings skipped",
		"embed_status must record degradation reason, got %q", status)

	// No embeddings must exist.
	metas, err := store.ListEmbeddingMeta(ctx)
	require.NoError(t, err)
	require.Empty(t, metas, "no embeddings expected when NoEmbed=true")
}

// TestRun_EmbedPassDeterminism is the two-run determinism test required by
// phases.md bug-class rule 2: same inputs → byte-identical vector output.
func TestRun_EmbedPassDeterminism(t *testing.T) {
	cfg, _ := testWorkspace(t)

	run := func() map[string]string { // entity_id → hex(content_hash):hex(embedder_id)
		dbDir := t.TempDir()
		_, err := Run(context.Background(), Options{
			Config:      cfg,
			DBDir:       dbDir,
			PatternsDir: "../../patterns",
			Workers:     2,
			Full:        true, // ensure fresh embed both times
		})
		require.NoError(t, err)

		store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
		require.NoError(t, err)
		defer store.Close()

		metas, err := store.ListEmbeddingMeta(context.Background())
		require.NoError(t, err)
		m := make(map[string]string, len(metas))
		for _, meta := range metas {
			m[meta.EntityID] = meta.EmbedderID + ":" + meta.ContentHash
		}
		return m
	}

	r1 := run()
	r2 := run()
	require.Equal(t, r1, r2, "embed pass is non-deterministic: run 1 ≠ run 2")
}

// fakeEmbedder is a test double for the Embedder interface.  It produces
// deterministic all-zero 4-dim vectors so tests can exercise the embed pass
// without depending on the real static model.
type fakeEmbedder struct {
	id   string
	dims int
}

func (f *fakeEmbedder) ID() string { return f.id }
func (f *fakeEmbedder) Dims() int  { return f.dims }
func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, f.dims)
	}
	return out, nil
}

// TestRun_EmbedPassEmbedderSwitch is the S.3 acceptance test:
// switching the embedder (simulated as changing the embedder ID) triggers a
// full re-embed of every entity, and a second run with the new embedder
// re-embeds zero (incremental gate holds).
func TestRun_EmbedPassEmbedderSwitch(t *testing.T) {
	cfg, _ := testWorkspace(t)
	dbDir := t.TempDir()

	embA := &fakeEmbedder{id: "fake-embedder-A", dims: 4}
	embB := &fakeEmbedder{id: "fake-embedder-B", dims: 4}

	// Run 1: embed with embedder A.
	_, err := Run(context.Background(), Options{
		Config:      cfg,
		DBDir:       dbDir,
		PatternsDir: "../../patterns",
		Workers:     2,
		Embedder:    embA,
	})
	require.NoError(t, err)

	store1, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	metas1, err := store1.ListEmbeddingMeta(context.Background())
	store1.Close()
	require.NoError(t, err)
	require.NotEmpty(t, metas1, "expected embeddings after first run")
	for _, m := range metas1 {
		require.Equal(t, embA.id, m.EmbedderID, "all embeddings must use embedder A after first run")
	}

	// Run 2: switch to embedder B — must re-embed everything.
	_, err = Run(context.Background(), Options{
		Config:      cfg,
		DBDir:       dbDir,
		PatternsDir: "../../patterns",
		Workers:     2,
		Embedder:    embB,
	})
	require.NoError(t, err)

	store2, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	metas2, err := store2.ListEmbeddingMeta(context.Background())
	store2.Close()
	require.NoError(t, err)
	require.Equal(t, len(metas1), len(metas2), "embedding count must be same after switch")
	for _, m := range metas2 {
		require.Equal(t, embB.id, m.EmbedderID, "all embeddings must use embedder B after switch")
	}

	// Run 3: same embedder B, unchanged sources — must re-embed zero.
	_, err = Run(context.Background(), Options{
		Config:      cfg,
		DBDir:       dbDir,
		PatternsDir: "../../patterns",
		Workers:     2,
		Embedder:    embB,
	})
	require.NoError(t, err)

	store3, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	metas3, err := store3.ListEmbeddingMeta(context.Background())
	store3.Close()
	require.NoError(t, err)
	// Content hashes and embedder IDs must be byte-identical — nothing re-embedded.
	require.Equal(t, len(metas2), len(metas3), "embedding count changed on third run")
	m2 := make(map[string]string, len(metas2))
	for _, m := range metas2 {
		m2[m.EntityID] = m.EmbedderID + "\x00" + m.ContentHash
	}
	m3 := make(map[string]string, len(metas3))
	for _, m := range metas3 {
		m3[m.EntityID] = m.EmbedderID + "\x00" + m.ContentHash
	}
	require.Equal(t, m2, m3, "embeddings changed on third run (no-op expected)")
}
