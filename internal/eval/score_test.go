package eval_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/eval"
)

// Q1: all expected files returned — perfect recall & precision
func TestScore_AllMatch(t *testing.T) {
	returned := []string{"a.go", "b.go"}
	expected := []string{"a.go", "b.go"}
	got := eval.Score("q1", returned, expected, expected, nil)

	assert.Equal(t, "q1", got.CaseID)
	assert.InDelta(t, 1.0, got.Recall, 1e-9)
	assert.InDelta(t, 1.0, got.Precision, 1e-9)
	assert.Equal(t, 0, got.HonestMisses)
	assert.Equal(t, 0, got.SilentMisses)
	assert.False(t, got.HardFail)
}

// Q2: nothing returned — zero recall, all silent misses, HardFail on must_not_miss
func TestScore_NoMatch(t *testing.T) {
	returned := []string{}
	expected := []string{"a.go", "b.go"}
	mustNotMiss := []string{"a.go"}
	got := eval.Score("q2", returned, expected, mustNotMiss, nil)

	assert.InDelta(t, 0.0, got.Recall, 1e-9)
	assert.InDelta(t, 0.0, got.Precision, 1e-9)
	assert.Equal(t, 0, got.HonestMisses)
	assert.Equal(t, 2, got.SilentMisses)
	assert.True(t, got.HardFail)
}

// Q3: partial match with honest miss (missed file appears in unresolved ledger)
func TestScore_PartialHonestMiss(t *testing.T) {
	returned := []string{"a.go"}
	expected := []string{"a.go", "b.go", "c.go"}
	// b.go is in the unresolved ledger → honest miss; c.go is not → silent miss
	unresolvedFiles := map[string]bool{"b.go": true}
	mustNotMiss := []string{"a.go"} // a.go IS returned → no HardFail

	got := eval.Score("q3", returned, expected, mustNotMiss, unresolvedFiles)

	assert.InDelta(t, 1.0/3.0, got.Recall, 1e-9)
	assert.InDelta(t, 1.0, got.Precision, 1e-9) // 1/1 returned files match
	assert.Equal(t, 1, got.HonestMisses)
	assert.Equal(t, 1, got.SilentMisses)
	assert.False(t, got.HardFail)
}

// Q4: silent miss on a must_not_miss file → HardFail; honest miss does NOT trigger HardFail
func TestScore_HardFailSilentOnly(t *testing.T) {
	returned := []string{}
	expected := []string{"a.go", "b.go"}
	mustNotMiss := []string{"a.go", "b.go"}
	// a.go is honest (in unresolved), b.go is silent → HardFail because b.go is silent + must_not_miss
	unresolvedFiles := map[string]bool{"a.go": true}

	got := eval.Score("q4", returned, expected, mustNotMiss, unresolvedFiles)

	assert.Equal(t, 1, got.HonestMisses)
	assert.Equal(t, 1, got.SilentMisses)
	assert.True(t, got.HardFail)
}

// Honest miss on a must_not_miss file does NOT trigger HardFail.
func TestScore_HonestMissOnMustNotMissNoHardFail(t *testing.T) {
	returned := []string{}
	expected := []string{"a.go"}
	mustNotMiss := []string{"a.go"}
	unresolvedFiles := map[string]bool{"a.go": true} // honest miss

	got := eval.Score("honest-mnm", returned, expected, mustNotMiss, unresolvedFiles)

	assert.Equal(t, 1, got.HonestMisses)
	assert.Equal(t, 0, got.SilentMisses)
	assert.False(t, got.HardFail, "honest miss on must_not_miss must not HardFail")
}

// AggregateReport macro-averages recall and precision across cases.
func TestAggregateReport(t *testing.T) {
	results := []eval.CaseResult{
		{CaseID: "a", Recall: 1.0, Precision: 0.5},
		{CaseID: "b", Recall: 0.5, Precision: 1.0},
	}
	r := eval.AggregateReport("myrepo", results)

	assert.Equal(t, "myrepo", r.Repo)
	assert.InDelta(t, 0.75, r.Recall, 1e-9)
	assert.InDelta(t, 0.75, r.Precision, 1e-9)
}

// AggregateReport on empty results returns zero values, no panic.
func TestAggregateReport_Empty(t *testing.T) {
	r := eval.AggregateReport("empty", nil)
	assert.Equal(t, "empty", r.Repo)
	assert.InDelta(t, 0.0, r.Recall, 1e-9)
	assert.InDelta(t, 0.0, r.Precision, 1e-9)
}

// Empty expected list: recall stays 0 (no expected = no hits possible), precision
// is 0 when returned files don't intersect anything (empty expected means no hits).
func TestScore_EmptyExpected(t *testing.T) {
	got := eval.Score("empty-exp", []string{"a.go"}, nil, nil, nil)
	assert.InDelta(t, 0.0, got.Recall, 1e-9)
	assert.InDelta(t, 0.0, got.Precision, 1e-9)
	assert.False(t, got.HardFail)
}
