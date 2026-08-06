package graph_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBatchWriterFlushNodes(t *testing.T) {
	s := newTestStore(t)
	w := graph.NewBatchWriter(s)
	ctx := context.Background()

	for i := range 5 {
		require.NoError(t, w.AddNode(ctx, nodeFixture(fmt.Sprintf("n%d", i))))
	}
	require.NoError(t, w.Flush(ctx))

	n, _, err := s.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 5, n)
}

func TestBatchWriterFlushEdges(t *testing.T) {
	s := newTestStore(t)
	w := graph.NewBatchWriter(s)
	ctx := context.Background()

	require.NoError(t, w.AddNode(ctx, nodeFixture("src")))
	require.NoError(t, w.AddNode(ctx, nodeFixture("dst")))
	require.NoError(t, w.Flush(ctx))

	require.NoError(t, w.AddEdge(ctx, edgeFixture("e1", "src", "dst")))
	require.NoError(t, w.Flush(ctx))

	_, e, err := s.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, e)
}

func TestBatchWriter_FlushEmpty(t *testing.T) {
	s := newTestStore(t)
	w := graph.NewBatchWriter(s)
	ctx := context.Background()
	// Flushing with nothing pending must not error.
	require.NoError(t, w.Flush(ctx))
}

func TestBatchWriter_FlushNodesError(t *testing.T) {
	s, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	w := graph.NewBatchWriter(s)
	ctx := context.Background()
	require.NoError(t, w.AddNode(ctx, nodeFixture("n1")))
	s.Close() // close store to force error on flush
	err = w.FlushNodes(ctx)
	assert.Error(t, err)
}

func TestBatchWriter_FlushEdgesError(t *testing.T) {
	s, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	// Add nodes first so edge FK passes — but we'll close the store before flushing.
	ctx := context.Background()
	w := graph.NewBatchWriter(s)
	require.NoError(t, w.AddNode(ctx, nodeFixture("src")))
	require.NoError(t, w.AddNode(ctx, nodeFixture("dst")))
	require.NoError(t, w.FlushNodes(ctx))
	require.NoError(t, w.AddEdge(ctx, edgeFixture("e1", "src", "dst")))
	s.Close()
	err = w.FlushEdges(ctx)
	assert.Error(t, err)
}

func TestBatchWriterAutoFlush(t *testing.T) {
	s, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	// Use a tiny batch size to trigger auto-flush
	w := graph.NewBatchWriterWithSize(s, 3)
	ctx := context.Background()

	for i := range 7 {
		require.NoError(t, w.AddNode(ctx, nodeFixture(fmt.Sprintf("n%d", i))))
	}
	// 2 auto-flushes happened (batches of 3+3), 1 pending
	require.NoError(t, w.Flush(ctx))

	n, _, err := s.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 7, n)
}

// Regression: an edge-buffer auto-flush must flush pending nodes first.
// Nodes are always queued before their edges per file, but the buffers fill
// at different rates — inserting an edge batch while its endpoints still sit
// in the node buffer violated the FK constraint on any workspace producing
// more than one edge batch.
func TestBatchWriterAutoFlush_EdgesAfterPendingNodes(t *testing.T) {
	s, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	// Node batch (size 5) still pending when the edge batch (3 edges per
	// node-pair round) fills up first.
	w := graph.NewBatchWriterWithSize(s, 3)
	ctx := context.Background()

	for i := range 4 {
		from := fmt.Sprintf("from%d", i)
		to := fmt.Sprintf("to%d", i)
		require.NoError(t, w.AddNode(ctx, nodeFixture(from)))
		require.NoError(t, w.AddNode(ctx, nodeFixture(to)))
		require.NoError(t, w.AddEdge(ctx, edgeFixture(fmt.Sprintf("e%d", i), from, to)))
	}
	require.NoError(t, w.Flush(ctx))

	n, e, err := s.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 8, n)
	assert.Equal(t, 4, e)
}

// Regression: FlushEdges must persist confidence/method/path — the batch
// statement used to drop them, so every indexed edge came back with empty
// confidence (and the UI treated uncertain edges as static).
func TestBatchWriter_EdgeConfidencePersisted(t *testing.T) {
	s, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })

	w := graph.NewBatchWriter(s)
	ctx := context.Background()
	require.NoError(t, w.AddNode(ctx, nodeFixture("a")))
	require.NoError(t, w.AddNode(ctx, nodeFixture("b")))
	e := edgeFixture("e1", "a", "b")
	e.Confidence = graph.ConfidencePartial
	e.Method = "POST"
	e.Path = "/api/users"
	require.NoError(t, w.AddEdge(ctx, e))
	require.NoError(t, w.Flush(ctx))

	idx, err := s.BuildIndex(ctx)
	require.NoError(t, err)
	out := idx.OutEdges["a"]
	require.Len(t, out, 1)
	assert.Equal(t, graph.ConfidencePartial, out[0].Confidence)
	assert.Equal(t, "POST", out[0].Method)
	assert.Equal(t, "/api/users", out[0].Path)
}

// countFTSRows returns how many nodes_fts rows exist for a node ID. The
// invariant every writer must preserve is exactly one row per live node,
// holding that node's current label/file/service.
func countFTSRows(t *testing.T, s *graph.SQLiteStore, id string) int {
	t.Helper()
	var n int
	require.NoError(t, s.DB().QueryRow(`SELECT COUNT(*) FROM nodes_fts WHERE id = ?`, id).Scan(&n))
	return n
}

func ftsLabel(t *testing.T, s *graph.SQLiteStore, id string) string {
	t.Helper()
	var label string
	require.NoError(t, s.DB().QueryRow(`SELECT label FROM nodes_fts WHERE id = ?`, id).Scan(&label))
	return label
}

// A build store re-upserts most nodes several times (semantic pass, linking
// passes, root classification, evidence reconciliation), each through a
// *different* BatchWriter. The FTS journal skips the redundant nodes_fts
// delete+insert, and this pins the invariant it must not break: one row per
// node, never a duplicate, never a stale one.
func TestBuildStoreFTSStaysCanonicalAcrossWriters(t *testing.T) {
	dir := t.TempDir()
	s, err := graph.NewBuildStore(filepath.Join(dir, "graph.db.tmp"))
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()

	// Pass 1: the parse-phase writer mints the node.
	w1 := graph.NewFreshBatchWriter(s)
	require.NoError(t, w1.AddNode(ctx, nodeFixture("n1")))
	require.NoError(t, w1.Flush(ctx))
	assert.Equal(t, 1, countFTSRows(t, s, "n1"))

	// Pass 2: a meta-only rewrite (what root classification does) through a
	// separate writer must leave the FTS row alone, not duplicate it.
	w2 := graph.NewBatchWriter(s)
	metaOnly := nodeFixture("n1")
	metaOnly.Meta = map[string]string{"root_kind": "entrypoint"}
	require.NoError(t, w2.AddNode(ctx, metaOnly))
	require.NoError(t, w2.Flush(ctx))
	assert.Equal(t, 1, countFTSRows(t, s, "n1"), "meta-only re-upsert must not add an FTS row")
	assert.Equal(t, "func_n1", ftsLabel(t, s, "n1"))

	// Pass 3: a genuine label change must replace the row, not append to it.
	w3 := graph.NewBatchWriter(s)
	renamed := nodeFixture("n1")
	renamed.Label = "renamed"
	require.NoError(t, w3.AddNode(ctx, renamed))
	require.NoError(t, w3.Flush(ctx))
	assert.Equal(t, 1, countFTSRows(t, s, "n1"), "label change must replace, not duplicate")
	assert.Equal(t, "renamed", ftsLabel(t, s, "n1"))
}

// A store opened over a pre-existing DB has no journal (it cannot know what
// rows are already there), so it must keep the original delete-then-insert
// behavior and still end at exactly one row.
func TestNonBuildStoreFTSReplacesOnReUpsert(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	w := graph.NewBatchWriter(s)
	require.NoError(t, w.AddNode(ctx, nodeFixture("n1")))
	require.NoError(t, w.Flush(ctx))

	w2 := graph.NewBatchWriter(s)
	renamed := nodeFixture("n1")
	renamed.Label = "renamed"
	require.NoError(t, w2.AddNode(ctx, renamed))
	require.NoError(t, w2.Flush(ctx))

	assert.Equal(t, 1, countFTSRows(t, s, "n1"))
	assert.Equal(t, "renamed", ftsLabel(t, s, "n1"))
}
