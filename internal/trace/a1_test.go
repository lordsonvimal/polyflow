package trace

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// provenanceGraph builds a three-node chain with F.0 provenance on each edge.
//
//	a --http_call(verified, runtime:s1/t1, static:a.go:1)--> b
//	b --calls(candidate, static:b.go:5)--> c
func provenanceGraph() *graph.AdjacencyIndex {
	return buildIdx(
		[]graph.Node{
			{ID: "a", Label: "A", Service: "frontend", Type: graph.NodeTypeHTTPClient, File: "a.go", Line: 1},
			{ID: "b", Label: "B", Service: "backend", Type: graph.NodeTypeHTTPHandler, File: "b.go", Line: 10},
			{ID: "c", Label: "C", Service: "backend", Type: graph.NodeTypeFunction, File: "c.go", Line: 5},
		},
		[]graph.Edge{
			{
				ID: "e1", From: "a", To: "b",
				Type:                graph.EdgeTypeHTTPCall,
				Confidence:          graph.ConfidenceStatic,
				VerificationState:   graph.StateVerified,
				VerifiedGranularity: graph.GranularityChannel,
				Sources: []graph.SourceRef{
					{Provider: "runtime", Confidence: graph.ConfidenceObserved, Ref: "s1/t1"},
					{Provider: "static", Confidence: graph.ConfidenceStatic, Ref: "a.go:1"},
				},
			},
			{
				ID: "e2", From: "b", To: "c",
				Type:                graph.EdgeTypeCalls,
				Confidence:          graph.ConfidenceStatic,
				VerificationState:   graph.StateCandidate,
				VerifiedGranularity: graph.GranularityChannel,
				Sources: []graph.SourceRef{
					{Provider: "static", Confidence: graph.ConfidenceStatic, Ref: "b.go:5"},
				},
			},
		},
	)
}

// TestTrace_VerificationSummaryPopulated verifies the summary accumulates
// all edges traversed during chain enumeration.
func TestTrace_VerificationSummaryPopulated(t *testing.T) {
	r := Run(provenanceGraph(), "a", "forward", 0, false, 0, nil, 0)
	require.NotNil(t, r)
	assert.Equal(t, 1, r.VerificationSummary.Verified)
	assert.Equal(t, 1, r.VerificationSummary.Candidate)
	assert.Contains(t, r.VerificationSummary.Note, "candidate")
}

// TestTrace_VerificationSummaryEmptyWhenNoFusedEdges checks all-zero struct
// on a graph with no VerificationState on edges.
func TestTrace_VerificationSummaryEmptyWhenNoFusedEdges(t *testing.T) {
	r := Run(linearGraph(), "a", "forward", 0, false, 0, nil, 0)
	require.NotNil(t, r)
	assert.Equal(t, graph.VerificationSummary{}, r.VerificationSummary)
}

// TestTrace_VerificationSummaryPresentInJSON verifies {}-never-absent.
func TestTrace_VerificationSummaryPresentInJSON(t *testing.T) {
	r := Run(linearGraph(), "a", "forward", 0, false, 0, nil, 0)
	require.NotNil(t, r)
	data, err := json.Marshal(r)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"verification_summary"`)
}

// TestTrace_HopProvenance verifies per-Hop verification fields are populated.
func TestTrace_HopProvenance(t *testing.T) {
	r := Run(provenanceGraph(), "a", "forward", 0, false, 0, nil, 0)
	require.NotNil(t, r)
	require.Len(t, r.Chains, 1)

	hops := r.Chains[0].Hops
	// Hops: [A, B, C] — root has no Via edge, B has e1 (verified), C has e2 (candidate).
	var hopB, hopC *Hop
	for i := range hops {
		switch hops[i].ID {
		case "b":
			hopB = &hops[i]
		case "c":
			hopC = &hops[i]
		}
	}
	require.NotNil(t, hopB, "hop B must exist")
	assert.Equal(t, graph.StateVerified, hopB.VerificationState)
	assert.Equal(t, graph.GranularityChannel, hopB.VerifiedGranularity)
	assert.NotNil(t, hopB.Sources)

	require.NotNil(t, hopC, "hop C must exist")
	assert.Equal(t, graph.StateCandidate, hopC.VerificationState)
}

// TestTrace_SourcesCompactFormat verifies the "provider:ref" compact encoding.
func TestTrace_SourcesCompactFormat(t *testing.T) {
	r := Run(provenanceGraph(), "a", "forward", 0, false, 0, nil, 0)
	require.NotNil(t, r)
	require.Len(t, r.Chains, 1)

	for i := range r.Chains[0].Hops {
		if r.Chains[0].Hops[i].ID == "b" {
			var srcs []string
			require.NoError(t, json.Unmarshal(r.Chains[0].Hops[i].Sources, &srcs))
			require.Len(t, srcs, 2)
			// sorted by (provider, ref): runtime < static
			assert.Equal(t, "runtime:s1/t1", srcs[0])
			assert.Equal(t, "static:a.go:1", srcs[1])
			return
		}
	}
	t.Fatal("hop B not found")
}

// TestTrace_SourcesVerboseFormat verifies full SourceRef structs.
func TestTrace_SourcesVerboseFormat(t *testing.T) {
	r := Run(provenanceGraph(), "a", "forward", 0, true, 0, nil, 0)
	require.NotNil(t, r)
	require.Len(t, r.Chains, 1)

	for i := range r.Chains[0].Hops {
		if r.Chains[0].Hops[i].ID == "b" {
			var refs []graph.SourceRef
			require.NoError(t, json.Unmarshal(r.Chains[0].Hops[i].Sources, &refs))
			require.Len(t, refs, 2)
			assert.Equal(t, "runtime", refs[0].Provider)
			assert.Equal(t, "static", refs[1].Provider)
			return
		}
	}
	t.Fatal("hop B not found")
}

// TestTrace_DeterministicOutput verifies two runs produce identical JSON (rule 2).
func TestTrace_DeterministicOutput(t *testing.T) {
	idx := provenanceGraph()
	run := func() string {
		r := Run(idx, "a", "forward", 0, false, 0, nil, 0)
		data, err := json.Marshal(r)
		require.NoError(t, err)
		return string(data)
	}
	assert.Equal(t, run(), run())
}

// TestFinalizeEpistemic_Exact verifies a clean, fully-verified, measured
// result reports epistemic.verdict "exact" with no causes (EE.0).
func TestFinalizeEpistemic_Exact(t *testing.T) {
	r := Run(linearGraph(), "a", "forward", 0, false, 0, nil, 0)
	require.NotNil(t, r)
	r.Trust = graph.TrustStamp{Measured: true}
	r.AttachUnresolved(nil)
	r.FinalizeEpistemic()
	assert.Equal(t, graph.EpistemicExact, r.Epistemic.Verdict)
	assert.Empty(t, r.Epistemic.Causes)
}

// TestFinalizeEpistemic_CandidateEdge verifies r.Epistemic reflects this
// result's own VerificationSummary.Candidate, not a recomputation.
func TestFinalizeEpistemic_CandidateEdge(t *testing.T) {
	r := Run(provenanceGraph(), "a", "forward", 0, false, 0, nil, 0)
	require.NotNil(t, r)
	r.Trust = graph.TrustStamp{Measured: true}
	r.AttachUnresolved(nil)
	r.FinalizeEpistemic()
	assert.Equal(t, graph.EpistemicLowerBound, r.Epistemic.Verdict)
	assert.Equal(t, []string{graph.CauseCandidateEdge}, r.Epistemic.Causes)
}

// TestFinalizeEpistemic_DynamicDispatch verifies a partial/unknown confidence
// hop surfaces as the dynamic_dispatch cause.
func TestFinalizeEpistemic_DynamicDispatch(t *testing.T) {
	idx := buildIdx(
		[]graph.Node{
			{ID: "a", Label: "A", Type: graph.NodeTypeFunction, File: "a.go"},
			{ID: "b", Label: "B", Type: graph.NodeTypeFunction, File: "b.go"},
		},
		[]graph.Edge{
			{ID: "e1", From: "a", To: "b", Type: graph.EdgeTypeCalls, Confidence: graph.ConfidenceUnknown},
		},
	)
	r := Run(idx, "a", "forward", 0, false, 0, nil, 0)
	require.NotNil(t, r)
	r.Trust = graph.TrustStamp{Measured: true}
	r.AttachUnresolved(nil)
	r.FinalizeEpistemic()
	assert.Equal(t, graph.EpistemicLowerBound, r.Epistemic.Verdict)
	assert.Equal(t, []string{graph.CauseDynamicDispatch}, r.Epistemic.Causes)
}

// TestFinalizeEpistemic_PresentInJSON verifies {}-never-absent, mirroring
// TestTrace_VerificationSummaryPresentInJSON.
func TestFinalizeEpistemic_PresentInJSON(t *testing.T) {
	r := Run(linearGraph(), "a", "forward", 0, false, 0, nil, 0)
	require.NotNil(t, r)
	r.AttachUnresolved(nil)
	r.FinalizeEpistemic()
	data, err := json.Marshal(r)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"epistemic"`)
}

// TestCompact_EpistemicCarried verifies epistemic survives the compact wire
// shape, mirroring the other always-present sections in Compact's contract.
func TestCompact_EpistemicCarried(t *testing.T) {
	r := Run(provenanceGraph(), "a", "forward", 0, false, 0, nil, 0)
	require.NotNil(t, r)
	r.Trust = graph.TrustStamp{Measured: true}
	r.AttachUnresolved(nil)
	r.FinalizeEpistemic()
	c := r.Compact()
	assert.Equal(t, r.Epistemic, c.Epistemic)
}
