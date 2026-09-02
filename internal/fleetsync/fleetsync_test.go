package fleetsync_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/fleetconfig"
	"github.com/lordsonvimal/polyflow/internal/fleetsync"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/registry"
)

const polyflowYML = `
name: svc
version: "1"
services:
  - name: svc
    path: .
    language: go
`

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// newBareRepo creates a bare remote and a first commit (polyflow.yml + a
// trivial source file) on "main", pushed to the bare repo. Returns the bare
// repo path (usable as a git URL) and the commit SHA.
func newBareRepo(t *testing.T) (bareURL, sha string) {
	t.Helper()
	bareDir := t.TempDir()
	runGit(t, "", "init", "--bare", bareDir)
	// Pin HEAD to "main" regardless of the runner's init.defaultBranch —
	// CI runners default to "master", which leaves clones in a detached
	// state once we push only "main", breaking pushNewCommit's push.
	runGit(t, bareDir, "symbolic-ref", "HEAD", "refs/heads/main")

	workDir := t.TempDir()
	runGit(t, workDir, "init")
	runGit(t, workDir, "checkout", "-b", "main")
	runGit(t, workDir, "config", "user.email", "test@example.com")
	runGit(t, workDir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "polyflow.yml"), []byte(polyflowYML), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	runGit(t, workDir, "add", ".")
	runGit(t, workDir, "commit", "-m", "init")
	runGit(t, workDir, "remote", "add", "origin", bareDir)
	runGit(t, workDir, "push", "origin", "main")

	return bareDir, runGit(t, workDir, "rev-parse", "HEAD")
}

// pushNewCommit adds a second commit on top of an existing clone of bareURL
// and pushes it, moving main's head. Returns the new SHA.
func pushNewCommit(t *testing.T, bareURL string) string {
	t.Helper()
	workDir := t.TempDir()
	runGit(t, "", "clone", bareURL, workDir)
	runGit(t, workDir, "config", "user.email", "test@example.com")
	runGit(t, workDir, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "extra.go"), []byte("package main\n"), 0o644))
	runGit(t, workDir, "add", ".")
	runGit(t, workDir, "commit", "-m", "second")
	runGit(t, workDir, "push", "origin", "main")
	return runGit(t, workDir, "rev-parse", "HEAD")
}

func cloneAt(t *testing.T, bareURL, dest string) {
	t.Helper()
	runGit(t, "", "clone", bareURL, dest)
}

func newRegistryPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "registry.yml")
}

func isEmptyDir(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	return len(entries) == 0
}

func TestResolveService_CleanLocalMatch_NoClone(t *testing.T) {
	bareURL, sha := newBareRepo(t)
	svc := fleetconfig.Service{Name: "svc", Git: bareURL, Ref: "main"}

	localDir := filepath.Join(t.TempDir(), "local")
	cloneAt(t, bareURL, localDir)

	regPath := newRegistryPath(t)
	require.NoError(t, registry.Sync(regPath, "svc", localDir))

	scratch := t.TempDir()
	dbPath, resolvedSHA, err := fleetsync.ResolveService(context.Background(), svc, "", fleetsync.ResolveOptions{
		RegistryPath: regPath,
		ScratchDir:   scratch,
	})
	require.NoError(t, err)
	assert.Equal(t, sha, resolvedSHA)
	assert.Equal(t, filepath.Join(localDir, meta.DBDir, meta.DBFile), dbPath)
	assert.True(t, isEmptyDir(t, scratch), "clean local match must not clone")

	// Running it again performs no clone either — the acceptance bar for
	// GR.1 (zero network beyond the one ls-remote on a clean checkout).
	dbPath2, resolvedSHA2, err := fleetsync.ResolveService(context.Background(), svc, "", fleetsync.ResolveOptions{
		RegistryPath: regPath,
		ScratchDir:   scratch,
	})
	require.NoError(t, err)
	assert.Equal(t, dbPath, dbPath2)
	assert.Equal(t, resolvedSHA, resolvedSHA2)
	assert.True(t, isEmptyDir(t, scratch))
}

// TestResolveService_CleanLocalMatch_Unindexed_IndexesInPlace covers the
// `polyflow fleet sync` bug: a registered checkout at the right SHA whose
// graph.db was never built must be indexed in place (no scratch re-clone)
// so the returned path actually exists for the bridge build to open.
func TestResolveService_CleanLocalMatch_Unindexed_IndexesInPlace(t *testing.T) {
	bareURL, sha := newBareRepo(t)
	svc := fleetconfig.Service{Name: "svc", Git: bareURL, Ref: "main"}

	localDir := filepath.Join(t.TempDir(), "local")
	cloneAt(t, bareURL, localDir)

	regPath := newRegistryPath(t)
	require.NoError(t, registry.Sync(regPath, "svc", localDir))

	dbPath := filepath.Join(localDir, meta.DBDir, meta.DBFile)
	require.NoFileExists(t, dbPath)

	scratch := t.TempDir()
	got, resolvedSHA, err := fleetsync.ResolveService(context.Background(), svc, "", fleetsync.ResolveOptions{
		RegistryPath: regPath,
		ScratchDir:   scratch,
	})
	require.NoError(t, err)
	assert.Equal(t, sha, resolvedSHA)
	assert.Equal(t, dbPath, got)
	assert.FileExists(t, dbPath, "an unindexed clean checkout must be indexed in place")
	assert.True(t, isEmptyDir(t, scratch), "indexing in place must not clone")
}

func TestResolveService_DirtyLocalMatch_ReusesLocal(t *testing.T) {
	bareURL, sha := newBareRepo(t)
	svc := fleetconfig.Service{Name: "svc", Git: bareURL, Ref: "main"}

	localDir := filepath.Join(t.TempDir(), "local")
	cloneAt(t, bareURL, localDir)
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "main.go"), []byte("package main\n\nfunc main() { println(1) }\n"), 0o644))

	regPath := newRegistryPath(t)
	require.NoError(t, registry.Sync(regPath, "svc", localDir))

	scratch := t.TempDir()
	dbPath, resolvedSHA, err := fleetsync.ResolveService(context.Background(), svc, "", fleetsync.ResolveOptions{
		RegistryPath: regPath,
		ScratchDir:   scratch,
	})
	require.NoError(t, err)
	assert.Equal(t, sha, resolvedSHA)
	assert.True(t, isEmptyDir(t, scratch), "dirty local match at the right SHA must not clone")
	assert.Equal(t, filepath.Join(localDir, meta.DBDir, meta.DBFile), dbPath)
}

func TestResolveService_WrongSHALocalMatch_Clones(t *testing.T) {
	bareURL, sha1 := newBareRepo(t)
	svc := fleetconfig.Service{Name: "svc", Git: bareURL, Ref: "main"}

	localDir := filepath.Join(t.TempDir(), "local")
	cloneAt(t, bareURL, localDir)

	regPath := newRegistryPath(t)
	require.NoError(t, registry.Sync(regPath, "svc", localDir))

	sha2 := pushNewCommit(t, bareURL)
	require.NotEqual(t, sha1, sha2)

	scratch := t.TempDir()
	dbPath, resolvedSHA, err := fleetsync.ResolveService(context.Background(), svc, "", fleetsync.ResolveOptions{
		RegistryPath: regPath,
		ScratchDir:   scratch,
	})
	require.NoError(t, err)
	assert.Equal(t, sha2, resolvedSHA)
	assert.False(t, isEmptyDir(t, scratch), "stale local SHA must clone")
	assert.FileExists(t, dbPath)
}

// TestResolveService_DirtyLocalMatch_EphemeralScratch_DoesNotClobberRegistry
// proves cloneAndIndex's fix: an auto-generated (ScratchDir == "") temp
// clone must never overwrite a real, durable registry entry with its own
// ephemeral path — that path is gone by the next process, so registering it
// would turn a working "local checkout matches" resolution into a
// permanently dangling one.
func TestResolveService_DirtyLocalMatch_NoScratchDir_ReusesLocal(t *testing.T) {
	bareURL, _ := newBareRepo(t)
	svc := fleetconfig.Service{Name: "svc", Git: bareURL, Ref: "main"}

	localDir := filepath.Join(t.TempDir(), "local")
	cloneAt(t, bareURL, localDir)
	require.NoError(t, os.WriteFile(filepath.Join(localDir, "main.go"), []byte("package main\n\nfunc main() { println(1) }\n"), 0o644))

	regPath := newRegistryPath(t)
	require.NoError(t, registry.Sync(regPath, "svc", localDir))

	// No ScratchDir set: a dirty checkout at the right SHA must still reuse
	// localDir directly rather than falling to cloneAndIndex's own
	// os.MkdirTemp fallback.
	_, _, err := fleetsync.ResolveService(context.Background(), svc, "", fleetsync.ResolveOptions{
		RegistryPath: regPath,
	})
	require.NoError(t, err)

	reg, err := registry.Load(regPath)
	require.NoError(t, err)
	entry, ok := reg.Lookup("svc")
	require.True(t, ok)
	assert.Equal(t, localDir, entry.LocalPath, "registry entry must still point at the real durable checkout, not an ephemeral scratch clone")
}

func TestResolveService_NoLocalEntry_CacheHit_NoClone(t *testing.T) {
	bareURL, sha := newBareRepo(t)
	svc := fleetconfig.Service{Name: "svc", Git: bareURL, Ref: "main"}

	cacheDir := t.TempDir()
	cached := filepath.Join(cacheDir, "svc", sha, meta.DBFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(cached), 0o755))
	require.NoError(t, os.WriteFile(cached, []byte("cached-db"), 0o644))

	scratch := t.TempDir()
	dbPath, resolvedSHA, err := fleetsync.ResolveService(context.Background(), svc, "", fleetsync.ResolveOptions{
		RegistryPath: newRegistryPath(t),
		CacheDir:     cacheDir,
		ScratchDir:   scratch,
	})
	require.NoError(t, err)
	assert.Equal(t, sha, resolvedSHA)
	assert.Equal(t, cached, dbPath)
	assert.True(t, isEmptyDir(t, scratch), "a cache hit must not clone")
}

// TestResolveStatus_CleanLocalMatch_ReportsLocalNoClone is GR.5's read-only
// counterpart to TestResolveService_CleanLocalMatch_NoClone: a clean
// checkout whose graph.db is on disk reports Source=="local" without ever
// touching ScratchDir — a status view has no step 4.
func TestResolveStatus_CleanLocalMatch_ReportsLocalNoClone(t *testing.T) {
	bareURL, sha := newBareRepo(t)
	svc := fleetconfig.Service{Name: "svc", Git: bareURL, Ref: "main"}

	localDir := filepath.Join(t.TempDir(), "local")
	cloneAt(t, bareURL, localDir)

	dbPath := filepath.Join(localDir, meta.DBDir, meta.DBFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o755))
	require.NoError(t, os.WriteFile(dbPath, []byte("db"), 0o644))

	regPath := newRegistryPath(t)
	require.NoError(t, registry.Sync(regPath, "svc", localDir))

	st, err := fleetsync.ResolveStatus(context.Background(), svc, "", fleetsync.ResolveOptions{
		RegistryPath: regPath,
	})
	require.NoError(t, err)
	assert.Equal(t, sha, st.SHA)
	assert.Equal(t, "local", st.Source)
	assert.Equal(t, localDir, st.LocalPath)
}

// TestResolveStatus_CleanLocalMatch_Unindexed_ReportsLocalUnindexed covers a
// registered checkout at the right SHA whose graph.db was never built (or,
// for a Subpath member, whose per-service shard a whole-workspace index
// never wrote): status reports Source=="local-unindexed" so the operator
// knows the next sync will index it in place rather than clone.
func TestResolveStatus_CleanLocalMatch_Unindexed_ReportsLocalUnindexed(t *testing.T) {
	bareURL, sha := newBareRepo(t)
	svc := fleetconfig.Service{Name: "svc", Git: bareURL, Ref: "main"}

	localDir := filepath.Join(t.TempDir(), "local")
	cloneAt(t, bareURL, localDir)

	regPath := newRegistryPath(t)
	require.NoError(t, registry.Sync(regPath, "svc", localDir))

	st, err := fleetsync.ResolveStatus(context.Background(), svc, "", fleetsync.ResolveOptions{
		RegistryPath: regPath,
	})
	require.NoError(t, err)
	assert.Equal(t, sha, st.SHA)
	assert.Equal(t, "local-unindexed", st.Source)
	assert.Equal(t, localDir, st.LocalPath)
}

// TestResolveStatus_CacheHit_ReportsCache is GR.5's read-only counterpart to
// TestResolveService_NoLocalEntry_CacheHit_NoClone.
func TestResolveStatus_CacheHit_ReportsCache(t *testing.T) {
	bareURL, sha := newBareRepo(t)
	svc := fleetconfig.Service{Name: "svc", Git: bareURL, Ref: "main"}

	cacheDir := t.TempDir()
	cached := filepath.Join(cacheDir, "svc", sha, meta.DBFile)
	require.NoError(t, os.MkdirAll(filepath.Dir(cached), 0o755))
	require.NoError(t, os.WriteFile(cached, []byte("cached-db"), 0o644))

	st, err := fleetsync.ResolveStatus(context.Background(), svc, "", fleetsync.ResolveOptions{
		RegistryPath: newRegistryPath(t),
		CacheDir:     cacheDir,
	})
	require.NoError(t, err)
	assert.Equal(t, sha, st.SHA)
	assert.Equal(t, "cache", st.Source)
}

// TestResolveStatus_NoLocalNoCache_ReportsUnresolvedNoClone proves the whole
// point of a status command versus a sync: a cold member (neither a local
// checkout nor a cache hit) is reported as Source=="unresolved" rather than
// falling through to a clone.
func TestResolveStatus_NoLocalNoCache_ReportsUnresolvedNoClone(t *testing.T) {
	bareURL, sha := newBareRepo(t)
	svc := fleetconfig.Service{Name: "svc", Git: bareURL, Ref: "main"}

	st, err := fleetsync.ResolveStatus(context.Background(), svc, "", fleetsync.ResolveOptions{
		RegistryPath: newRegistryPath(t),
		CacheDir:     t.TempDir(),
	})
	require.NoError(t, err)
	assert.Equal(t, sha, st.SHA)
	assert.Equal(t, "unresolved", st.Source)
}

func TestResolveService_NoLocalEntry_CacheMiss_ClonesAndPopulatesCache(t *testing.T) {
	bareURL, sha := newBareRepo(t)
	svc := fleetconfig.Service{Name: "svc", Git: bareURL, Ref: "main"}

	cacheDir := t.TempDir()
	scratch := t.TempDir()
	dbPath, resolvedSHA, err := fleetsync.ResolveService(context.Background(), svc, "", fleetsync.ResolveOptions{
		RegistryPath: newRegistryPath(t),
		CacheDir:     cacheDir,
		ScratchDir:   scratch,
	})
	require.NoError(t, err)
	assert.Equal(t, sha, resolvedSHA)
	assert.False(t, isEmptyDir(t, scratch), "a cache miss must clone")

	cachedPath := filepath.Join(cacheDir, "svc", sha, meta.DBFile)
	assert.FileExists(t, cachedPath)
	assert.NotEqual(t, dbPath, cachedPath)
}
