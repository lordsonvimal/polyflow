package graph

import (
	"context"
	"database/sql"
	"fmt"
)

const defaultBatchSize = 1000

// BatchWriter accumulates nodes and edges and flushes them to SQLite in batches,
// wrapping each flush in a single transaction for performance.
type BatchWriter struct {
	store     *SQLiteStore
	batchSize int
	nodes     []*Node
	edges     []*Edge
}

// NewBatchWriter creates a BatchWriter with the default batch size.
func NewBatchWriter(store *SQLiteStore) *BatchWriter {
	return NewBatchWriterWithSize(store, defaultBatchSize)
}

// NewBatchWriterWithSize creates a BatchWriter with a custom batch size.
func NewBatchWriterWithSize(store *SQLiteStore, batchSize int) *BatchWriter {
	return &BatchWriter{
		store:     store,
		batchSize: batchSize,
	}
}

// AddNode queues a node, auto-flushing when the batch is full.
func (w *BatchWriter) AddNode(ctx context.Context, n *Node) error {
	w.nodes = append(w.nodes, n)
	if len(w.nodes) >= w.batchSize {
		return w.FlushNodes(ctx)
	}
	return nil
}

// AddEdge queues an edge, auto-flushing when the batch is full.
func (w *BatchWriter) AddEdge(ctx context.Context, e *Edge) error {
	w.edges = append(w.edges, e)
	if len(w.edges) >= w.batchSize {
		return w.FlushEdges(ctx)
	}
	return nil
}

// FlushNodes writes all pending nodes in a single transaction.
func (w *BatchWriter) FlushNodes(ctx context.Context) error {
	if len(w.nodes) == 0 {
		return nil
	}
	batch := w.nodes
	w.nodes = w.nodes[:0]

	return w.store.WithTx(ctx, func(tx *sql.Tx) error {
		for _, n := range batch {
			metaJSON, err := marshalMeta(n.Meta)
			if err != nil {
				return fmt.Errorf("marshal node %s meta: %w", n.ID, err)
			}
			_, err = tx.ExecContext(ctx, `
				INSERT INTO nodes (id, type, label, service, file, line, language, meta)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET
					type=excluded.type, label=excluded.label, service=excluded.service,
					file=excluded.file, line=excluded.line, language=excluded.language,
					meta=excluded.meta`,
				n.ID, string(n.Type), n.Label, n.Service, n.File, n.Line, n.Language, metaJSON)
			if err != nil {
				return fmt.Errorf("upsert node %s: %w", n.ID, err)
			}
			// keep FTS in sync
			if _, err = tx.ExecContext(ctx, `DELETE FROM nodes_fts WHERE id = ?`, n.ID); err != nil {
				return fmt.Errorf("fts delete %s: %w", n.ID, err)
			}
			if _, err = tx.ExecContext(ctx,
				`INSERT INTO nodes_fts (id, label, file, service) VALUES (?, ?, ?, ?)`,
				n.ID, n.Label, n.File, n.Service); err != nil {
				return fmt.Errorf("fts insert %s: %w", n.ID, err)
			}
		}
		return nil
	})
}

// FlushEdges writes all pending edges in a single transaction.
func (w *BatchWriter) FlushEdges(ctx context.Context) error {
	if len(w.edges) == 0 {
		return nil
	}
	batch := w.edges
	w.edges = w.edges[:0]

	return w.store.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range batch {
			metaJSON, err := marshalMeta(e.Meta)
			if err != nil {
				return fmt.Errorf("marshal edge %s meta: %w", e.ID, err)
			}
			_, err = tx.ExecContext(ctx, `
				INSERT INTO edges (id, "from", "to", type, label, meta)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET
					"from"=excluded."from", "to"=excluded."to",
					type=excluded.type, label=excluded.label, meta=excluded.meta`,
				e.ID, e.From, e.To, string(e.Type), e.Label, metaJSON)
			if err != nil {
				return fmt.Errorf("upsert edge %s: %w", e.ID, err)
			}
		}
		return nil
	})
}

// Flush writes all remaining pending nodes and edges.
func (w *BatchWriter) Flush(ctx context.Context) error {
	if err := w.FlushNodes(ctx); err != nil {
		return err
	}
	return w.FlushEdges(ctx)
}
