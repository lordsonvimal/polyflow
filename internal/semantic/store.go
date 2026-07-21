package semantic

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Store handles reads and writes for the embeddings and entities_fts tables.
// It wraps a *sql.DB directly (the caller supplies the connection from
// graph.SQLiteStore.DB()) so the semantic package has no compile-time
// dependency on the graph package.
type Store struct {
	db *sql.DB
}

// NewStore creates a Store wrapping the supplied database connection.
// The caller retains ownership of the connection; Close is the caller's
// responsibility.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// ListEmbeddingMeta returns lightweight metadata for every stored embedding
// (entity_id, embedder_id, content_hash) without loading vector bytes.
// Used by the indexer to compute the re-embed delta set.
func (s *Store) ListEmbeddingMeta(ctx context.Context) ([]EmbeddingMeta, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_id, embedder_id, content_hash FROM embeddings ORDER BY entity_id`)
	if err != nil {
		return nil, fmt.Errorf("list embedding meta: %w", err)
	}
	defer rows.Close()
	var out []EmbeddingMeta
	for rows.Next() {
		var m EmbeddingMeta
		if err := rows.Scan(&m.EntityID, &m.EmbedderID, &m.ContentHash); err != nil {
			return nil, fmt.Errorf("scan embedding meta: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// entityAnchors is the JSON-encoded anchor metadata stored in the embeddings.meta column.
type entityAnchors struct {
	NodeID  string   `json:"node_id,omitempty"`
	Members []string `json:"members,omitempty"`
	File    string   `json:"file,omitempty"`
	Line    int      `json:"line,omitempty"`
}

// BatchUpsertEmbeddings writes a batch of embeddings in one transaction.
// The vector is stored as little-endian float32 bytes (BLOB). Entity anchors
// (NodeID, Members, File, Line) are serialised into the meta JSON column.
func (s *Store) BatchUpsertEmbeddings(ctx context.Context, entities []Entity, vecs [][]float32, embedderID string) error {
	if len(entities) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin embed tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO embeddings (entity_id, entity_type, content_hash, embedder_id, dims, vector, meta)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(entity_id) DO UPDATE SET
			entity_type=excluded.entity_type,
			content_hash=excluded.content_hash,
			embedder_id=excluded.embedder_id,
			dims=excluded.dims,
			vector=excluded.vector,
			meta=excluded.meta`)
	if err != nil {
		return fmt.Errorf("prepare embed upsert: %w", err)
	}
	defer stmt.Close()

	for i, ent := range entities {
		vec := vecs[i]
		blob := vecToBlob(vec)
		metaJSON, _ := json.Marshal(entityAnchors{
			NodeID:  ent.NodeID,
			Members: ent.Members,
			File:    ent.File,
			Line:    ent.Line,
		})
		if _, err := stmt.ExecContext(ctx,
			ent.ID, ent.Type, ent.ContentHash, embedderID, len(vec), blob, string(metaJSON)); err != nil {
			return fmt.Errorf("upsert embedding %s: %w", ent.ID, err)
		}
		// entities_fts is the lexical twin — upsert text alongside the vector.
		// FTS5 virtual tables do not support ON CONFLICT; delete + re-insert.
		if _, err := tx.ExecContext(ctx, `DELETE FROM entities_fts WHERE entity_id = ?`, ent.ID); err != nil {
			return fmt.Errorf("fts delete %s: %w", ent.ID, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO entities_fts (entity_id, entity_type, text) VALUES (?, ?, ?)`,
			ent.ID, ent.Type, ent.Text); err != nil {
			return fmt.Errorf("fts insert %s: %w", ent.ID, err)
		}
	}
	return tx.Commit()
}

// ftsHit is one result from FTSSearch.
type ftsHit struct {
	EntityID   string
	EntityType string
	Rank       int    // 1-based (1 = best match)
	Label      string // node label for exact-match detection; "" for flows/docs
}

// FTSSearch runs the tokenised ftsQuery against entities_fts and returns up
// to limit ranked hits. For node entities, the node label is also loaded from
// the nodes table so the caller can detect exact-match hits.
// ftsQuery must already be FTS5-safe (see buildFTS5Query in search.go).
func (s *Store) FTSSearch(ctx context.Context, ftsQuery string, limit int) ([]ftsHit, error) {
	if ftsQuery == "" {
		return nil, nil
	}
	// Wrap the FTS match in a subquery so the LEFT JOIN with nodes is safe
	// across all SQLite versions that support FTS5 JOIN semantics.
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.entity_id, f.entity_type, COALESCE(n.label,'') AS label
		FROM (
			SELECT entity_id, entity_type
			FROM entities_fts
			WHERE entities_fts MATCH ?
			ORDER BY rank
			LIMIT ?
		) f
		LEFT JOIN nodes n ON n.id = f.entity_id AND f.entity_type = 'node'`,
		ftsQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("fts search: %w", err)
	}
	defer rows.Close()
	var hits []ftsHit
	for rows.Next() {
		var h ftsHit
		if err := rows.Scan(&h.EntityID, &h.EntityType, &h.Label); err != nil {
			return nil, fmt.Errorf("scan fts hit: %w", err)
		}
		h.Rank = len(hits) + 1 // 1-based rank preserving FTS5 BM25 order
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// LoadEntitiesByIDs loads entity metadata (anchors) for the given ids from the
// embeddings table.  Node entities are additionally enriched with authoritative
// file/line from the nodes table, which handles pre-S.2 rows whose meta is '{}'.
func (s *Store) LoadEntitiesByIDs(ctx context.Context, ids []string) (map[string]Entity, error) {
	if len(ids) == 0 {
		return map[string]Entity{}, nil
	}
	ph := strings.Repeat("?,", len(ids))
	ph = ph[:len(ph)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_id, entity_type, meta FROM embeddings WHERE entity_id IN (`+ph+`)`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("load entity meta: %w", err)
	}
	defer rows.Close()

	result := make(map[string]Entity, len(ids))
	var nodeIDs []string
	for rows.Next() {
		var id, etype, metaStr string
		if err := rows.Scan(&id, &etype, &metaStr); err != nil {
			return nil, fmt.Errorf("scan entity meta: %w", err)
		}
		var a entityAnchors
		_ = json.Unmarshal([]byte(metaStr), &a)
		result[id] = Entity{
			ID:      id,
			Type:    etype,
			NodeID:  a.NodeID,
			Members: a.Members,
			File:    a.File,
			Line:    a.Line,
		}
		if etype == "node" {
			nodeIDs = append(nodeIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Enrich node entities from the authoritative nodes table (file, line,
	// node_id). This handles old embeddings whose meta column is '{}'.
	if len(nodeIDs) > 0 {
		nph := strings.Repeat("?,", len(nodeIDs))
		nph = nph[:len(nph)-1]
		nargs := make([]any, len(nodeIDs))
		for i, id := range nodeIDs {
			nargs[i] = id
		}
		nrows, nerr := s.db.QueryContext(ctx,
			`SELECT id, file, line FROM nodes WHERE id IN (`+nph+`)`, nargs...)
		if nerr != nil {
			return nil, fmt.Errorf("enrich node meta: %w", nerr)
		}
		defer nrows.Close()
		for nrows.Next() {
			var id, file string
			var line int
			if err := nrows.Scan(&id, &file, &line); err != nil {
				return nil, fmt.Errorf("scan node row: %w", err)
			}
			if ent, ok := result[id]; ok {
				ent.File = file
				ent.Line = line
				ent.NodeID = id
				result[id] = ent
			}
		}
		if err := nrows.Err(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// GetEmbedStatus reads the "embed_status" key from the graph meta table.
// Returns "" if the key is absent (first run before any index).
func (s *Store) GetEmbedStatus(ctx context.Context) string {
	var v string
	_ = s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'embed_status'`).Scan(&v)
	return v
}

// CheckEmbedderConsistency reports whether all stored embeddings share one
// embedder ID (a requirement for valid cosine search — vectors from different
// model spaces cannot be compared).
//
// Returns ("", nil) when the table is empty.
// Returns (id, nil) when all rows share the same embedder ID.
// Returns ("", error) when two or more distinct IDs are found; the error message
// names the fix: run `polyflow index` to re-embed with a single embedder.
func (s *Store) CheckEmbedderConsistency(ctx context.Context) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT embedder_id FROM embeddings LIMIT 2`)
	if err != nil {
		return "", fmt.Errorf("check embedder consistency: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", fmt.Errorf("check embedder consistency: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("check embedder consistency: %w", err)
	}
	if len(ids) > 1 {
		return "", fmt.Errorf(
			"embeddings table contains vectors from multiple embedders %v — "+
				"vector spaces are incompatible; run `polyflow index` to re-embed with a single embedder",
			ids)
	}
	if len(ids) == 1 {
		return ids[0], nil
	}
	return "", nil
}

// LoadVectors loads all stored vectors for in-memory cosine search.
// Returns entity ids (ordered), entity types, and the flat float32 matrix
// (n × dims, row-major).  S.2 calls this at first-search time.
func (s *Store) LoadVectors(ctx context.Context) (ids, types []string, mat []float32, dims int, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_id, entity_type, dims, vector FROM embeddings ORDER BY entity_id`)
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("load vectors: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var eid, etype string
		var d int
		var blob []byte
		if err := rows.Scan(&eid, &etype, &d, &blob); err != nil {
			return nil, nil, nil, 0, fmt.Errorf("scan vector row: %w", err)
		}
		if dims == 0 {
			dims = d
		}
		ids = append(ids, eid)
		types = append(types, etype)
		vec, berr := blobToVec(blob, d)
		if berr != nil {
			return nil, nil, nil, 0, fmt.Errorf("decode vector %s: %w", eid, berr)
		}
		mat = append(mat, vec...)
	}
	return ids, types, mat, dims, rows.Err()
}

// --- vector encoding ---

// vecToBlob encodes a float32 slice as little-endian bytes.
func vecToBlob(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// blobToVec decodes a little-endian blob back to float32.
func blobToVec(b []byte, dims int) ([]float32, error) {
	if len(b) != dims*4 {
		return nil, fmt.Errorf("expected %d bytes for %d dims, got %d", dims*4, dims, len(b))
	}
	v := make([]float32, dims)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4 : i*4+4]))
	}
	return v, nil
}
