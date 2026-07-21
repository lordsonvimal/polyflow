package semantic

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestCheckEmbedderConsistency_Empty(t *testing.T) {
	db := openTestDB(t)
	s := NewStore(db)
	id, err := s.CheckEmbedderConsistency(context.Background())
	if err != nil {
		t.Fatalf("empty table: want no error, got %v", err)
	}
	if id != "" {
		t.Errorf("empty table: want empty id, got %q", id)
	}
}

func TestCheckEmbedderConsistency_SingleID(t *testing.T) {
	db := openTestDB(t)
	s := NewStore(db)

	insertEmbedderIDRow(t, db, "e1", "node", "static-v1-int8")
	insertEmbedderIDRow(t, db, "e2", "node", "static-v1-int8")

	id, err := s.CheckEmbedderConsistency(context.Background())
	if err != nil {
		t.Fatalf("single ID: want no error, got %v", err)
	}
	if id != "static-v1-int8" {
		t.Errorf("single ID: want %q, got %q", "static-v1-int8", id)
	}
}

// TestCheckEmbedderConsistency_MixedIDs is the space-mixing guard test:
// two different embedder IDs in the table → error naming the fix (polyflow index).
func TestCheckEmbedderConsistency_MixedIDs(t *testing.T) {
	db := openTestDB(t)
	s := NewStore(db)

	insertEmbedderIDRow(t, db, "e1", "node", "static-v1-int8")
	insertEmbedderIDRow(t, db, "e2", "node", "sidecar:nomic-embed-text-v1.5-q8")

	_, err := s.CheckEmbedderConsistency(context.Background())
	if err == nil {
		t.Fatal("mixed embedder IDs: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "polyflow index") {
		t.Errorf("error message does not name the fix (`polyflow index`): %q", err.Error())
	}
}

// insertEmbedderIDRow inserts a minimal row into the embeddings table with the
// specified embedder_id, so we can test consistency checks without going
// through the full embed pipeline.
func insertEmbedderIDRow(t *testing.T, db *sql.DB, entityID, entityType, embedderID string) {
	t.Helper()
	vec := make([]float32, 4)
	blob := vecToBlob(vec)
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO embeddings (entity_id, entity_type, content_hash, embedder_id, dims, vector, meta)
		VALUES (?, ?, 'testhash', ?, 4, ?, '{}')`,
		entityID, entityType, embedderID, blob)
	if err != nil {
		t.Fatalf("insert embedder row %s: %v", entityID, err)
	}
}
