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
