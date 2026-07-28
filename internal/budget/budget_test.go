package budget_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/budget"
)

func TestEstimate(t *testing.T) {
	// {"a":"bbbb"} → 12 bytes → 3 tokens.
	assert.Equal(t, 3, budget.Estimate(map[string]string{"a": "bbbb"}))
	// Unmarshalable values estimate to 0 instead of failing.
	assert.Equal(t, 0, budget.Estimate(func() {}))
}

func TestTrimToFit_KeepsAllWhenWithinBudget(t *testing.T) {
	est := func(n int) int { return n * 10 }
	assert.Equal(t, 5, budget.TrimToFit(5, 50, est))
}

func TestTrimToFit_TrimsToLargestFittingPrefix(t *testing.T) {
	est := func(n int) int { return n * 10 }
	assert.Equal(t, 3, budget.TrimToFit(10, 35, est))
}

func TestTrimToFit_KeepsAtLeastOneEntry(t *testing.T) {
	est := func(n int) int { return 1000 + n }
	assert.Equal(t, 1, budget.TrimToFit(10, 5, est))
}

func TestSnippet_ReadsRequestedLines(t *testing.T) {
	got := budget.Snippet("testdata", "snippet.txt", 2, 3)
	assert.Equal(t, "line 2\nline 3\nline 4", got)
}

func TestSnippet_ClampsAtEOF(t *testing.T) {
	got := budget.Snippet("testdata", "snippet.txt", 5, 10)
	assert.Equal(t, "line 5\n", got)
}

func TestSnippet_BestEffortFailuresReturnEmpty(t *testing.T) {
	assert.Empty(t, budget.Snippet("testdata", "missing.txt", 1, 3), "missing file")
	assert.Empty(t, budget.Snippet("testdata", "snippet.txt", 99, 3), "start past EOF")
	assert.Empty(t, budget.Snippet("testdata", "snippet.txt", 1, 0), "zero lines")
	assert.Empty(t, budget.Snippet("testdata", "", 1, 3), "empty path")
}

func TestSnippetSpan_ExactSpan(t *testing.T) {
	src, truncated := budget.SnippetSpan("testdata", "snippet.txt", 2, 4, 200)
	assert.Equal(t, "line 2\nline 3\nline 4", src)
	assert.False(t, truncated)
}

func TestSnippetSpan_UnknownEndWindows(t *testing.T) {
	// end<=0 → maxLines window from start (Snippet semantics).
	src, truncated := budget.SnippetSpan("testdata", "snippet.txt", 2, 0, 3)
	assert.Equal(t, "line 2\nline 3\nline 4", src)
	assert.False(t, truncated)
}

func TestSnippetSpan_TruncatesRunawaySpan(t *testing.T) {
	src, truncated := budget.SnippetSpan("testdata", "snippet.txt", 1, 5, 3)
	assert.Equal(t, "line 1\nline 2\nline 3", src)
	assert.True(t, truncated)
}

func TestSnippetSpan_ClampsAtEOF(t *testing.T) {
	// end past EOF clamps; never reads past the last line.
	src, truncated := budget.SnippetSpan("testdata", "snippet.txt", 5, 99, 200)
	assert.Equal(t, "line 5\n", src)
	assert.False(t, truncated)
}

func TestSnippetSpan_BestEffortFailuresReturnEmpty(t *testing.T) {
	src, _ := budget.SnippetSpan("testdata", "missing.txt", 1, 3, 200)
	assert.Empty(t, src, "missing file")
	src, _ = budget.SnippetSpan("testdata", "snippet.txt", 99, 100, 200)
	assert.Empty(t, src, "start past EOF")
	src, _ = budget.SnippetSpan("testdata", "snippet.txt", 1, 3, 0)
	assert.Empty(t, src, "zero maxLines")
	src, _ = budget.SnippetSpan("testdata", "", 1, 3, 200)
	assert.Empty(t, src, "empty path")
}
