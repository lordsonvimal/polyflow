package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/ops"
)

// runCLI executes rootCmd end-to-end (the real cobra Execute() path, so
// PersistentPreRunE/opsFinalize actually run) with the given args, in the
// current working directory, and returns the command error.
func runCLI(t *testing.T, args ...string) error {
	t.Helper()
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	opsFinalize(err)
	return err
}

func minimalWorkspace(t *testing.T) {
	t.Helper()
	require.NoError(t, os.WriteFile(meta.ConfigFile, []byte(
		"name: \"opslog-test\"\nversion: \"1\"\nservices:\n  - name: svc\n    path: .\n    language: go\n",
	), 0o644))
	// Normally `polyflow index` creates .polyflow/ before graph.db/ops.db are
	// opened; this fixture skips a real index run (heavier, not what this
	// test is about) and creates the dir directly.
	require.NoError(t, os.MkdirAll(meta.DBDir, 0o755))
}

func TestOpsLog_CLI_RecordsSuccessfulCall(t *testing.T) {
	t.Chdir(t.TempDir())
	minimalWorkspace(t)

	err := runCLI(t, "search", "nothing")
	require.NoError(t, err, "search on an empty (auto-created) graph.db should succeed with zero results")

	o, oerr := ops.Open(filepath.Join(meta.DBDir, meta.OpsFile))
	require.NoError(t, oerr)
	t.Cleanup(func() { o.Close() })

	list, lerr := o.ListCalls(t.Context(), ops.ListFilter{})
	require.NoError(t, lerr)
	require.Len(t, list.Calls, 1)

	call := list.Calls[0]
	assert.Equal(t, "cli", call.Source)
	assert.Equal(t, "search", call.Tool)
	assert.Equal(t, "ok", call.Status)
	assert.Contains(t, call.Params, "nothing", "positional args are recorded")
}

func TestOpsLog_CLI_RecordsErrorCall(t *testing.T) {
	t.Chdir(t.TempDir())
	minimalWorkspace(t)

	err := runCLI(t, "impact") // impact requires --target or --file
	require.Error(t, err)

	o, oerr := ops.Open(filepath.Join(meta.DBDir, meta.OpsFile))
	require.NoError(t, oerr)
	t.Cleanup(func() { o.Close() })

	list, lerr := o.ListCalls(t.Context(), ops.ListFilter{})
	require.NoError(t, lerr)
	require.Len(t, list.Calls, 1)
	assert.Equal(t, "error", list.Calls[0].Status)
	assert.NotEmpty(t, list.Calls[0].Error)
}

func TestOpsLog_CLI_SkipsOutsideWorkspace(t *testing.T) {
	t.Chdir(t.TempDir()) // no polyflow.yml here

	_ = runCLI(t, "search", "nothing") // error is fine either way; only recording matters

	_, statErr := os.Stat(filepath.Join(meta.DBDir, meta.OpsFile))
	assert.True(t, os.IsNotExist(statErr), "ops.db must not be created outside a workspace")
}

func TestOpsLog_CLI_CapturesResolvedFlagsAndStdout(t *testing.T) {
	t.Chdir(t.TempDir())
	minimalWorkspace(t)

	err := runCLI(t, "search", "nothing", "--limit", "5", "--format", "json")
	require.NoError(t, err)

	o, oerr := ops.Open(filepath.Join(meta.DBDir, meta.OpsFile))
	require.NoError(t, oerr)
	t.Cleanup(func() { o.Close() })

	list, lerr := o.ListCalls(t.Context(), ops.ListFilter{})
	require.NoError(t, lerr)
	require.Len(t, list.Calls, 1)
	call := list.Calls[0]

	assert.Contains(t, call.Params, `"limit":"5"`)
	assert.Contains(t, call.Params, `"format":"json"`)
	// --format json on zero results prints "null\n" (json.Encoder of a nil slice).
	assert.NotEmpty(t, call.Result)
}
