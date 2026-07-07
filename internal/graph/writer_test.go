package graph_test

import (
	"context"
	"fmt"
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
