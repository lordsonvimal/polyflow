package registry_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/registry"
)

func TestDefaultPath_HonorsPolyflowHome(t *testing.T) {
	t.Setenv("POLYFLOW_HOME", "/custom/home")
	path, err := registry.DefaultPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/custom/home", "registry.yml"), path)
}

func TestDefaultPath_FallsBackToUserHome(t *testing.T) {
	t.Setenv("POLYFLOW_HOME", "")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	path, err := registry.DefaultPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".polyflow", "registry.yml"), path)
}

func TestLoad_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	reg, err := registry.Load(filepath.Join(dir, "nope.yml"))
	require.NoError(t, err)
	assert.Equal(t, "1", reg.Version)
	assert.Empty(t, reg.Entries)
}

func TestSync_UpsertsNotDuplicates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.yml")

	require.NoError(t, registry.Sync(path, "willow", "/Users/dev/willow"))
	reg, err := registry.Load(path)
	require.NoError(t, err)
	require.Len(t, reg.Entries, 1)
	assert.Equal(t, "/Users/dev/willow", reg.Entries[0].LocalPath)
	firstIndexedAt := reg.Entries[0].IndexedAt

	// Repeat sync for the same service with a different path: upsert, not append.
	require.NoError(t, registry.Sync(path, "willow", "/Users/dev/willow-2"))
	reg, err = registry.Load(path)
	require.NoError(t, err)
	require.Len(t, reg.Entries, 1)
	assert.Equal(t, "/Users/dev/willow-2", reg.Entries[0].LocalPath)
	assert.True(t, !reg.Entries[0].IndexedAt.Before(firstIndexedAt))

	// A different service appends a second entry.
	require.NoError(t, registry.Sync(path, "maple-agent", "/Users/dev/juniper"))
	reg, err = registry.Load(path)
	require.NoError(t, err)
	assert.Len(t, reg.Entries, 2)
}

func TestLookup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.yml")
	require.NoError(t, registry.Sync(path, "willow", "/Users/dev/willow"))
	reg, err := registry.Load(path)
	require.NoError(t, err)

	e, ok := reg.Lookup("willow")
	require.True(t, ok)
	assert.Equal(t, "/Users/dev/willow", e.LocalPath)

	_, ok = reg.Lookup("nope")
	assert.False(t, ok)
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.yml")
	require.NoError(t, registry.Sync(path, "svc", "/path/to/svc"))

	got, err := registry.Load(path)
	require.NoError(t, err)
	require.Len(t, got.Entries, 1)
	assert.Equal(t, "svc", got.Entries[0].Service)
}
