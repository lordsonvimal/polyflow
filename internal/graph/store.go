package graph

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// Schema is the SQLite DDL for the polyflow graph database.
const Schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

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
	id                   TEXT PRIMARY KEY,
	"from"               TEXT NOT NULL REFERENCES nodes(id),
	"to"                 TEXT NOT NULL REFERENCES nodes(id),
	type                 TEXT NOT NULL,
	label                TEXT NOT NULL DEFAULT '',
	meta                 TEXT NOT NULL DEFAULT '{}',
	confidence           TEXT NOT NULL DEFAULT '',
	method               TEXT NOT NULL DEFAULT '',
	path                 TEXT NOT NULL DEFAULT '',
	sources_json         TEXT NOT NULL DEFAULT '[]',
	verification_state   TEXT NOT NULL DEFAULT '',
	verified_granularity TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_nodes_service    ON nodes(service);
CREATE INDEX IF NOT EXISTS idx_nodes_type       ON nodes(type);
CREATE INDEX IF NOT EXISTS idx_nodes_file       ON nodes(file);
CREATE INDEX IF NOT EXISTS idx_edges_from       ON edges("from");
CREATE INDEX IF NOT EXISTS idx_edges_to         ON edges("to");
CREATE INDEX IF NOT EXISTS idx_edges_type       ON edges(type);
CREATE INDEX IF NOT EXISTS idx_edges_confidence ON edges(confidence);
CREATE INDEX IF NOT EXISTS idx_edges_method     ON edges(method);
CREATE INDEX IF NOT EXISTS idx_edges_path       ON edges(path);

CREATE VIRTUAL TABLE IF NOT EXISTS nodes_fts USING fts5(id UNINDEXED, label, file, service);

CREATE TABLE IF NOT EXISTS parse_errors (
	file_path        TEXT PRIMARY KEY,
	service          TEXT NOT NULL,
	error_count      INTEGER NOT NULL DEFAULT 1,
	first_error_line INTEGER NOT NULL DEFAULT 0,
	indexed_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

-- Incremental indexing: per-file content hash plus the parse results for the
-- file, so unchanged files skip tree-sitter entirely on re-index.
CREATE TABLE IF NOT EXISTS file_hashes (
	file_path    TEXT PRIMARY KEY,
	service      TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	indexed_at   INTEGER NOT NULL,
	nodes_json      TEXT NOT NULL DEFAULT '[]',
	edges_json      TEXT NOT NULL DEFAULT '[]',
	unresolved_json TEXT NOT NULL DEFAULT '[]',
	errored      INTEGER NOT NULL DEFAULT 0
);

-- References the indexer saw but could not resolve to a node: the graph's
-- blind-spot ledger, reported by "polyflow status".
CREATE TABLE IF NOT EXISTS unresolved_refs (
	service TEXT NOT NULL,
	file    TEXT NOT NULL,
	line    INTEGER NOT NULL,
	name    TEXT NOT NULL,
	kind    TEXT NOT NULL,
	PRIMARY KEY (service, file, line, name, kind)
);

-- Whole-service semantic (go/packages) results, keyed by a fingerprint of
-- all the service's file hashes.
CREATE TABLE IF NOT EXISTS semantic_cache (
	service         TEXT PRIMARY KEY,
	fingerprint     TEXT NOT NULL,
	nodes_json      TEXT NOT NULL DEFAULT '[]',
	edges_json      TEXT NOT NULL DEFAULT '[]',
	referenced_json TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS dependencies (
	service   TEXT NOT NULL,
	ecosystem TEXT NOT NULL,
	name      TEXT NOT NULL,
	version   TEXT NOT NULL,
	kind      TEXT NOT NULL DEFAULT 'prod',
	PRIMARY KEY (service, ecosystem, name)
);

CREATE INDEX IF NOT EXISTS idx_dependencies_name ON dependencies(name);
`

// Store is the persistence interface for the polyflow graph.
type Store interface {
	UpsertNode(ctx context.Context, n *Node) error
	UpsertEdge(ctx context.Context, e *Edge) error
	GetNode(ctx context.Context, id string) (*Node, error)
	GetEdge(ctx context.Context, id string) (*Edge, error)
	SearchNodes(ctx context.Context, query string, limit int) ([]*Node, error)
	ListEdgesFrom(ctx context.Context, nodeID string) ([]*Edge, error)
	ListEdgesTo(ctx context.Context, nodeID string) ([]*Edge, error)
	BuildIndex(ctx context.Context) (*AdjacencyIndex, error)
	Stats(ctx context.Context) (nodeCount, edgeCount int, err error)
	// UpsertParseError records (or updates) a parse error for a file.
	UpsertParseError(ctx context.Context, pe *ParseError) error
	// ListParseErrors returns all files that had parse errors during the last index.
	ListParseErrors(ctx context.Context) ([]*ParseError, error)
	// UpsertDependency records one resolved dependency version for a service.
	UpsertDependency(ctx context.Context, d *Dependency) error
	// ListDependencies returns resolved dependencies, optionally filtered by
	// service (empty service = all).
	ListDependencies(ctx context.Context, service string) ([]*Dependency, error)
	// SetMeta stores a key-value metadata entry.
	SetMeta(ctx context.Context, key, value string) error
	// GetMeta retrieves a metadata entry by key.
	GetMeta(ctx context.Context, key string) (string, error)
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
	// single writer connection; WAL set in schema
	db.SetMaxOpenConns(1)
	// Wait for concurrent holders (e.g. a running `polyflow serve`) instead
	// of failing instantly with SQLITE_BUSY.
	if _, err := db.Exec(`PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply busy_timeout: %w", err)
	}
	if _, err := db.Exec(Schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	// F.0 migration: add provenance columns to existing DBs that were created
	// before SchemaVersion 15.  ALTER TABLE ADD COLUMN ignores the error when
	// the column already exists (SQLite returns "duplicate column name"),
	// keeping this idempotent across versions without requiring IF NOT EXISTS
	// (which was added in SQLite 3.37 and is not available in all embedded builds).
	for _, col := range []struct{ stmt, name string }{
		{`ALTER TABLE edges ADD COLUMN sources_json TEXT NOT NULL DEFAULT '[]'`, "sources_json"},
		{`ALTER TABLE edges ADD COLUMN verification_state TEXT NOT NULL DEFAULT ''`, "verification_state"},
		{`ALTER TABLE edges ADD COLUMN verified_granularity TEXT NOT NULL DEFAULT ''`, "verified_granularity"},
	} {
		if _, merr := db.Exec(col.stmt); merr != nil {
			// "duplicate column name" is the expected error when the column
			// already exists — ignore it; propagate anything else.
			if !isDuplicateColumn(merr) {
				db.Close()
				return nil, fmt.Errorf("migrate edges.%s: %w", col.name, merr)
			}
		}
	}
	return &SQLiteStore{db: db}, nil
}

// isDuplicateColumn reports whether err is a SQLite "duplicate column name" error.
func isDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column name")
}

// NewBuildStore opens a store tuned for bulk index builds: durability is
// relaxed (synchronous=OFF, in-memory journal) because the indexer writes to
// a throwaway temp file and atomically renames it over the real DB only
// after the whole build succeeds — a crash mid-build loses nothing.
func NewBuildStore(dsn string) (*SQLiteStore, error) {
	s, err := NewSQLiteStore(dsn)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.Exec(`PRAGMA synchronous=OFF; PRAGMA journal_mode=MEMORY;`); err != nil {
		s.Close()
		return nil, fmt.Errorf("apply build pragmas: %w", err)
	}
	return s, nil
}

func (s *SQLiteStore) UpsertNode(ctx context.Context, n *Node) error {
	metaJSON, err := marshalMeta(n.Meta)
	if err != nil {
		return fmt.Errorf("marshal node meta: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
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
	if err := s.upsertFTS(ctx, n); err != nil {
		return err
	}
	return nil
}

// upsertFTS keeps the nodes_fts virtual table in sync with the nodes table.
func (s *SQLiteStore) upsertFTS(ctx context.Context, n *Node) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM nodes_fts WHERE id = ?`, n.ID); err != nil {
		return fmt.Errorf("fts delete %s: %w", n.ID, err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO nodes_fts (id, label, file, service) VALUES (?, ?, ?, ?)`,
		n.ID, n.Label, n.File, n.Service); err != nil {
		return fmt.Errorf("fts insert %s: %w", n.ID, err)
	}
	return nil
}

func (s *SQLiteStore) UpsertEdge(ctx context.Context, e *Edge) error {
	metaJSON, err := marshalMeta(e.Meta)
	if err != nil {
		return fmt.Errorf("marshal edge meta: %w", err)
	}
	sourcesJSON, err := marshalSources(e.Sources)
	if err != nil {
		return fmt.Errorf("marshal edge sources: %w", err)
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
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO edges (id, "from", "to", type, label, meta, confidence, method, path, sources_json, verification_state, verified_granularity)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			"from"=excluded."from", "to"=excluded."to",
			type=excluded.type, label=excluded.label, meta=excluded.meta,
			confidence=excluded.confidence, method=excluded.method, path=excluded.path,
			sources_json=excluded.sources_json,
			verification_state=excluded.verification_state,
			verified_granularity=excluded.verified_granularity`,
		e.ID, e.From, e.To, string(e.Type), e.Label, metaJSON,
		confidence, method, path, sourcesJSON, e.VerificationState, e.VerifiedGranularity)
	if err != nil {
		return fmt.Errorf("upsert edge %s: %w", e.ID, err)
	}
	return nil
}

func (s *SQLiteStore) GetNode(ctx context.Context, id string) (*Node, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, type, label, service, file, line, language, meta FROM nodes WHERE id = ?`, id)
	return scanNode(row)
}

func (s *SQLiteStore) GetEdge(ctx context.Context, id string) (*Edge, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, "from", "to", type, label, meta, confidence, method, path, sources_json, verification_state, verified_granularity FROM edges WHERE id = ?`, id)
	return scanEdge(row)
}

func (s *SQLiteStore) SearchNodes(ctx context.Context, query string, limit int) ([]*Node, error) {
	// FTS5 prefix search: append * for prefix matching. Nodes whose label is
	// an exact (case-insensitive) match for the query rank above prefix-only
	// matches — bm25 alone shuffles as the corpus grows (e.g. indexing test
	// files), and a query for "Create" must find the node named Create before
	// CreateClient.
	ftsQuery := query + "*"
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.id, n.type, n.label, n.service, n.file, n.line, n.language, n.meta
		FROM nodes n
		JOIN nodes_fts f ON f.id = n.id
		WHERE nodes_fts MATCH ?
		ORDER BY (lower(n.label) = lower(?)) DESC, rank
		LIMIT ?`, ftsQuery, query, limit)
	if err != nil {
		return nil, fmt.Errorf("search nodes: %w", err)
	}
	defer rows.Close()
	return scanNodes(rows)
}

func (s *SQLiteStore) ListEdgesFrom(ctx context.Context, nodeID string) ([]*Edge, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, "from", "to", type, label, meta, confidence, method, path, sources_json, verification_state, verified_granularity FROM edges WHERE "from" = ?`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("list edges from %s: %w", nodeID, err)
	}
	defer rows.Close()
	return scanEdges(rows)
}

func (s *SQLiteStore) ListEdgesTo(ctx context.Context, nodeID string) ([]*Edge, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, "from", "to", type, label, meta, confidence, method, path, sources_json, verification_state, verified_granularity FROM edges WHERE "to" = ?`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("list edges to %s: %w", nodeID, err)
	}
	defer rows.Close()
	return scanEdges(rows)
}

func (s *SQLiteStore) BuildIndex(ctx context.Context) (*AdjacencyIndex, error) {
	idx := NewAdjacencyIndex()

	nodeRows, err := s.db.QueryContext(ctx,
		`SELECT id, type, label, service, file, line, language, meta FROM nodes`)
	if err != nil {
		return nil, fmt.Errorf("load nodes: %w", err)
	}
	defer nodeRows.Close()
	nodes, err := scanNodes(nodeRows)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		idx.AddNode(n)
	}

	edgeRows, err := s.db.QueryContext(ctx,
		`SELECT id, "from", "to", type, label, meta, confidence, method, path, sources_json, verification_state, verified_granularity FROM edges`)
	if err != nil {
		return nil, fmt.Errorf("load edges: %w", err)
	}
	defer edgeRows.Close()
	edges, err := scanEdges(edgeRows)
	if err != nil {
		return nil, err
	}
	for _, e := range edges {
		idx.AddEdge(e)
	}

	return idx, nil
}

// DeleteNodes removes nodes and any edges referencing them by ID.
func (s *SQLiteStore) DeleteNodes(ctx context.Context, ids map[string]bool) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM edges WHERE "from"=? OR "to"=?`, id, id); err != nil {
			return fmt.Errorf("delete edges for node %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM nodes WHERE id=?`, id); err != nil {
			return fmt.Errorf("delete node %s: %w", id, err)
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) Stats(ctx context.Context) (int, int, error) {
	var nodeCount, edgeCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM nodes`).Scan(&nodeCount); err != nil {
		return 0, 0, fmt.Errorf("count nodes: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM edges`).Scan(&edgeCount); err != nil {
		return 0, 0, fmt.Errorf("count edges: %w", err)
	}
	return nodeCount, edgeCount, nil
}

func (s *SQLiteStore) UpsertParseError(ctx context.Context, pe *ParseError) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO parse_errors (file_path, service, error_count, first_error_line, indexed_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(file_path) DO UPDATE SET
			service=excluded.service,
			error_count=excluded.error_count,
			first_error_line=excluded.first_error_line,
			indexed_at=excluded.indexed_at`,
		pe.FilePath, pe.Service, pe.ErrorCount, pe.FirstErrorLine, pe.IndexedAt)
	if err != nil {
		return fmt.Errorf("upsert parse error %s: %w", pe.FilePath, err)
	}
	return nil
}

func (s *SQLiteStore) ListParseErrors(ctx context.Context) ([]*ParseError, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT file_path, service, error_count, first_error_line, indexed_at FROM parse_errors ORDER BY file_path`)
	if err != nil {
		return nil, fmt.Errorf("list parse errors: %w", err)
	}
	defer rows.Close()

	var out []*ParseError
	for rows.Next() {
		var pe ParseError
		if err := rows.Scan(&pe.FilePath, &pe.Service, &pe.ErrorCount, &pe.FirstErrorLine, &pe.IndexedAt); err != nil {
			return nil, fmt.Errorf("scan parse error row: %w", err)
		}
		out = append(out, &pe)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpsertFileHash(ctx context.Context, fh *FileHash) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO file_hashes (file_path, service, content_hash, indexed_at, nodes_json, edges_json, unresolved_json, errored)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(file_path) DO UPDATE SET
			service=excluded.service, content_hash=excluded.content_hash,
			indexed_at=excluded.indexed_at, nodes_json=excluded.nodes_json,
			edges_json=excluded.edges_json, unresolved_json=excluded.unresolved_json,
			errored=excluded.errored`,
		fh.FilePath, fh.Service, fh.ContentHash, fh.IndexedAt, fh.NodesJSON, fh.EdgesJSON, orEmptyList(fh.UnresolvedJSON), boolToInt(fh.Errored))
	if err != nil {
		return fmt.Errorf("upsert file hash %s: %w", fh.FilePath, err)
	}
	return nil
}

// UpsertFileHashes writes a batch of file-hash records in one transaction.
// The per-row autocommit variant costs one fsync per file, which dominates
// re-index time on large workspaces.
func (s *SQLiteStore) UpsertFileHashes(ctx context.Context, fhs []*FileHash) error {
	if len(fhs) == 0 {
		return nil
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO file_hashes (file_path, service, content_hash, indexed_at, nodes_json, edges_json, unresolved_json, errored)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(file_path) DO UPDATE SET
				service=excluded.service, content_hash=excluded.content_hash,
				indexed_at=excluded.indexed_at, nodes_json=excluded.nodes_json,
				edges_json=excluded.edges_json, unresolved_json=excluded.unresolved_json,
				errored=excluded.errored`)
		if err != nil {
			return fmt.Errorf("prepare file-hash upsert: %w", err)
		}
		defer stmt.Close()
		for _, fh := range fhs {
			if _, err := stmt.ExecContext(ctx,
				fh.FilePath, fh.Service, fh.ContentHash, fh.IndexedAt, fh.NodesJSON, fh.EdgesJSON, orEmptyList(fh.UnresolvedJSON), boolToInt(fh.Errored)); err != nil {
				return fmt.Errorf("upsert file hash %s: %w", fh.FilePath, err)
			}
		}
		return nil
	})
}

func (s *SQLiteStore) ListFileHashes(ctx context.Context) (map[string]*FileHash, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT file_path, service, content_hash, indexed_at, nodes_json, edges_json, unresolved_json, errored FROM file_hashes`)
	if err != nil {
		return nil, fmt.Errorf("list file hashes: %w", err)
	}
	defer rows.Close()

	out := make(map[string]*FileHash)
	for rows.Next() {
		var fh FileHash
		var errored int
		if err := rows.Scan(&fh.FilePath, &fh.Service, &fh.ContentHash, &fh.IndexedAt, &fh.NodesJSON, &fh.EdgesJSON, &fh.UnresolvedJSON, &errored); err != nil {
			return nil, fmt.Errorf("scan file hash row: %w", err)
		}
		fh.Errored = errored != 0
		out[fh.FilePath] = &fh
	}
	return out, rows.Err()
}

// UpsertUnresolvedRefs records references that could not be resolved to
// nodes. The primary key dedupes re-submissions across index runs.
func (s *SQLiteStore) UpsertUnresolvedRefs(ctx context.Context, refs []UnresolvedRef) error {
	if len(refs) == 0 {
		return nil
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO unresolved_refs (service, file, line, name, kind)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(service, file, line, name, kind) DO NOTHING`)
		if err != nil {
			return fmt.Errorf("prepare unresolved-ref upsert: %w", err)
		}
		defer stmt.Close()
		for _, r := range refs {
			if _, err := stmt.ExecContext(ctx, r.Service, r.File, r.Line, r.Name, r.Kind); err != nil {
				return fmt.Errorf("upsert unresolved ref %s:%d %s: %w", r.File, r.Line, r.Name, err)
			}
		}
		return nil
	})
}

// ListUnresolvedRefs returns the graph's blind-spot ledger, ordered for
// stable reporting.
func (s *SQLiteStore) ListUnresolvedRefs(ctx context.Context) ([]UnresolvedRef, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT service, file, line, name, kind FROM unresolved_refs ORDER BY service, file, line, name`)
	if err != nil {
		return nil, fmt.Errorf("list unresolved refs: %w", err)
	}
	defer rows.Close()

	var out []UnresolvedRef
	for rows.Next() {
		var r UnresolvedRef
		if err := rows.Scan(&r.Service, &r.File, &r.Line, &r.Name, &r.Kind); err != nil {
			return nil, fmt.Errorf("scan unresolved ref: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// orEmptyList normalizes an empty cached-JSON string to a valid empty array.
func orEmptyList(s string) string {
	if s == "" {
		return "[]"
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *SQLiteStore) UpsertSemanticCache(ctx context.Context, service, fingerprint, nodesJSON, edgesJSON, referencedJSON string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO semantic_cache (service, fingerprint, nodes_json, edges_json, referenced_json)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(service) DO UPDATE SET
			fingerprint=excluded.fingerprint, nodes_json=excluded.nodes_json,
			edges_json=excluded.edges_json, referenced_json=excluded.referenced_json`,
		service, fingerprint, nodesJSON, edgesJSON, referencedJSON)
	if err != nil {
		return fmt.Errorf("upsert semantic cache %s: %w", service, err)
	}
	return nil
}

func (s *SQLiteStore) GetSemanticCache(ctx context.Context, service string) (fingerprint, nodesJSON, edgesJSON, referencedJSON string, err error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT fingerprint, nodes_json, edges_json, referenced_json FROM semantic_cache WHERE service = ?`, service)
	if err := row.Scan(&fingerprint, &nodesJSON, &edgesJSON, &referencedJSON); err != nil {
		if err == sql.ErrNoRows {
			return "", "", "", "", nil
		}
		return "", "", "", "", fmt.Errorf("get semantic cache %s: %w", service, err)
	}
	return fingerprint, nodesJSON, edgesJSON, referencedJSON, nil
}

func (s *SQLiteStore) UpsertDependency(ctx context.Context, d *Dependency) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO dependencies (service, ecosystem, name, version, kind)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(service, ecosystem, name) DO UPDATE SET
			version=excluded.version,
			kind=excluded.kind`,
		d.Service, d.Ecosystem, d.Name, d.Version, d.Kind)
	if err != nil {
		return fmt.Errorf("upsert dependency %s/%s: %w", d.Service, d.Name, err)
	}
	return nil
}

func (s *SQLiteStore) ListDependencies(ctx context.Context, service string) ([]*Dependency, error) {
	query := `SELECT service, ecosystem, name, version, kind FROM dependencies`
	args := []any{}
	if service != "" {
		query += ` WHERE service = ?`
		args = append(args, service)
	}
	query += ` ORDER BY service, ecosystem, name`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list dependencies: %w", err)
	}
	defer rows.Close()

	var out []*Dependency
	for rows.Next() {
		var d Dependency
		if err := rows.Scan(&d.Service, &d.Ecosystem, &d.Name, &d.Version, &d.Kind); err != nil {
			return nil, fmt.Errorf("scan dependency row: %w", err)
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		key, value)
	if err != nil {
		return fmt.Errorf("set meta %s: %w", key, err)
	}
	return nil
}

func (s *SQLiteStore) GetMeta(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("meta key not found: %s", key)
	}
	if err != nil {
		return "", fmt.Errorf("get meta %s: %w", key, err)
	}
	return value, nil
}

func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// WithTx executes fn inside a single transaction, rolling back on error.
func (s *SQLiteStore) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// --- scanning helpers ---

type rowScanner interface {
	Scan(dest ...any) error
}

func scanNode(row rowScanner) (*Node, error) {
	var n Node
	var typ, metaJSON string
	err := row.Scan(&n.ID, &typ, &n.Label, &n.Service, &n.File, &n.Line, &n.Language, &metaJSON)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("node not found")
	}
	if err != nil {
		return nil, fmt.Errorf("scan node: %w", err)
	}
	n.Type = NodeType(typ)
	n.Meta, err = unmarshalMeta(metaJSON)
	if err != nil {
		return nil, fmt.Errorf("unmarshal node meta: %w", err)
	}
	return &n, nil
}

func scanNodes(rows *sql.Rows) ([]*Node, error) {
	var out []*Node
	for rows.Next() {
		var n Node
		var typ, metaJSON string
		if err := rows.Scan(&n.ID, &typ, &n.Label, &n.Service, &n.File, &n.Line, &n.Language, &metaJSON); err != nil {
			return nil, fmt.Errorf("scan node row: %w", err)
		}
		n.Type = NodeType(typ)
		var err error
		n.Meta, err = unmarshalMeta(metaJSON)
		if err != nil {
			return nil, fmt.Errorf("unmarshal node meta: %w", err)
		}
		out = append(out, &n)
	}
	return out, rows.Err()
}

func scanEdge(row rowScanner) (*Edge, error) {
	var e Edge
	var typ, metaJSON, sourcesJSON string
	err := row.Scan(&e.ID, &e.From, &e.To, &typ, &e.Label, &metaJSON, &e.Confidence, &e.Method, &e.Path, &sourcesJSON, &e.VerificationState, &e.VerifiedGranularity)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("edge not found")
	}
	if err != nil {
		return nil, fmt.Errorf("scan edge: %w", err)
	}
	e.Type = EdgeType(typ)
	e.Meta, err = unmarshalMeta(metaJSON)
	if err != nil {
		return nil, fmt.Errorf("unmarshal edge meta: %w", err)
	}
	e.Sources, err = unmarshalSources(sourcesJSON)
	if err != nil {
		return nil, fmt.Errorf("unmarshal edge sources: %w", err)
	}
	return &e, nil
}

func scanEdges(rows *sql.Rows) ([]*Edge, error) {
	var out []*Edge
	for rows.Next() {
		var e Edge
		var typ, metaJSON, sourcesJSON string
		if err := rows.Scan(&e.ID, &e.From, &e.To, &typ, &e.Label, &metaJSON, &e.Confidence, &e.Method, &e.Path, &sourcesJSON, &e.VerificationState, &e.VerifiedGranularity); err != nil {
			return nil, fmt.Errorf("scan edge row: %w", err)
		}
		e.Type = EdgeType(typ)
		var err error
		e.Meta, err = unmarshalMeta(metaJSON)
		if err != nil {
			return nil, fmt.Errorf("unmarshal edge meta: %w", err)
		}
		e.Sources, err = unmarshalSources(sourcesJSON)
		if err != nil {
			return nil, fmt.Errorf("unmarshal edge sources: %w", err)
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// --- JSON helpers ---

func marshalSources(s []SourceRef) (string, error) {
	if len(s) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalSources(s string) ([]SourceRef, error) {
	if s == "" || s == "[]" {
		return nil, nil
	}
	var refs []SourceRef
	if err := json.Unmarshal([]byte(s), &refs); err != nil {
		return nil, err
	}
	return refs, nil
}

func marshalMeta(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalMeta(s string) (map[string]string, error) {
	if s == "" || s == "{}" {
		return nil, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}
