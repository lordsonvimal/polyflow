package graph

import (
	"context"
	"fmt"
)

const defaultBatchSize = 1000

// BatchWriter accumulates nodes and edges and flushes them to a Store in batches.
type BatchWriter struct {
	store     Store
	batchSize int
	nodes     []*Node
	edges     []*Edge
}

// NewBatchWriter creates a BatchWriter with the default batch size.
func NewBatchWriter(store Store) *BatchWriter {
	return &BatchWriter{
		store:     store,
		batchSize: defaultBatchSize,
	}
}

// AddNode queues a node for writing.
func (w *BatchWriter) AddNode(n *Node) error {
	w.nodes = append(w.nodes, n)
	if len(w.nodes) >= w.batchSize {
		return w.FlushNodes(context.Background())
	}
	return nil
}

// AddEdge queues an edge for writing.
func (w *BatchWriter) AddEdge(e *Edge) error {
	w.edges = append(w.edges, e)
	if len(w.edges) >= w.batchSize {
		return w.FlushEdges(context.Background())
	}
	return nil
}

// FlushNodes writes all pending nodes to the store.
func (w *BatchWriter) FlushNodes(ctx context.Context) error {
	for _, n := range w.nodes {
		if err := w.store.UpsertNode(ctx, n); err != nil {
			return fmt.Errorf("flush node %s: %w", n.ID, err)
		}
	}
	w.nodes = w.nodes[:0]
	return nil
}

// FlushEdges writes all pending edges to the store.
func (w *BatchWriter) FlushEdges(ctx context.Context) error {
	for _, e := range w.edges {
		if err := w.store.UpsertEdge(ctx, e); err != nil {
			return fmt.Errorf("flush edge %s: %w", e.ID, err)
		}
	}
	w.edges = w.edges[:0]
	return nil
}

// Flush writes all pending nodes and edges.
func (w *BatchWriter) Flush(ctx context.Context) error {
	if err := w.FlushNodes(ctx); err != nil {
		return err
	}
	return w.FlushEdges(ctx)
}
