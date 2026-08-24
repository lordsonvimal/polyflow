package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/fleetsync"
	"github.com/lordsonvimal/polyflow/internal/registry"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// whatever it wrote — runFleetStatus/runRegistry print with fmt.Printf
// directly, no injectable writer.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	fn()
	require.NoError(t, w.Close())
	os.Stdout = orig
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

func fsRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// newBareServiceRepoWithCheckout creates a bare remote on "main" plus a
// separate, clean local clone of it. Returns the bare URL (usable as a
// fleetconfig.Service.Git), the local clone's dir, and the commit SHA.
func newBareServiceRepoWithCheckout(t *testing.T) (bareURL, localDir, sha string) {
	t.Helper()
	bareDir := t.TempDir()
	fsRunGit(t, "", "init", "--bare", bareDir)
	// Pin HEAD to "main" regardless of the runner's init.defaultBranch —
	// CI runners default to "master", which leaves clones in a detached
	// state once we push only "main".
	fsRunGit(t, bareDir, "symbolic-ref", "HEAD", "refs/heads/main")

	workDir := t.TempDir()
	fsRunGit(t, workDir, "init")
	fsRunGit(t, workDir, "checkout", "-b", "main")
	fsRunGit(t, workDir, "config", "user.email", "test@example.com")
	fsRunGit(t, workDir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	fsRunGit(t, workDir, "add", ".")
	fsRunGit(t, workDir, "commit", "-m", "init")
	fsRunGit(t, workDir, "remote", "add", "origin", bareDir)
	fsRunGit(t, workDir, "push", "origin", "main")
	sha = fsRunGit(t, workDir, "rev-parse", "HEAD")

	localDir = filepath.Join(t.TempDir(), "local")
	fsRunGit(t, "", "clone", bareDir, localDir)
	return bareDir, localDir, sha
}

// TestFleetStatus_ReportsLocalMemberAndBridgeAge is GR.5's acceptance test
// for `polyflow fleet status`: a workspace registered as a fleet member,
// with one sibling resolvable from a clean local checkout and a bridge.db
// already on disk, must report the resolved SHA, "local checkout matches",
// and a synced-N-ago bridge line — all without ResolveStatus ever cloning
// (proven separately by TestResolveStatus_CleanLocalMatch_ReportsLocalNoClone
// in internal/fleetsync).
func TestFleetStatus_ReportsLocalMemberAndBridgeAge(t *testing.T) {
	home := t.TempDir()
	t.Setenv("POLYFLOW_HOME", home)

	apiBare, apiLocal, apiSHA := newBareServiceRepoWithCheckout(t)

	fleetDir := t.TempDir()
	fleetYML := filepath.Join(fleetDir, "fleet.yml")
	require.NoError(t, os.WriteFile(fleetYML, []byte(`
name: myfleet
version: "1"
services:
  - name: api
    git: `+apiBare+`
    ref: main
    language: go
`), 0o644))

	// apiLocal is the "cd willow"-equivalent workspace: it needs its own
	// .polyflow/graph.db (queryresolve.FindLocalDB's upward walk) plus a
	// registry entry recording it as a clean checkout at apiSHA. A real
	// workspace's own .gitignore keeps .polyflow/ out of `git status`;
	// .git/info/exclude (a local-only exclude, no commit needed) does the
	// same here so isCleanCheckoutAt's porcelain check still reports clean.
	require.NoError(t, os.WriteFile(filepath.Join(apiLocal, ".git", "info", "exclude"), []byte(".polyflow/\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(apiLocal, ".polyflow"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(apiLocal, ".polyflow", "graph.db"), nil, 0o644))

	regPath, err := registry.DefaultPath()
	require.NoError(t, err)
	require.NoError(t, registry.Save(regPath, &registry.Registry{
		Version: "1",
		Entries: []registry.Entry{
			{Service: "api", LocalPath: apiLocal, Fleets: []string{"myfleet"}},
		},
		FleetConfigPaths: map[string]string{"myfleet": fleetYML},
	}))

	bridgePath, err := fleetsync.DefaultBridgePath("myfleet")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(bridgePath), 0o755))
	require.NoError(t, os.WriteFile(bridgePath, []byte("bridge"), 0o644))
	fiveMinAgo := time.Now().Add(-5 * time.Minute)
	require.NoError(t, os.Chtimes(bridgePath, fiveMinAgo, fiveMinAgo))

	t.Chdir(apiLocal)

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var runErr error
	out := captureStdout(t, func() {
		runErr = runFleetStatus(cmd, nil)
	})
	require.NoError(t, runErr)

	assert.Contains(t, out, "myfleet:")
	assert.Contains(t, out, "resolved main@"+apiSHA[:7])
	assert.Contains(t, out, "local checkout matches")
	assert.Contains(t, out, "bridge: synced")
}

// TestFleetStatus_NotAFleetMember prints a clear message rather than an
// error or empty output when run outside any registered fleet member.
func TestFleetStatus_NotAFleetMember(t *testing.T) {
	t.Setenv("POLYFLOW_HOME", t.TempDir())
	t.Chdir(t.TempDir())

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var runErr error
	out := captureStdout(t, func() {
		runErr = runFleetStatus(cmd, nil)
	})
	require.NoError(t, runErr)
	assert.Contains(t, out, "not a registered fleet member")
}

// TestRegistry_DefaultHidesNonFleetEntries_AllShowsEverything is GR.5's
// acceptance test for `polyflow registry [--all]`.
func TestRegistry_DefaultHidesNonFleetEntries_AllShowsEverything(t *testing.T) {
	home := t.TempDir()
	t.Setenv("POLYFLOW_HOME", home)

	regPath, err := registry.DefaultPath()
	require.NoError(t, err)
	require.NoError(t, registry.Save(regPath, &registry.Registry{
		Version: "1",
		Entries: []registry.Entry{
			{Service: "fleet-svc", LocalPath: "/tmp/fleet-svc", Fleets: []string{"myfleet"}, IndexedAt: time.Now()},
			{Service: "standalone-svc", LocalPath: "/tmp/standalone-svc", IndexedAt: time.Now()},
		},
	}))

	registryAll = false
	defer func() { registryAll = false }()
	out := captureStdout(t, func() {
		require.NoError(t, runRegistry(&cobra.Command{}, nil))
	})
	assert.Contains(t, out, "fleet-svc")
	assert.Contains(t, out, "myfleet")
	assert.NotContains(t, out, "standalone-svc")

	registryAll = true
	out = captureStdout(t, func() {
		require.NoError(t, runRegistry(&cobra.Command{}, nil))
	})
	assert.Contains(t, out, "fleet-svc")
	assert.Contains(t, out, "standalone-svc")
}
