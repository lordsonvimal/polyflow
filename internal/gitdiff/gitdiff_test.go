package gitdiff_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/gitdiff"
)

// initGitRepo creates a git repository at dir with one committed file
// (committed.txt), so callers can then modify it to produce a real diff.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "committed.txt"), []byte("line1\nline2\n"), 0o644))
	run("add", "committed.txt")
	run("commit", "-q", "-m", "init")
}

func TestParse_ModifiedFileMultipleHunks(t *testing.T) {
	diff := `diff --git a/internal/foo/foo.go b/internal/foo/foo.go
index 1111111..2222222 100644
--- a/internal/foo/foo.go
+++ b/internal/foo/foo.go
@@ -10,2 +10,3 @@ func A() {
-	old := 1
-	use(old)
+	n := 2
+	use(n)
+	more(n)
@@ -40 +41 @@ func B() {
-	return x
+	return y
`
	got := gitdiff.Parse(strings.NewReader(diff))
	require.Len(t, got, 1)
	assert.Equal(t, "internal/foo/foo.go", got[0].Path)
	assert.False(t, got[0].Deleted)
	assert.Equal(t, []gitdiff.Span{{Start: 10, End: 12}, {Start: 41, End: 41}}, got[0].Spans)
}

func TestParse_PureDeletionAnchorsToPrecedingLine(t *testing.T) {
	diff := `diff --git a/a.go b/a.go
index 1111111..2222222 100644
--- a/a.go
+++ b/a.go
@@ -21,3 +20,0 @@ func C() {
-	a()
-	b()
-	c()
`
	got := gitdiff.Parse(strings.NewReader(diff))
	require.Len(t, got, 1)
	// New-side count 0: the deletion anchors to the line before the cut.
	assert.Equal(t, []gitdiff.Span{{Start: 20, End: 20}}, got[0].Spans)
}

func TestParse_NewFile(t *testing.T) {
	diff := `diff --git a/new.go b/new.go
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/new.go
@@ -0,0 +1,4 @@
+package new
+
+func New() {
+}
`
	got := gitdiff.Parse(strings.NewReader(diff))
	require.Len(t, got, 1)
	assert.Equal(t, "new.go", got[0].Path)
	assert.False(t, got[0].Deleted)
	assert.Equal(t, []gitdiff.Span{{Start: 1, End: 4}}, got[0].Spans)
}

func TestParse_DeletedFile(t *testing.T) {
	diff := `diff --git a/gone.go b/gone.go
deleted file mode 100644
index 3333333..0000000
--- a/gone.go
+++ /dev/null
@@ -1,4 +0,0 @@
-package gone
-
-func Gone() {
-}
`
	got := gitdiff.Parse(strings.NewReader(diff))
	require.Len(t, got, 1)
	assert.Equal(t, "gone.go", got[0].Path)
	assert.True(t, got[0].Deleted)
	assert.Empty(t, got[0].Spans)
}

func TestParse_BinaryFileHasNoSpans(t *testing.T) {
	diff := `diff --git a/logo.png b/logo.png
index 1111111..2222222 100644
Binary files a/logo.png and b/logo.png differ
`
	got := gitdiff.Parse(strings.NewReader(diff))
	// No ---/+++ header pair for binary diffs without --text: no entry.
	assert.Empty(t, got)
}

func TestParse_ContentLineLookingLikeFileHeaderIsIgnored(t *testing.T) {
	// A removed content line "-- a/x" renders as "--- a/x" in the diff body;
	// it must not be mistaken for a file header.
	diff := `diff --git a/notes.txt b/notes.txt
index 1111111..2222222 100644
--- a/notes.txt
+++ b/notes.txt
@@ -5 +5 @@
--- a/decoy.go
+++ b/decoy-replacement.go
`
	got := gitdiff.Parse(strings.NewReader(diff))
	require.Len(t, got, 1)
	assert.Equal(t, "notes.txt", got[0].Path)
	assert.Equal(t, []gitdiff.Span{{Start: 5, End: 5}}, got[0].Spans)
}

func TestParse_EmptyDiff(t *testing.T) {
	assert.Empty(t, gitdiff.Parse(strings.NewReader("")))
}

func TestRoot_FindsRepoRoot(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	sub := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	root, err := gitdiff.Root(sub)
	require.NoError(t, err)

	wantRoot, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	gotRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	assert.Equal(t, wantRoot, gotRoot)
}

func TestRoot_NotAGitRepoErrors(t *testing.T) {
	_, err := gitdiff.Root(t.TempDir())
	assert.Error(t, err)
}

func TestResolveRoots_DedupesAndReportsNoGitRepo(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	subA := filepath.Join(repo, "svc-a")
	subB := filepath.Join(repo, "svc-b")
	require.NoError(t, os.MkdirAll(subA, 0o755))
	require.NoError(t, os.MkdirAll(subB, 0o755))
	noRepo := t.TempDir()

	roots := gitdiff.ResolveRoots([]gitdiff.ServiceDir{
		{Name: "svc-a", Path: subA},
		{Name: "svc-b", Path: subB},
		{Name: "svc-c", Path: noRepo},
	})
	require.Len(t, roots, 3)
	assert.False(t, roots[0].NoGitRepo)
	assert.False(t, roots[1].NoGitRepo)
	assert.True(t, roots[2].NoGitRepo)

	wantRoot, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	gotA, err := filepath.EvalSymlinks(roots[0].Root)
	require.NoError(t, err)
	gotB, err := filepath.EvalSymlinks(roots[1].Root)
	require.NoError(t, err)
	assert.Equal(t, wantRoot, gotA)
	assert.Equal(t, gotA, gotB, "svc-a and svc-b share one repo root")
}

// TestMultiChanges_UnionsAcrossRootsWithAbsolutePaths proves the Z.1 core
// behavior: two services in two separate git repos each get their own
// `git diff` run, and the union comes back as absolute, root-joined paths
// so they compare directly against graph.Node.File (Z.0: node files are
// absolute).
func TestMultiChanges_UnionsAcrossRootsWithAbsolutePaths(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()
	initGitRepo(t, repoA)
	initGitRepo(t, repoB)
	require.NoError(t, os.WriteFile(filepath.Join(repoA, "committed.txt"), []byte("line1\nCHANGED-A\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoB, "committed.txt"), []byte("line1\nCHANGED-B\n"), 0o644))

	roots := gitdiff.ResolveRoots([]gitdiff.ServiceDir{
		{Name: "svc-a", Path: repoA},
		{Name: "svc-b", Path: repoB},
	})
	changes, err := gitdiff.MultiChanges(roots, false)
	require.NoError(t, err)
	require.Len(t, changes, 2)

	wantA, err := filepath.EvalSymlinks(filepath.Join(repoA, "committed.txt"))
	require.NoError(t, err)
	wantB, err := filepath.EvalSymlinks(filepath.Join(repoB, "committed.txt"))
	require.NoError(t, err)

	gotPaths := make([]string, len(changes))
	for i, ch := range changes {
		p, err := filepath.EvalSymlinks(ch.Path)
		require.NoError(t, err)
		gotPaths[i] = p
		assert.True(t, filepath.IsAbs(ch.Path), "expected absolute path, got %q", ch.Path)
	}
	assert.ElementsMatch(t, []string{wantA, wantB}, gotPaths)
}

// TestMultiChanges_OnlyOneRootChangedContributesNothingFromTheOther proves a
// clean repo contributes zero changes and does not error just because a
// sibling repo in the same workspace has edits.
func TestMultiChanges_OnlyOneRootChangedContributesNothingFromTheOther(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()
	initGitRepo(t, repoA)
	initGitRepo(t, repoB) // repoB stays clean — no edits after init commit
	require.NoError(t, os.WriteFile(filepath.Join(repoA, "committed.txt"), []byte("line1\nCHANGED-A\n"), 0o644))

	roots := gitdiff.ResolveRoots([]gitdiff.ServiceDir{
		{Name: "svc-a", Path: repoA},
		{Name: "svc-b", Path: repoB},
	})
	changes, err := gitdiff.MultiChanges(roots, false)
	require.NoError(t, err)
	require.Len(t, changes, 1)

	wantA, err := filepath.EvalSymlinks(filepath.Join(repoA, "committed.txt"))
	require.NoError(t, err)
	gotA, err := filepath.EvalSymlinks(changes[0].Path)
	require.NoError(t, err)
	assert.Equal(t, wantA, gotA)
}

// TestMultiChanges_SkipsNoGitRepoWithoutError proves a service outside any
// git repo does not abort the rest of the diff — the caller (impact package)
// surfaces it separately via AppendNoGitRepo.
func TestMultiChanges_SkipsNoGitRepoWithoutError(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "committed.txt"), []byte("line1\nCHANGED\n"), 0o644))
	noRepo := t.TempDir()

	roots := gitdiff.ResolveRoots([]gitdiff.ServiceDir{
		{Name: "svc-a", Path: repo},
		{Name: "svc-b", Path: noRepo},
	})
	changes, err := gitdiff.MultiChanges(roots, false)
	require.NoError(t, err)
	require.Len(t, changes, 1)
}

// TestMultiChanges_Determinism runs the union twice over the same input and
// requires byte-identical output (bug-class rule 2).
func TestMultiChanges_Determinism(t *testing.T) {
	repoA := t.TempDir()
	repoB := t.TempDir()
	initGitRepo(t, repoA)
	initGitRepo(t, repoB)
	require.NoError(t, os.WriteFile(filepath.Join(repoA, "committed.txt"), []byte("line1\nCHANGED-A\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoB, "committed.txt"), []byte("line1\nCHANGED-B\n"), 0o644))

	roots := gitdiff.ResolveRoots([]gitdiff.ServiceDir{
		{Name: "svc-a", Path: repoA},
		{Name: "svc-b", Path: repoB},
	})

	run1, err := gitdiff.MultiChanges(roots, false)
	require.NoError(t, err)
	run2, err := gitdiff.MultiChanges(roots, false)
	require.NoError(t, err)
	assert.Equal(t, run1, run2)
}
