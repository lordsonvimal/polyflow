package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// TestPerServiceLastIndexed_NoServiceDBs is FR.6's default case: a workspace
// that has only ever run a full `polyflow index` has no `services/<name>/`
// directories, so the per-service section must be omitted entirely rather
// than printing an empty or all-"never" block.
func TestPerServiceLastIndexed_NoServiceDBs(t *testing.T) {
	dir := t.TempDir()
	dbDir := filepath.Join(dir, ".polyflow")

	cfg := &workspace.WorkspaceConfig{
		Name: "fleet",
		Services: []workspace.Service{
			{Name: "api", Path: dir, Language: "go"},
		},
	}
	assert.Empty(t, perServiceLastIndexed(cfg, dbDir))
}

// TestPerServiceLastIndexed_ReadsEachServiceDB proves the line is sourced
// from each service's own `services/<name>/graph.db` meta table (FR.2),
// independent of the merged fleet DB.
func TestPerServiceLastIndexed_ReadsEachServiceDB(t *testing.T) {
	dir := t.TempDir()
	dbDir := filepath.Join(dir, ".polyflow")

	apiDBDir := filepath.Join(dbDir, "services", "api")
	require.NoError(t, os.MkdirAll(apiDBDir, 0o755))
	store, err := graph.NewSQLiteStore(filepath.Join(apiDBDir, meta.DBFile))
	require.NoError(t, err)
	require.NoError(t, store.SetMeta(context.Background(), "last_indexed", "1700000000"))
	require.NoError(t, store.Close())

	cfg := &workspace.WorkspaceConfig{
		Name: "fleet",
		Services: []workspace.Service{
			{Name: "api", Path: dir, Language: "go"},
			{Name: "web", Path: dir, Language: "javascript"},
		},
	}

	lines := perServiceLastIndexed(cfg, dbDir)
	require.Len(t, lines, 1, "only api has a per-service DB on disk")
	assert.Contains(t, lines[0], "api:")
}
