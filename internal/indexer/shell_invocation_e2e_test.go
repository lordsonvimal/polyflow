package indexer

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

// TestRun_ShellInvocation_ImpactCallerChain is SH1's acceptance test: a
// service where deploy.sh sources lib.sh and runs `bash migrate.sh` — after
// a full index run, migrate.sh's (script) node has an incoming `calls` edge
// (via=exec) from deploy.sh's (script) node, i.e. "what calls migrate.sh"
// (the impact/blast-radius question) resolves to deploy.sh with no changes
// to internal/contract/engine.go — this pass is entirely additive (a shell
// parser + a linker pass).
func TestRun_ShellInvocation_ImpactCallerChain(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "ops")
	require.NoError(t, os.MkdirAll(svcDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(svcDir, "lib.sh"), []byte(
		"log() {\n  echo \"[log] $1\"\n}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(svcDir, "migrate.sh"), []byte(
		"migrate_up() {\n  echo migrating\n}\n\nmigrate_up\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(svcDir, "deploy.sh"), []byte(
		"source lib.sh\nbash migrate.sh\n"), 0o644))

	cfg := &workspace.WorkspaceConfig{
		Name: "shelltest", Version: "1",
		Services: []workspace.Service{{Name: "ops", Path: svcDir, Language: "bash"}},
	}
	dbDir := t.TempDir()
	runIndexer(t, cfg, dbDir, true)

	st, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer st.Close()
	ctx := context.Background()

	fnNodes, err := st.ListNodesByType(ctx, "function", "ops", 1000)
	require.NoError(t, err)

	var migrateScriptID, libScriptID, deployScriptID string
	for _, n := range fnNodes {
		if n.Label != "(script)" {
			continue
		}
		switch filepath.Base(n.File) {
		case "migrate.sh":
			migrateScriptID = n.ID
		case "lib.sh":
			libScriptID = n.ID
		case "deploy.sh":
			deployScriptID = n.ID
		}
	}
	require.NotEmpty(t, migrateScriptID, "migrate.sh (script) node missing")
	require.NotEmpty(t, libScriptID, "lib.sh (script) node missing")
	require.NotEmpty(t, deployScriptID, "deploy.sh (script) node missing")

	migrateCallers, err := st.ListEdgesTo(ctx, migrateScriptID)
	require.NoError(t, err)
	var foundMigrate bool
	for _, e := range migrateCallers {
		if e.From == deployScriptID && e.Type == graph.EdgeTypeCalls && e.Meta["via"] == "exec" {
			foundMigrate = true
		}
	}
	assert.True(t, foundMigrate, "expected deploy.sh -> migrate.sh calls edge (via=exec)")

	libCallers, err := st.ListEdgesTo(ctx, libScriptID)
	require.NoError(t, err)
	var foundLib bool
	for _, e := range libCallers {
		if e.From == deployScriptID && e.Type == graph.EdgeTypeCalls && e.Meta["via"] == "exec" {
			foundLib = true
		}
	}
	assert.True(t, foundLib, "expected deploy.sh -> lib.sh calls edge (via=exec)")

	// migrate.sh and lib.sh are each invoked by deploy.sh — not roots.
	// deploy.sh itself has no inbound caller — it IS the entrypoint.
	migrateNode, err := st.GetNode(ctx, migrateScriptID)
	require.NoError(t, err)
	assert.NotEqual(t, "entrypoint", migrateNode.Meta["root_kind"], "migrate.sh is invoked by deploy.sh, not a root")

	deployNode, err := st.GetNode(ctx, deployScriptID)
	require.NoError(t, err)
	assert.Equal(t, "entrypoint", deployNode.Meta["root_kind"], "deploy.sh has no inbound exec caller — must classify as entrypoint")
}
