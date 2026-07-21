package semantic

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
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

// BatchUpsertEmbeddings writes a batch of embeddings in one transaction.
// The vector is stored as little-endian float32 bytes (BLOB).
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
		VALUES (?, ?, ?, ?, ?, ?, '{}')
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

	ftsStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO entities_fts (entity_id, entity_type, text)
		VALUES (?, ?, ?)
		ON CONFLICT DO NOTHING`)
	if err != nil {
		return fmt.Errorf("prepare fts insert: %w", err)
	}
	defer ftsStmt.Close()

	for i, ent := range entities {
		vec := vecs[i]
		blob := vecToBlob(vec)
		if _, err := stmt.ExecContext(ctx,
			ent.ID, ent.Type, ent.ContentHash, embedderID, len(vec), blob); err != nil {
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
	_ = ftsStmt // closed via defer; suppress unused warning
	return tx.Commit()
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
