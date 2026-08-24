package queryresolve_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/fleetsync"
	"github.com/lordsonvimal/polyflow/internal/queryresolve"
	"github.com/lordsonvimal/polyflow/internal/registry"
)

// setupWorkspace creates <dir>/.polyflow/graph.db (an empty file — Resolve
// only ever os.Stats it, never opens it) and returns dir.
func setupWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".polyflow"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".polyflow", "graph.db"), nil, 0o644))
	return dir
}

func TestResolve_NoLocalDB(t *testing.T) {
	res, err := queryresolve.Resolve(context.Background(), t.TempDir(), queryresolve.Options{})
	require.NoError(t, err)
	assert.Empty(t, res.LocalDBPath)
	assert.Empty(t, res.FleetName)
	assert.Empty(t, res.BridgePath)
}

func TestResolve_LocalDBNoFleetMembership(t *testing.T) {
	ws := setupWorkspace(t)
	regPath := filepath.Join(t.TempDir(), "registry.yml")
	require.NoError(t, registry.Save(regPath, &registry.Registry{Version: "1"}))

	res, err := queryresolve.Resolve(context.Background(), ws, queryresolve.Options{RegistryPath: regPath})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(ws, ".polyflow", "graph.db"), res.LocalDBPath)
	assert.Empty(t, res.FleetName)
	assert.Empty(t, res.BridgePath)
}

func TestResolve_SingleFleet_BridgeAlreadyFreshNoSyncTriggered(t *testing.T) {
	ws := setupWorkspace(t)
	regPath := filepath.Join(t.TempDir(), "registry.yml")
	require.NoError(t, registry.Save(regPath, &registry.Registry{
		Version: "1",
		Entries: []registry.Entry{{Service: "svc", LocalPath: ws, Fleets: []string{"myfleet"}}},
	}))

	home := t.TempDir()
	t.Setenv("POLYFLOW_HOME", home)
	bridgePath, err := fleetsync.DefaultBridgePath("myfleet")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(bridgePath), 0o755))
	require.NoError(t, os.WriteFile(bridgePath, []byte("bridge"), 0o644))
	// Bridge must be newer than the local DB to count as fresh.
	future := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(bridgePath, future, future))

	res, err := queryresolve.Resolve(context.Background(), ws, queryresolve.Options{
		RegistryPath: regPath,
		// FleetConfigPaths is empty in the registry above, so a sync
		// attempt (if wrongly triggered) would be a silent no-op anyway —
		// this test's real assertion is that the existing fresh bridge is
		// reported as-is.
	})
	require.NoError(t, err)
	assert.Equal(t, "myfleet", res.FleetName)
	assert.Equal(t, bridgePath, res.BridgePath)
}

func TestResolve_AmbiguousFleet_RequiresFleetOption(t *testing.T) {
	ws := setupWorkspace(t)
	regPath := filepath.Join(t.TempDir(), "registry.yml")
	require.NoError(t, registry.Save(regPath, &registry.Registry{
		Version: "1",
		Entries: []registry.Entry{{Service: "svc", LocalPath: ws, Fleets: []string{"fleet-a", "fleet-b"}}},
	}))
	t.Setenv("POLYFLOW_HOME", t.TempDir())

	_, err := queryresolve.Resolve(context.Background(), ws, queryresolve.Options{RegistryPath: regPath})
	require.Error(t, err)
	var ambigErr *queryresolve.ErrAmbiguousFleet
	require.ErrorAs(t, err, &ambigErr)
	assert.ElementsMatch(t, []string{"fleet-a", "fleet-b"}, ambigErr.Candidates)

	res, err := queryresolve.Resolve(context.Background(), ws, queryresolve.Options{RegistryPath: regPath, Fleet: "fleet-b"})
	require.NoError(t, err)
	assert.Equal(t, "fleet-b", res.FleetName)
}

func TestResolve_MissingBridgeNoFleetConfigPath_ReportsEmptyBridgeWithoutError(t *testing.T) {
	ws := setupWorkspace(t)
	regPath := filepath.Join(t.TempDir(), "registry.yml")
	require.NoError(t, registry.Save(regPath, &registry.Registry{
		Version: "1",
		Entries: []registry.Entry{{Service: "svc", LocalPath: ws, Fleets: []string{"myfleet"}}},
	}))
	t.Setenv("POLYFLOW_HOME", t.TempDir())

	res, err := queryresolve.Resolve(context.Background(), ws, queryresolve.Options{RegistryPath: regPath})
	require.NoError(t, err)
	assert.Equal(t, "myfleet", res.FleetName)
	assert.Empty(t, res.BridgePath, "no known fleet definition path — best-effort sync must not error, just skip")
}

func TestResolve_NoSyncOption_NeverTriesToBuildEvenWhenStale(t *testing.T) {
	ws := setupWorkspace(t)
	regPath := filepath.Join(t.TempDir(), "registry.yml")
	require.NoError(t, registry.Save(regPath, &registry.Registry{
		Version: "1",
		Entries: []registry.Entry{{Service: "svc", LocalPath: ws, Fleets: []string{"myfleet"}}},
	}))
	t.Setenv("POLYFLOW_HOME", t.TempDir())

	res, err := queryresolve.Resolve(context.Background(), ws, queryresolve.Options{RegistryPath: regPath, NoSync: true})
	require.NoError(t, err)
	assert.Empty(t, res.BridgePath, "bridge does not exist on disk and NoSync must not build it")
}
