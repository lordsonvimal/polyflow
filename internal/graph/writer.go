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

	// fresh: the target DB started empty (index builds write to a new tmp
	// file), so an FTS row can only pre-exist for IDs this writer already
	// wrote. `DELETE FROM nodes_fts WHERE id = ?` is a full FTS-table scan
	// (id is not the rowid), which made index builds O(n²) — in fresh mode
	// the delete runs only on the rare duplicate-ID re-upsert.
	fresh   bool
	ftsSeen map[string]bool
}

// NewBatchWriter creates a BatchWriter with the default batch size.
func NewBatchWriter(store *SQLiteStore) *BatchWriter {
	return NewBatchWriterWithSize(store, defaultBatchSize)
}

// NewFreshBatchWriter creates a BatchWriter for a database known to start
// empty, enabling the FTS fast path (see the fresh field).
func NewFreshBatchWriter(store *SQLiteStore) *BatchWriter {
	w := NewBatchWriterWithSize(store, defaultBatchSize)
	w.fresh = true
	w.ftsSeen = make(map[string]bool)
	return w
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

// AddEdge queues an edge, auto-flushing when the batch is full. Pending
// nodes are flushed first: an edge's endpoints may still be sitting in the
// node buffer, and inserting the edge before them violates the FK
// constraint (hit by any workspace producing more than one edge batch).
func (w *BatchWriter) AddEdge(ctx context.Context, e *Edge) error {
	w.edges = append(w.edges, e)
	if len(w.edges) >= w.batchSize {
		if err := w.FlushNodes(ctx); err != nil {
			return err
		}
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

	// Statements are prepared once per transaction: per-row ExecContext
	// re-parses the SQL on every call, which dominates large index builds.
	return w.store.WithTx(ctx, func(tx *sql.Tx) error {
		upsert, err := tx.PrepareContext(ctx, `
			INSERT INTO nodes (id, type, label, service, file, line, language, meta)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				type=excluded.type, label=excluded.label, service=excluded.service,
				file=excluded.file, line=excluded.line, language=excluded.language,
				meta=excluded.meta`)
		if err != nil {
			return fmt.Errorf("prepare node upsert: %w", err)
		}
		defer upsert.Close()
		ftsDelete, err := tx.PrepareContext(ctx, `DELETE FROM nodes_fts WHERE id = ?`)
		if err != nil {
			return fmt.Errorf("prepare fts delete: %w", err)
		}
		defer ftsDelete.Close()
		ftsInsert, err := tx.PrepareContext(ctx,
			`INSERT INTO nodes_fts (id, label, file, service) VALUES (?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare fts insert: %w", err)
		}
		defer ftsInsert.Close()

		for _, n := range batch {
			metaJSON, err := marshalMeta(n.Meta)
			if err != nil {
				return fmt.Errorf("marshal node %s meta: %w", n.ID, err)
			}
			if _, err = upsert.ExecContext(ctx,
				n.ID, string(n.Type), n.Label, n.Service, n.File, n.Line, n.Language, metaJSON); err != nil {
				return fmt.Errorf("upsert node %s: %w", n.ID, err)
			}
			// keep FTS in sync; skip the (full-scan) delete when this is the
			// first time a fresh-DB writer sees the ID
			if !w.fresh || w.ftsSeen[n.ID] {
				if _, err = ftsDelete.ExecContext(ctx, n.ID); err != nil {
					return fmt.Errorf("fts delete %s: %w", n.ID, err)
				}
			}
			if w.fresh {
				w.ftsSeen[n.ID] = true
			}
			if _, err = ftsInsert.ExecContext(ctx, n.ID, n.Label, n.File, n.Service); err != nil {
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
		// Full column set, matching UpsertEdge — the previous statement
		// dropped confidence/method/path, so every batch-indexed edge lost
		// its confidence level in the stored graph.
		upsert, err := tx.PrepareContext(ctx, `
			INSERT INTO edges (id, "from", "to", type, label, meta, confidence, method, path)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				"from"=excluded."from", "to"=excluded."to",
				type=excluded.type, label=excluded.label, meta=excluded.meta,
				confidence=excluded.confidence, method=excluded.method, path=excluded.path`)
		if err != nil {
			return fmt.Errorf("prepare edge upsert: %w", err)
		}
		defer upsert.Close()

		for _, e := range batch {
			metaJSON, err := marshalMeta(e.Meta)
			if err != nil {
				return fmt.Errorf("marshal edge %s meta: %w", e.ID, err)
			}
			confidence := e.Confidence
			if confidence == "" {
				confidence = e.Meta["confidence"]
			}
			method := e.Method
			if method == "" {
				method = e.Meta["method"]
			}
			path := e.Path
			if path == "" {
				path = e.Meta["path"]
			}
			if _, err = upsert.ExecContext(ctx,
				e.ID, e.From, e.To, string(e.Type), e.Label, metaJSON,
				confidence, method, path); err != nil {
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
