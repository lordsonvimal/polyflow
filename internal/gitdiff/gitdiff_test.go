package gitdiff_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/gitdiff"
)

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
