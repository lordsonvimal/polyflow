package impact_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
)

// provenanceIndex builds a graph where the edges carry F.0 provenance.
//
//	frontend:fetchUser --http_call(verified, runtime:s1/t1)--> backend:getUser
//	backend:getUser --calls(candidate, static:handler.go:20)--> backend:queryDB
func provenanceIndex() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "fe:fetchUser", Type: graph.NodeTypeHTTPClient, Label: "fetchUser", Service: "frontend", File: "api.js", Line: 10})
	idx.AddNode(&graph.Node{ID: "be:getUser", Type: graph.NodeTypeHTTPHandler, Label: "GET /api/user", Service: "backend", File: "handler.go", Line: 20})
	idx.AddNode(&graph.Node{ID: "be:queryDB", Type: graph.NodeTypeFunction, Label: "queryDB", Service: "backend", File: "db.go", Line: 40})
	idx.AddEdge(&graph.Edge{
		ID: "e1", From: "fe:fetchUser", To: "be:getUser",
		Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceStatic,
		VerificationState:   graph.StateVerified,
		VerifiedGranularity: graph.GranularityChannel,
		Sources: []graph.SourceRef{
			{Provider: "runtime", Confidence: graph.ConfidenceObserved, Ref: "s1/t1"},
			{Provider: "static", Confidence: graph.ConfidenceStatic, Ref: "api.js:10"},
		},
	})
	idx.AddEdge(&graph.Edge{
		ID: "e2", From: "be:getUser", To: "be:queryDB",
		Type: graph.EdgeTypeCalls, Confidence: graph.ConfidenceStatic,
		VerificationState:   graph.StateCandidate,
		VerifiedGranularity: graph.GranularityChannel,
		Sources: []graph.SourceRef{
			{Provider: "static", Confidence: graph.ConfidenceStatic, Ref: "handler.go:20"},
		},
	})
	return idx
}

// TestBuild_VerificationSummaryPopulated verifies the top-level
// verification_summary is computed from traversed edges.
func TestBuild_VerificationSummaryPopulated(t *testing.T) {
	idx := provenanceIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], impact.Options{Depth: 10})

	// e1 (verified) and e2 (candidate) are in the blast radius
	assert.Equal(t, 1, out.VerificationSummary.Verified)
	assert.Equal(t, 1, out.VerificationSummary.Candidate)
	assert.Equal(t, 0, out.VerificationSummary.ObservedOnlyGap)
	assert.Equal(t, 0, out.VerificationSummary.Conflicting)
	assert.Contains(t, out.VerificationSummary.Note, "candidate")
}

// TestBuild_VerificationSummaryEmptyWhenNoFusedEdges verifies the summary is
// present but all-zero when no edges have verification state.
func TestBuild_VerificationSummaryEmptyWhenNoFusedEdges(t *testing.T) {
	idx := fixtureIndex() // edges have no VerificationState
	out := impact.Build(idx, idx.Nodes["be:queryDB"], impact.Options{Depth: 10})
	assert.Equal(t, graph.VerificationSummary{}, out.VerificationSummary)
}

// TestBuild_VerificationSummaryPresentInJSON verifies {}-never-absent.
func TestBuild_VerificationSummaryPresentInJSON(t *testing.T) {
	idx := fixtureIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], impact.Options{Depth: 10})
	data, err := json.Marshal(out)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"verification_summary"`)
}

// TestBuild_PerEdgeProvenance verifies per-caller verification fields are set.
func TestBuild_PerEdgeProvenance(t *testing.T) {
	idx := provenanceIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], impact.Options{Depth: 10})

	// be:getUser is depth 1; its via edge is e2 (candidate)
	require.NotEmpty(t, out.Callers)
	var getUserCaller *impact.Caller
	for i := range out.Callers {
		if out.Callers[i].ID == "be:getUser" {
			getUserCaller = &out.Callers[i]
			break
		}
	}
	require.NotNil(t, getUserCaller)
	assert.Equal(t, graph.StateCandidate, getUserCaller.VerificationState)
	assert.Equal(t, graph.GranularityChannel, getUserCaller.VerifiedGranularity)
	assert.NotNil(t, getUserCaller.Sources)
}

// TestBuild_SourcesCompactFormat verifies compact "provider:ref" encoding.
func TestBuild_SourcesCompactFormat(t *testing.T) {
	idx := provenanceIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], impact.Options{Depth: 10})

	var feCallerSources []string
	for _, c := range out.Callers {
		if c.ID == "fe:fetchUser" {
			require.NoError(t, json.Unmarshal(c.Sources, &feCallerSources))
			break
		}
	}
	// fe:fetchUser's via edge is e1 with two SourceRefs; sorted by (provider,ref)
	require.Len(t, feCallerSources, 2)
	assert.Equal(t, "runtime:s1/t1", feCallerSources[0])
	assert.Equal(t, "static:api.js:10", feCallerSources[1])
}

// TestBuild_SourcesVerboseFormat verifies full SourceRef structs under verbose.
func TestBuild_SourcesVerboseFormat(t *testing.T) {
	idx := provenanceIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], impact.Options{Depth: 10, VerboseSources: true}) // verboseSources

	for _, c := range out.Callers {
		if c.ID == "fe:fetchUser" {
			var refs []graph.SourceRef
			require.NoError(t, json.Unmarshal(c.Sources, &refs))
			require.Len(t, refs, 2)
			assert.Equal(t, "runtime", refs[0].Provider)
			assert.Equal(t, "static", refs[1].Provider)
			return
		}
	}
	t.Fatal("fe:fetchUser caller not found")
}

// TestSummarize_VerificationSummaryCarried verifies the summary survives rollup.
func TestSummarize_VerificationSummaryCarried(t *testing.T) {
	idx := provenanceIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], impact.Options{Depth: 10})
	s := out.Summarize()
	assert.Equal(t, out.VerificationSummary, s.VerificationSummary)
}

// TestBudgetFloor_VerificationSummaryAlwaysPresent verifies that a tiny
// max-tokens budget still carries both unresolved and verification_summary.
func TestBudgetFloor_VerificationSummaryAlwaysPresent(t *testing.T) {
	idx := provenanceIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], impact.Options{Depth: 10})
	out.AttachUnresolved([]graph.UnresolvedRef{
		{Service: "backend", File: "db.go", Line: 5, Name: "missingFn", Kind: "call_ref"},
	})

	// 1 token budget — forces rollup and trimming.
	budgeted := out.ApplyBudget(1, false)
	data, err := json.Marshal(budgeted)
	require.NoError(t, err)
	s := string(data)

	assert.Contains(t, s, `"unresolved"`, "unresolved must survive budget cut")
	assert.Contains(t, s, `"verification_summary"`, "verification_summary must survive budget cut")
}

// TestBuild_DeterministicOutput verifies two runs produce identical JSON (rule 2).
func TestBuild_DeterministicOutput(t *testing.T) {
	idx := provenanceIndex()
	run := func() string {
		out := impact.Build(idx, idx.Nodes["be:queryDB"], impact.Options{Depth: 10})
		data, err := json.Marshal(out)
		require.NoError(t, err)
		return string(data)
	}
	assert.Equal(t, run(), run())
}

// TestBudget_VerificationSummaryInSummaryShape verifies verification_summary is
// present in the file-rollup (Summary) shape too.
func TestBudget_VerificationSummaryInSummaryShape(t *testing.T) {
	idx := provenanceIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], impact.Options{Depth: 10})

	// forceSummary=true to get the Summary shape.
	budgeted := out.ApplyBudget(0, true)
	data, err := json.Marshal(budgeted)
	require.NoError(t, err)

	s := string(data)
	assert.Contains(t, s, `"verification_summary"`)
	assert.Contains(t, s, `"summary":true`)

	// Budget field is present (forceSummary always computes a budget entry).
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	_, hasBudget := raw["budget"]
	assert.True(t, hasBudget, "budget field present when forceSummary=true")
}

// TestBuild_TrustZeroStateInJSON verifies the trust field is always present
// (plan-14 T.0), even before a caller sets it — Build itself has no DB access
// so Trust is the zero TrustStamp{Measured:false} until the caller populates it.
func TestBuild_TrustZeroStateInJSON(t *testing.T) {
	idx := fixtureIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], impact.Options{Depth: 10})
	assert.Equal(t, graph.TrustStamp{Measured: false}, out.Trust)

	data, err := json.Marshal(out)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"trust":{"measured":false}`)
}

// TestBudgetFloor_TrustAlwaysPresent verifies a tiny max-tokens budget still
// carries trust — it must survive any token budget cut like verification_summary.
func TestBudgetFloor_TrustAlwaysPresent(t *testing.T) {
	idx := provenanceIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], impact.Options{Depth: 10})
	out.Trust = graph.TrustStamp{Measured: true, Corpus: "chessleap", Cases: 12, Recall: 1.0, MeasuredAt: "2026-07-19T10:31:00Z"}

	budgeted := out.ApplyBudget(1, false)
	data, err := json.Marshal(budgeted)
	require.NoError(t, err)
	s := string(data)

	assert.Contains(t, s, `"trust"`, "trust must survive budget cut")
	assert.Contains(t, s, `"chessleap"`)
}

// TestSummarize_TrustCarried verifies trust survives file rollup.
func TestSummarize_TrustCarried(t *testing.T) {
	idx := provenanceIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], impact.Options{Depth: 10})
	out.Trust = graph.TrustStamp{Measured: true, Corpus: "chessleap", Cases: 12, Recall: 1.0}
	s := out.Summarize()
	assert.Equal(t, out.Trust, s.Trust)
}
