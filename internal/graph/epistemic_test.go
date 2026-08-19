package graph_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestBuildEpistemic_Clean(t *testing.T) {
	e := graph.BuildEpistemic(nil, false, graph.VerificationSummary{}, graph.TrustStamp{Measured: true})
	assert.Equal(t, graph.EpistemicExact, e.Verdict)
	assert.Empty(t, e.Causes)
}

func TestBuildEpistemic_UnresolvedReference(t *testing.T) {
	unresolved := []graph.UnresolvedRef{{Kind: "call_ref"}}
	e := graph.BuildEpistemic(unresolved, false, graph.VerificationSummary{}, graph.TrustStamp{Measured: true})
	assert.Equal(t, graph.EpistemicLowerBound, e.Verdict)
	assert.Equal(t, []string{graph.CauseUnresolvedReference}, e.Causes)
}

func TestBuildEpistemic_DynamicDispatch(t *testing.T) {
	e := graph.BuildEpistemic(nil, true, graph.VerificationSummary{}, graph.TrustStamp{Measured: true})
	assert.Equal(t, graph.EpistemicLowerBound, e.Verdict)
	assert.Equal(t, []string{graph.CauseDynamicDispatch}, e.Causes)
}

func TestBuildEpistemic_CandidateEdge(t *testing.T) {
	vs := graph.VerificationSummary{Candidate: 1}
	e := graph.BuildEpistemic(nil, false, vs, graph.TrustStamp{Measured: true})
	assert.Equal(t, graph.EpistemicLowerBound, e.Verdict)
	assert.Equal(t, []string{graph.CauseCandidateEdge}, e.Causes)
}

func TestBuildEpistemic_ConflictingEvidence(t *testing.T) {
	vs := graph.VerificationSummary{Conflicting: 1}
	e := graph.BuildEpistemic(nil, false, vs, graph.TrustStamp{Measured: true})
	assert.Equal(t, graph.EpistemicLowerBound, e.Verdict)
	assert.Equal(t, []string{graph.CauseConflictingEvidence}, e.Causes)
}

func TestBuildEpistemic_UnmeasuredTrust(t *testing.T) {
	e := graph.BuildEpistemic(nil, false, graph.VerificationSummary{}, graph.TrustStamp{Measured: false})
	assert.Equal(t, graph.EpistemicLowerBound, e.Verdict)
	assert.Equal(t, []string{graph.CauseUnmeasuredTrust}, e.Causes)
}

func TestBuildEpistemic_StaleTrust(t *testing.T) {
	e := graph.BuildEpistemic(nil, false, graph.VerificationSummary{}, graph.TrustStamp{Measured: true, Stale: true})
	assert.Equal(t, graph.EpistemicLowerBound, e.Verdict)
	assert.Equal(t, []string{graph.CauseStaleTrust}, e.Causes)
}

func TestBuildEpistemic_AllCausesTogetherSorted(t *testing.T) {
	unresolved := []graph.UnresolvedRef{{Kind: "call_ref"}}
	vs := graph.VerificationSummary{Candidate: 1, Conflicting: 1}
	trust := graph.TrustStamp{Measured: false, Stale: true}
	e := graph.BuildEpistemic(unresolved, true, vs, trust)
	assert.Equal(t, graph.EpistemicLowerBound, e.Verdict)
	assert.Equal(t, []string{
		graph.CauseCandidateEdge,
		graph.CauseConflictingEvidence,
		graph.CauseDynamicDispatch,
		graph.CauseStaleTrust,
		graph.CauseUnmeasuredTrust,
		graph.CauseUnresolvedReference,
	}, e.Causes, "causes must be sorted for deterministic output (rule 2)")
}

func TestBuildEpistemic_Deterministic(t *testing.T) {
	unresolved := []graph.UnresolvedRef{{Kind: "call_ref"}}
	vs := graph.VerificationSummary{Candidate: 1, Conflicting: 1}
	trust := graph.TrustStamp{Measured: false, Stale: true}
	e1 := graph.BuildEpistemic(unresolved, true, vs, trust)
	e2 := graph.BuildEpistemic(unresolved, true, vs, trust)
	assert.Equal(t, e1.Causes, e2.Causes, "same inputs must produce byte-identical Causes order")
}

func TestHasWeakConfidence(t *testing.T) {
	assert.False(t, graph.HasWeakConfidence(nil))
	assert.False(t, graph.HasWeakConfidence([]string{graph.ConfidenceStatic, graph.ConfidenceInferred}))
	assert.True(t, graph.HasWeakConfidence([]string{graph.ConfidenceStatic, graph.ConfidencePartial}))
	assert.True(t, graph.HasWeakConfidence([]string{graph.ConfidenceUnknown}))
}
