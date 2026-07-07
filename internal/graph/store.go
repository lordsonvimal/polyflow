package graph

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Schema is the SQLite DDL for the polyflow graph database.
const Schema = `
CREATE TABLE IF NOT EXISTS nodes (
	id       TEXT PRIMARY KEY,
	type     TEXT NOT NULL,
	label    TEXT NOT NULL,
	service  TEXT NOT NULL,
	file     TEXT NOT NULL,
	line     INTEGER NOT NULL DEFAULT 0,
	language TEXT NOT NULL,
	meta     TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS edges (
	id     TEXT PRIMARY KEY,
	"from" TEXT NOT NULL,
	"to"   TEXT NOT NULL,
	type   TEXT NOT NULL,
	label  TEXT NOT NULL DEFAULT '',
	meta   TEXT NOT NULL DEFAULT '{}',
	FOREIGN KEY("from") REFERENCES nodes(id),
	FOREIGN KEY("to")   REFERENCES nodes(id)
);

CREATE INDEX IF NOT EXISTS idx_nodes_service  ON nodes(service);
CREATE INDEX IF NOT EXISTS idx_nodes_type     ON nodes(type);
CREATE INDEX IF NOT EXISTS idx_edges_from     ON edges("from");
CREATE INDEX IF NOT EXISTS idx_edges_to       ON edges("to");
CREATE INDEX IF NOT EXISTS idx_edges_type     ON edges(type);
CREATE VIRTUAL TABLE IF NOT EXISTS nodes_fts USING fts5(id, label, file, service);
`

// Store is the persistence interface for the polyflow graph.
type Store interface {
	// UpsertNode inserts or replaces a node.
	UpsertNode(ctx context.Context, n *Node) error
	// UpsertEdge inserts or replaces an edge.
	UpsertEdge(ctx context.Context, e *Edge) error
	// GetNode fetches a node by ID.
	GetNode(ctx context.Context, id string) (*Node, error)
	// GetEdge fetches an edge by ID.
	GetEdge(ctx context.Context, id string) (*Edge, error)
	// SearchNodes performs a full-text search against labels/files.
	SearchNodes(ctx context.Context, query string, limit int) ([]*Node, error)
	// ListEdgesFrom returns all edges originating from nodeID.
	ListEdgesFrom(ctx context.Context, nodeID string) ([]*Edge, error)
	// ListEdgesTo returns all edges terminating at nodeID.
	ListEdgesTo(ctx context.Context, nodeID string) ([]*Edge, error)
	// BuildIndex loads the full graph into an AdjacencyIndex for traversal.
	BuildIndex(ctx context.Context) (*AdjacencyIndex, error)
	// Stats returns aggregate counts of nodes and edges.
	Stats(ctx context.Context) (nodeCount, edgeCount int, err error)
	// Close releases the database connection.
	Close() error
}

// SQLiteStore is the SQLite-backed implementation of Store.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) the SQLite database at dsn and applies the schema.
func NewSQLiteStore(dsn string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(Schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &SQLiteStore{db: db}, nil
}

func (s *SQLiteStore) UpsertNode(ctx context.Context, n *Node) error {
	// TODO: implement
	return fmt.Errorf("not yet implemented")
}

func (s *SQLiteStore) UpsertEdge(ctx context.Context, e *Edge) error {
	// TODO: implement
	return fmt.Errorf("not yet implemented")
}

func (s *SQLiteStore) GetNode(ctx context.Context, id string) (*Node, error) {
	// TODO: implement
	return nil, fmt.Errorf("not yet implemented")
}

func (s *SQLiteStore) GetEdge(ctx context.Context, id string) (*Edge, error) {
	// TODO: implement
	return nil, fmt.Errorf("not yet implemented")
}

func (s *SQLiteStore) SearchNodes(ctx context.Context, query string, limit int) ([]*Node, error) {
	// TODO: implement FTS5 query
	return nil, fmt.Errorf("not yet implemented")
}

func (s *SQLiteStore) ListEdgesFrom(ctx context.Context, nodeID string) ([]*Edge, error) {
	// TODO: implement
	return nil, fmt.Errorf("not yet implemented")
}

func (s *SQLiteStore) ListEdgesTo(ctx context.Context, nodeID string) ([]*Edge, error) {
	// TODO: implement
	return nil, fmt.Errorf("not yet implemented")
}

func (s *SQLiteStore) BuildIndex(ctx context.Context) (*AdjacencyIndex, error) {
	// TODO: load all nodes and edges, build AdjacencyIndex
	return NewAdjacencyIndex(), fmt.Errorf("not yet implemented")
}

func (s *SQLiteStore) Stats(ctx context.Context) (int, int, error) {
	// TODO: implement
	return 0, 0, fmt.Errorf("not yet implemented")
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
