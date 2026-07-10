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
