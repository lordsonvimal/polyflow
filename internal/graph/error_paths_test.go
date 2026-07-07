package graph_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSQLiteStoreInvalidDSN(t *testing.T) {
	// A path that can't be created (directory as file)
	_, err := graph.NewSQLiteStore("/dev/null/impossible.db")
	assert.Error(t, err)
}

func TestUpsertEdgeMissingNodes(t *testing.T) {
	s := newTestStore(t)
	// Foreign key violation: n1 and n2 don't exist
	err := s.UpsertEdge(context.Background(), edgeFixture("e1", "n1", "n2"))
	assert.Error(t, err)
}

func TestGetEdgeNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetEdge(context.Background(), "nonexistent")
	assert.Error(t, err)
}

func TestSearchNodesEmpty(t *testing.T) {
	s := newTestStore(t)
	results, err := s.SearchNodes(context.Background(), "nothing", 10)
	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestWithTxRollbackOnError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.UpsertNode(ctx, nodeFixture("n1")))

	sentinel := errors.New("forced failure")
	err := s.WithTx(ctx, func(_ *sql.Tx) error {
		return sentinel
	})
	assert.ErrorIs(t, err, sentinel)

	// n1 still exists — the rolled-back tx didn't corrupt state
	_, err = s.GetNode(ctx, "n1")
	require.NoError(t, err)
}

func TestBatchWriterFlushEmptyIsNoop(t *testing.T) {
	s := newTestStore(t)
	w := graph.NewBatchWriter(s)
	ctx := context.Background()

	// Flushing nothing should not error
	require.NoError(t, w.Flush(ctx))
	n, e, err := s.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, 0, e)
}

func TestBatchWriterAddEdgeAutoFlush(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.UpsertNode(ctx, nodeFixture("a")))
	require.NoError(t, s.UpsertNode(ctx, nodeFixture("b")))
	require.NoError(t, s.UpsertNode(ctx, nodeFixture("c")))
	require.NoError(t, s.UpsertNode(ctx, nodeFixture("d")))

	w := graph.NewBatchWriterWithSize(s, 2)
	require.NoError(t, w.AddEdge(ctx, edgeFixture("e1", "a", "b")))
	require.NoError(t, w.AddEdge(ctx, edgeFixture("e2", "b", "c")))
	// auto-flush triggered at size 2
	require.NoError(t, w.AddEdge(ctx, edgeFixture("e3", "c", "d")))
	require.NoError(t, w.Flush(ctx))

	_, ec, err := s.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, ec)
}

func TestBuildIndexEmpty(t *testing.T) {
	s := newTestStore(t)
	idx, err := s.BuildIndex(context.Background())
	require.NoError(t, err)
	assert.Empty(t, idx.Nodes)
	assert.Empty(t, idx.OutEdges)
	assert.Empty(t, idx.InEdges)
}

func TestNodeWithNilMeta(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	n := nodeFixture("nm")
	n.Meta = nil
	require.NoError(t, s.UpsertNode(ctx, n))

	got, err := s.GetNode(ctx, "nm")
	require.NoError(t, err)
	assert.Nil(t, got.Meta)
}
