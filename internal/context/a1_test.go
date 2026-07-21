package context_test

import (
	"encoding/json"
	"testing"

	ctx "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// provenanceIndex builds a graph where edges carry F.0 provenance.
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

// TestContext_VerificationSummaryPopulated verifies the summary accumulates
// both traversed edges for a debug (upstream+downstream) call.
func TestContext_VerificationSummaryPopulated(t *testing.T) {
	idx := provenanceIndex()
	r := ctx.Build(idx, "be:getUser", "debug", 5, false, 0)
	require.NotNil(t, r)

	// upstream traversal hits e1 (verified); downstream hits e2 (candidate).
	assert.Equal(t, 1, r.VerificationSummary.Verified)
	assert.Equal(t, 1, r.VerificationSummary.Candidate)
	assert.Contains(t, r.VerificationSummary.Note, "candidate")
}

// TestContext_VerificationSummaryPresentInJSON verifies {}-never-absent.
func TestContext_VerificationSummaryPresentInJSON(t *testing.T) {
	idx := fixtureIndex() // no fused edges
	r := ctx.Build(idx, "be:getUser", "debug", 5, false, 0)
	data, err := json.Marshal(r)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"verification_summary"`)
}

// TestContext_VerificationSummaryEmptyWhenNoFusedEdges checks all-zero struct.
func TestContext_VerificationSummaryEmptyWhenNoFusedEdges(t *testing.T) {
	idx := fixtureIndex()
	r := ctx.Build(idx, "be:getUser", "debug", 5, false, 0)
	assert.Equal(t, graph.VerificationSummary{}, r.VerificationSummary)
}

// TestContext_PerNodeProvenance verifies per-TraceNode verification fields.
func TestContext_PerNodeProvenance(t *testing.T) {
	idx := provenanceIndex()
	r := ctx.Build(idx, "be:getUser", "debug", 5, false, 0)
	require.NotEmpty(t, r.Upstream)

	var fetchUserNode *ctx.TraceNode
	for i := range r.Upstream {
		if r.Upstream[i].ID == "fe:fetchUser" {
			fetchUserNode = &r.Upstream[i]
			break
		}
	}
	require.NotNil(t, fetchUserNode, "fe:fetchUser must appear upstream")
	assert.Equal(t, graph.StateVerified, fetchUserNode.VerificationState)
	assert.Equal(t, graph.GranularityChannel, fetchUserNode.VerifiedGranularity)
	assert.NotNil(t, fetchUserNode.Sources)
}

// TestContext_SourcesCompactFormat verifies the compact "provider:ref" encoding.
func TestContext_SourcesCompactFormat(t *testing.T) {
	idx := provenanceIndex()
	r := ctx.Build(idx, "be:getUser", "debug", 5, false, 0)

	for _, n := range r.Upstream {
		if n.ID == "fe:fetchUser" {
			var srcs []string
			require.NoError(t, json.Unmarshal(n.Sources, &srcs))
			require.Len(t, srcs, 2)
			assert.Equal(t, "runtime:s1/t1", srcs[0])
			assert.Equal(t, "static:api.js:10", srcs[1])
			return
		}
	}
	t.Fatal("fe:fetchUser not found in upstream")
}

// TestContext_SourcesVerboseFormat verifies full SourceRef structs.
func TestContext_SourcesVerboseFormat(t *testing.T) {
	idx := provenanceIndex()
	r := ctx.Build(idx, "be:getUser", "debug", 5, true, 0)

	for _, n := range r.Upstream {
		if n.ID == "fe:fetchUser" {
			var refs []graph.SourceRef
			require.NoError(t, json.Unmarshal(n.Sources, &refs))
			require.Len(t, refs, 2)
			assert.Equal(t, "runtime", refs[0].Provider)
			assert.Equal(t, "static", refs[1].Provider)
			return
		}
	}
	t.Fatal("fe:fetchUser not found in upstream")
}

// TestContext_DeterministicOutput verifies rule 2: sources ordered by (provider,ref).
func TestContext_DeterministicOutput(t *testing.T) {
	idx := provenanceIndex()
	run := func() string {
		r := ctx.Build(idx, "be:getUser", "debug", 5, false, 0)
		data, err := json.Marshal(r)
		require.NoError(t, err)
		return string(data)
	}
	assert.Equal(t, run(), run())
}
