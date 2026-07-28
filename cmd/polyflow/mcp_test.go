package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// TestMCPGate covers the state-file toggle: the marker is absent by default
// (enabled), `off` creates it (disabled), `on` removes it, and both are
// idempotent.
func TestMCPGate(t *testing.T) {
	t.Chdir(t.TempDir())

	assert.True(t, mcpEnabled(), "no marker → enabled")

	require.NoError(t, mcpOffCmd.RunE(mcpOffCmd, nil))
	assert.False(t, mcpEnabled(), "after off → disabled")
	_, statErr := os.Stat(mcpMarkerPath())
	require.NoError(t, statErr, "marker file exists after off")

	// off is idempotent.
	require.NoError(t, mcpOffCmd.RunE(mcpOffCmd, nil))
	assert.False(t, mcpEnabled())

	require.NoError(t, mcpOnCmd.RunE(mcpOnCmd, nil))
	assert.True(t, mcpEnabled(), "after on → enabled")

	// on is idempotent (removing an absent marker is not an error).
	require.NoError(t, mcpOnCmd.RunE(mcpOnCmd, nil))
	assert.True(t, mcpEnabled())
}

// TestIndexFreshness verifies the STALE / up-to-date verdict is driven by
// source-file mtimes relative to the last index run.
func TestIndexFreshness(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	require.NoError(t, os.WriteFile(src, []byte("package main\n"), 0o644))

	cfg := &workspace.WorkspaceConfig{
		Services: []workspace.Service{{Name: "svc", Path: dir, Language: "go"}},
	}

	// Never indexed → no verdict line (the "Last indexed: never" line says it).
	assert.Equal(t, "", indexFreshness(cfg, time.Time{}))

	// File is newer than the last index → STALE with a count.
	past := time.Now().Add(-time.Hour)
	assert.Contains(t, indexFreshness(cfg, past), "STALE — 1 file(s) changed")

	// Last index is newer than every source file → up to date.
	future := time.Now().Add(time.Hour)
	assert.Equal(t, "up to date", indexFreshness(cfg, future))
}
