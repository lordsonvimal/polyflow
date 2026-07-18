package graph_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestBuildVerificationSummary_Counts(t *testing.T) {
	edges := []graph.Edge{
		{ID: "e1", VerificationState: graph.StateVerified},
		{ID: "e2", VerificationState: graph.StateVerified},
		{ID: "e3", VerificationState: graph.StateCandidate},
		{ID: "e4", VerificationState: graph.StateObservedOnlyGap},
		{ID: "e5", VerificationState: graph.StateConflicting},
		{ID: "e6"}, // no state — not counted
	}
	vs := graph.BuildVerificationSummary(edges)
	assert.Equal(t, 2, vs.Verified)
	assert.Equal(t, 1, vs.Candidate)
	assert.Equal(t, 1, vs.ObservedOnlyGap)
	assert.Equal(t, 1, vs.Conflicting)
}

func TestBuildVerificationSummary_NoteWhenClean(t *testing.T) {
	vs := graph.BuildVerificationSummary([]graph.Edge{
		{ID: "e1", VerificationState: graph.StateVerified},
	})
	assert.Equal(t, 1, vs.Verified)
	assert.Empty(t, vs.Note, "no note when only verified edges")
}

func TestBuildVerificationSummary_NoteCandidate(t *testing.T) {
	vs := graph.BuildVerificationSummary([]graph.Edge{
		{ID: "e1", VerificationState: graph.StateCandidate},
	})
	assert.Contains(t, vs.Note, "candidate")
	assert.Contains(t, vs.Note, "static-only")
}

func TestBuildVerificationSummary_NoteGap(t *testing.T) {
	vs := graph.BuildVerificationSummary([]graph.Edge{
		{ID: "e1", VerificationState: graph.StateObservedOnlyGap},
	})
	assert.Contains(t, vs.Note, "observed-only gaps")
}

func TestBuildVerificationSummary_EmptyEdges(t *testing.T) {
	vs := graph.BuildVerificationSummary(nil)
	assert.Equal(t, graph.VerificationSummary{}, vs)
	assert.Empty(t, vs.Note)
}

func TestBuildVerificationSummary_NoStateCounted(t *testing.T) {
	// Pre-F.0 edges with no VerificationState must not inflate counts.
	vs := graph.BuildVerificationSummary([]graph.Edge{
		{ID: "e1"},
		{ID: "e2"},
	})
	assert.Equal(t, 0, vs.Verified)
	assert.Equal(t, 0, vs.Candidate)
}

func TestVerificationSummaryLine_ZeroReturnsEmpty(t *testing.T) {
	assert.Empty(t, graph.VerificationSummaryLine(graph.VerificationSummary{}))
}

func TestVerificationSummaryLine_NonZero(t *testing.T) {
	line := graph.VerificationSummaryLine(graph.VerificationSummary{
		Verified: 3, Candidate: 1,
	})
	assert.Contains(t, line, "verified=3")
	assert.Contains(t, line, "candidate=1")
}

func TestCompactSources_OrderedByProviderThenRef(t *testing.T) {
	sources := []graph.SourceRef{
		{Provider: "static", Ref: "b.go:20"},
		{Provider: "runtime", Ref: "sess1/trace2"},
		{Provider: "runtime", Ref: "sess1/trace1"},
		{Provider: "static", Ref: "a.go:10"},
	}
	compact := graph.CompactSources(sources)
	require.Len(t, compact, 4)
	assert.Equal(t, "runtime:sess1/trace1", compact[0])
	assert.Equal(t, "runtime:sess1/trace2", compact[1])
	assert.Equal(t, "static:a.go:10", compact[2])
	assert.Equal(t, "static:b.go:20", compact[3])
}

func TestCompactSources_Deterministic(t *testing.T) {
	sources := []graph.SourceRef{
		{Provider: "static", Ref: "z.go:1"},
		{Provider: "runtime", Ref: "s/t"},
	}
	// Two calls on the same input must produce identical output (rule 2).
	a := graph.CompactSources(sources)
	b := graph.CompactSources(sources)
	require.Equal(t, a, b)
}

func TestCompactSources_Empty(t *testing.T) {
	assert.Nil(t, graph.CompactSources(nil))
	assert.Nil(t, graph.CompactSources([]graph.SourceRef{}))
}

func TestSortedSources_OrderedByProviderThenRef(t *testing.T) {
	sources := []graph.SourceRef{
		{Provider: "static", Ref: "b.go:2", Confidence: "static"},
		{Provider: "runtime", Ref: "s/t1"},
		{Provider: "static", Ref: "a.go:1"},
	}
	sorted := graph.SortedSources(sources)
	require.Len(t, sorted, 3)
	assert.Equal(t, "runtime", sorted[0].Provider)
	assert.Equal(t, "a.go:1", sorted[1].Ref)
	assert.Equal(t, "b.go:2", sorted[2].Ref)
}
