package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ground truth written repo-relative must match impact output written
// absolute — the shape that zeroed the writefreely and chessleap corpora.
func TestPathCanon_RelativeGroundTruthMatchesAbsoluteOutput(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "cmd", "writefreely"), 0o755))
	target := filepath.Join(root, "cmd", "writefreely", "main.go")
	require.NoError(t, os.WriteFile(target, []byte("package main"), 0o644))

	pc := newPathCanon(root)

	assert.Equal(t, pc.key("cmd/writefreely/main.go"), pc.key(target),
		"the same file spelled two ways must share one key")
}

// A corpus repo reached through the eval/.cache symlink is recorded by the
// graph under the link path while the manifest names the real path.
func TestPathCanon_SymlinkedRepoMatchesRealPath(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "synergy")
	require.NoError(t, os.MkdirAll(filepath.Join(real, "apps"), 0o755))
	target := filepath.Join(real, "apps", "home.tsx")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o644))

	link := filepath.Join(base, "cache-synergy")
	require.NoError(t, os.Symlink(real, link))

	pc := newPathCanon(link) // graph indexed through the symlink

	viaLink := filepath.Join(link, "apps", "home.tsx")
	assert.Equal(t, pc.key(target), pc.key(viaLink),
		"link path and real path are the same file")
}

// Out-of-tree services are legitimate: a path outside the repo keeps its
// absolute form rather than being forced into a bogus relative one.
func TestPathCanon_OutOfTreePathStaysAbsolute(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "corpus")
	outside := filepath.Join(base, "other")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.MkdirAll(outside, 0o755))
	target := filepath.Join(outside, "svc.go")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o644))

	pc := newPathCanon(root)
	key := pc.key(target)

	assert.True(t, filepath.IsAbs(key), "out-of-tree path must stay absolute, got %q", key)
	assert.Equal(t, key, pc.key(target), "and must be stable")
}

// Two absolute paths that already agreed must keep agreeing — the fix widens
// what matches, it must not disturb the corpora that were passing.
func TestPathCanon_AlreadyMatchingAbsolutePathsStillMatch(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "app.go")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o644))

	pc := newPathCanon(root)

	assert.Equal(t, pc.key(target), pc.key(target))
}

// A path that no longer exists cannot be resolved; it must still produce a
// stable key so the case scores as an honest miss rather than crashing.
func TestPathCanon_MissingPathIsStable(t *testing.T) {
	root := t.TempDir()
	pc := newPathCanon(root)

	gone := filepath.Join(root, "deleted", "file.go")
	assert.Equal(t, pc.key(gone), pc.key(gone))
	assert.NotEmpty(t, pc.key(gone))
}

func TestPathCanon_KeySetAndKeysPreserveContent(t *testing.T) {
	pc := newPathCanon(t.TempDir())

	assert.Nil(t, pc.keys(nil))
	assert.Nil(t, pc.keySet(nil))
	assert.Equal(t, []string{"a.go", "b.go"}, pc.keys([]string{"a.go", "./b.go"}))

	set := pc.keySet(map[string]bool{"./x.go": true, "y.go": false})
	assert.True(t, set["x.go"])
	assert.False(t, set["y.go"])
}
