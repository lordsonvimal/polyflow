package graph_test

import (
	"testing"
	"time"

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

// ─── C.2: age rendering + freshness ──────────────────────────────────────────

func TestAgeString_Days(t *testing.T) {
	now := time.Unix(1000*86400, 0) // arbitrary reference
	ts := now.Unix() - 43*86400     // 43 days ago
	assert.Equal(t, "43d old", graph.AgeString(ts, now))
}

func TestAgeString_Hours(t *testing.T) {
	now := time.Unix(1000*86400, 0)
	ts := now.Unix() - 5*3600
	assert.Equal(t, "5h old", graph.AgeString(ts, now))
}

func TestAgeString_Minutes(t *testing.T) {
	now := time.Unix(1000*86400, 0)
	ts := now.Unix() - 12*60
	assert.Equal(t, "12m old", graph.AgeString(ts, now))
}

func TestAgeString_JustNow(t *testing.T) {
	now := time.Unix(1000*86400, 0)
	ts := now.Unix() - 30 // 30 seconds ago
	assert.Equal(t, "just now", graph.AgeString(ts, now))
}

func TestAgeString_ZeroTimestamp(t *testing.T) {
	now := time.Now()
	assert.Equal(t, "", graph.AgeString(0, now))
}

func TestAgeString_ZeroNow(t *testing.T) {
	assert.Equal(t, "", graph.AgeString(1000, time.Time{}))
}

func TestAgeString_FutureTimestamp(t *testing.T) {
	now := time.Unix(1000*86400, 0)
	ts := now.Unix() + 100 // future (clock skew)
	assert.Equal(t, "", graph.AgeString(ts, now))
}

func TestCompactSourcesAt_AgeAnnotation(t *testing.T) {
	now := time.Unix(2000*86400, 0)
	staleTS := now.Unix() - 43*86400
	sources := []graph.SourceRef{
		{Provider: "runtime", Ref: "sess/trace1", ObservedAt: staleTS},
		{Provider: "static", Ref: "a.go:10"},
	}
	compact := graph.CompactSourcesAt(sources, now)
	require.Len(t, compact, 2)
	assert.Equal(t, "runtime:sess/trace1 (43d old)", compact[0])
	assert.Equal(t, "static:a.go:10", compact[1])
}

func TestCompactSourcesAt_NoAgeWhenZeroNow(t *testing.T) {
	sources := []graph.SourceRef{
		{Provider: "runtime", Ref: "sess/trace1", ObservedAt: 1000},
	}
	compact := graph.CompactSourcesAt(sources, time.Time{})
	require.Len(t, compact, 1)
	assert.Equal(t, "runtime:sess/trace1", compact[0])
}

func TestCompactSourcesAt_NoAgeWhenObservedAtZero(t *testing.T) {
	now := time.Now()
	sources := []graph.SourceRef{
		{Provider: "runtime", Ref: "sess/trace1", ObservedAt: 0},
	}
	compact := graph.CompactSourcesAt(sources, now)
	require.Len(t, compact, 1)
	assert.Equal(t, "runtime:sess/trace1", compact[0])
}

// TestBuildVerificationSummaryAt_NoteNotDowngrade is the acceptance test for C.2:
// aging a fixture session's observed_at adds the stale_evidence note without
// changing VerificationState on any edge.
func TestBuildVerificationSummaryAt_NoteNotDowngrade(t *testing.T) {
	now := time.Unix(2000*86400, 0)
	staleTS := now.Unix() - 43*86400 // 43d old, past default 30d threshold

	edges := []graph.Edge{
		{
			ID:                "e1",
			VerificationState: graph.StateVerified,
			Sources: []graph.SourceRef{
				{Provider: "runtime", Ref: "sess/trace1", ObservedAt: staleTS},
			},
		},
		{
			ID:                "e2",
			VerificationState: graph.StateCandidate,
		},
	}

	staleAfter := 30 * 24 * time.Hour // default threshold
	vs := graph.BuildVerificationSummaryAt(edges, staleAfter, now)

	// State counts must be unchanged — staleness never downgrades.
	assert.Equal(t, 1, vs.Verified, "verified count must be unchanged")
	assert.Equal(t, 1, vs.Candidate, "candidate count must be unchanged")

	// stale_evidence count reflects the stale verified edge.
	assert.Equal(t, 1, vs.StaleEvidence, "stale_evidence must count the stale verified edge")

	// The note must mention staleness.
	assert.Contains(t, vs.Note, "stale runtime evidence", "note must mention stale evidence")
}

func TestBuildVerificationSummaryAt_FreshEdgeNotFlagged(t *testing.T) {
	now := time.Unix(2000*86400, 0)
	freshTS := now.Unix() - 2*86400 // 2 days old, within 30d threshold

	edges := []graph.Edge{
		{
			ID:                "e1",
			VerificationState: graph.StateVerified,
			Sources: []graph.SourceRef{
				{Provider: "runtime", Ref: "sess/trace1", ObservedAt: freshTS},
			},
		},
	}

	staleAfter := 30 * 24 * time.Hour
	vs := graph.BuildVerificationSummaryAt(edges, staleAfter, now)

	assert.Equal(t, 1, vs.Verified)
	assert.Equal(t, 0, vs.StaleEvidence, "fresh edge must not be counted as stale")
	assert.NotContains(t, vs.Note, "stale")
}

func TestBuildVerificationSummaryAt_ZeroThresholdNoStaleCheck(t *testing.T) {
	now := time.Unix(2000*86400, 0)
	oldTS := now.Unix() - 365*86400 // 1 year old

	edges := []graph.Edge{
		{
			ID:                "e1",
			VerificationState: graph.StateVerified,
			Sources: []graph.SourceRef{
				{Provider: "runtime", Ref: "sess/trace1", ObservedAt: oldTS},
			},
		},
	}

	// staleAfter=0 means no stale check (backward compat path).
	vs := graph.BuildVerificationSummaryAt(edges, 0, now)
	assert.Equal(t, 0, vs.StaleEvidence, "zero threshold must not flag anything")
}

func TestBuildVerificationSummaryAt_MixedSourcesOnlyStaleWhenAllRuntimeStale(t *testing.T) {
	now := time.Unix(2000*86400, 0)
	staleTS := now.Unix() - 43*86400
	freshTS := now.Unix() - 2*86400

	edges := []graph.Edge{
		{
			ID:                "e1",
			VerificationState: graph.StateVerified,
			Sources: []graph.SourceRef{
				{Provider: "runtime", Ref: "sess/trace1", ObservedAt: staleTS},
				{Provider: "runtime", Ref: "sess/trace2", ObservedAt: freshTS},
			},
		},
	}

	staleAfter := 30 * 24 * time.Hour
	vs := graph.BuildVerificationSummaryAt(edges, staleAfter, now)
	// Not all runtime sources are stale, so the edge should not be counted.
	assert.Equal(t, 0, vs.StaleEvidence, "edge with a fresh runtime source must not be counted as stale")
}

func TestBuildVerificationSummaryAt_Determinism(t *testing.T) {
	now := time.Unix(2000*86400, 0)
	staleTS := now.Unix() - 43*86400

	edges := []graph.Edge{
		{ID: "e1", VerificationState: graph.StateVerified,
			Sources: []graph.SourceRef{{Provider: "runtime", Ref: "s/t", ObservedAt: staleTS}}},
		{ID: "e2", VerificationState: graph.StateCandidate},
		{ID: "e3", VerificationState: graph.StateObservedOnlyGap},
	}
	staleAfter := 30 * 24 * time.Hour

	a := graph.BuildVerificationSummaryAt(edges, staleAfter, now)
	b := graph.BuildVerificationSummaryAt(edges, staleAfter, now)
	assert.Equal(t, a, b, "two runs on the same input must produce identical output (rule 2)")
}
