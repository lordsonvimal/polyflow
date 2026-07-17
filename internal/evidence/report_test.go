package evidence_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/evidence"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// helpers

func makeEdge(id, kind, label, from, to, state string, providers ...string) graph.Edge {
	var sources []graph.SourceRef
	for _, p := range providers {
		sources = append(sources, graph.SourceRef{Provider: p, Confidence: graph.ConfidenceStatic})
	}
	return graph.Edge{
		ID: id, Type: graph.EdgeType(kind), Label: label,
		From: from, To: to,
		VerificationState: state,
		Sources:           sources,
	}
}

// --- BuildReport ---

func TestBuildReport_EmptyEdges(t *testing.T) {
	r := evidence.BuildReport(nil)
	assert.Equal(t, 0, r.TotalEdges)
	assert.Equal(t, 0.0, r.VerifiedPct)
	assert.Empty(t, r.ByKind)
	assert.Empty(t, r.CandidateList)
	assert.Empty(t, r.GapList)
	assert.Empty(t, r.ConflictingList)
}

func TestBuildReport_CountsByState(t *testing.T) {
	edges := []graph.Edge{
		makeEdge("e1", "http_call", "GET /a", "svc-a", "svc-b", graph.StateVerified, "static", "runtime"),
		makeEdge("e2", "http_call", "GET /b", "svc-a", "svc-b", graph.StateCandidate, "static"),
		makeEdge("e3", "http_call", "GET /c", "svc-x", "svc-y", graph.StateObservedOnlyGap, "runtime"),
		makeEdge("e4", "http_call", "GET /d", "svc-p", "svc-q", graph.StateConflicting, "runtime", "contract"),
	}

	r := evidence.BuildReport(edges)

	// TotalEdges = verified + candidate (gap/conflicting have no static anchor)
	assert.Equal(t, 2, r.TotalEdges)
	assert.Equal(t, 1, r.VerifiedEdges)
	assert.Equal(t, 1, r.CandidateEdges)
	assert.Equal(t, 1, r.GapEdges)
	assert.Equal(t, 1, r.ConflictingEdges)
	assert.InDelta(t, 50.0, r.VerifiedPct, 0.1)
}

func TestBuildReport_ByKind(t *testing.T) {
	edges := []graph.Edge{
		makeEdge("e1", "http_call", "GET /a", "a", "b", graph.StateVerified, "static", "contract"),
		makeEdge("e2", "http_call", "GET /b", "a", "b", graph.StateCandidate, "static"),
		makeEdge("e3", "publishes", "orders", "a", "b", graph.StateVerified, "static", "runtime"),
		makeEdge("e4", "publishes", "events", "x", "y", graph.StateObservedOnlyGap, "runtime"),
	}

	r := evidence.BuildReport(edges)
	require.Len(t, r.ByKind, 2)

	// Sorted by Kind: http_call before publishes
	assert.Equal(t, "http_call", r.ByKind[0].Kind)
	assert.Equal(t, 2, r.ByKind[0].Total)
	assert.Equal(t, 1, r.ByKind[0].Verified)
	assert.Equal(t, 1, r.ByKind[0].Candidate)
	assert.Equal(t, 0, r.ByKind[0].Gap)
	assert.InDelta(t, 50.0, r.ByKind[0].Pct, 0.1)

	assert.Equal(t, "publishes", r.ByKind[1].Kind)
	assert.Equal(t, 1, r.ByKind[1].Total) // gap excluded from Total
	assert.Equal(t, 1, r.ByKind[1].Verified)
	assert.Equal(t, 1, r.ByKind[1].Gap)
	assert.InDelta(t, 100.0, r.ByKind[1].Pct, 0.1)
}

func TestBuildReport_ListsSortedDeterministically(t *testing.T) {
	edges := []graph.Edge{
		makeEdge("e3", "http_call", "GET /z", "z", "z", graph.StateCandidate, "static"),
		makeEdge("e1", "http_call", "GET /a", "a", "a", graph.StateCandidate, "static"),
		makeEdge("e2", "http_call", "GET /m", "m", "m", graph.StateObservedOnlyGap, "runtime"),
	}

	r := evidence.BuildReport(edges)
	require.Len(t, r.CandidateList, 2)
	assert.Equal(t, "GET /a", r.CandidateList[0].Key) // sorted by Key
	assert.Equal(t, "GET /z", r.CandidateList[1].Key)
}

func TestBuildReport_GapListSources(t *testing.T) {
	e := makeEdge("e1", "http_call", "GET /x", "svc-a", "svc-b", graph.StateObservedOnlyGap, "runtime", "contract")
	r := evidence.BuildReport([]graph.Edge{e})
	require.Len(t, r.GapList, 1)
	// Sources must be sorted (contract before runtime alphabetically)
	assert.Equal(t, []string{"contract", "runtime"}, r.GapList[0].Sources)
}

// --- Determinism (bug-class rule 2) ---

// TestBuildReport_Determinism runs BuildReport twice on the same input and
// requires byte-identical JSON output.
func TestBuildReport_Determinism(t *testing.T) {
	edges := []graph.Edge{
		makeEdge("e1", "http_call", "GET /a", "svc-a", "svc-b", graph.StateVerified, "static", "contract"),
		makeEdge("e2", "http_call", "GET /b", "svc-a", "svc-c", graph.StateCandidate, "static"),
		makeEdge("e3", "publishes", "orders", "svc-a", "svc-b", graph.StateObservedOnlyGap, "runtime"),
		makeEdge("e4", "publishes", "events", "svc-x", "svc-y", graph.StateConflicting, "contract", "runtime"),
		makeEdge("e5", "http_call", "GET /z", "svc-z", "svc-w", graph.StateVerified, "static", "runtime"),
	}

	run := func() []byte {
		r := evidence.BuildReport(edges)
		b, err := json.Marshal(r)
		require.NoError(t, err)
		return b
	}

	first := run()
	second := run()
	assert.Equal(t, first, second, "BuildReport must be byte-identical across two runs on same input")
}

// --- ProposeRules ---

func TestProposeRules_Empty(t *testing.T) {
	proposals := evidence.ProposeRules(nil)
	assert.Empty(t, proposals)
}

func TestProposeRules_FilenamesFromClusterKey(t *testing.T) {
	gaps := []evidence.EdgeSummary{
		{Kind: "http_call", Key: "GET /api/users", From: "frontend", To: "backend", Sources: []string{"runtime"}},
		{Kind: "publishes", Key: "orders.created", From: "order-svc", To: "notify-svc", Sources: []string{"contract"}},
	}

	proposals := evidence.ProposeRules(gaps)
	require.Len(t, proposals, 2)

	// Sorted by Filename
	assert.Less(t, proposals[0].Filename, proposals[1].Filename)

	// Filenames derive from (kind, key), not from counters
	for _, p := range proposals {
		assert.NotEmpty(t, p.Filename)
		assert.True(t, len(p.Filename) > 5)
		assert.Contains(t, p.Filename, ".yaml")
		// Must not contain an emission counter pattern like "-0001" or "-1"
		assert.NotContains(t, p.Filename, "0001")
	}
}

func TestProposeRules_MultipleGapsSameChannel_OneProposal(t *testing.T) {
	// Two gap edges with the same (Kind, Key) but different (From, To) produce
	// ONE proposal (they represent the same channel observed from two sides).
	gaps := []evidence.EdgeSummary{
		{Kind: "http_call", Key: "GET /api/users", From: "svc-a", Sources: []string{"runtime"}},
		{Kind: "http_call", Key: "GET /api/users", From: "svc-b", Sources: []string{"contract"}},
	}

	proposals := evidence.ProposeRules(gaps)
	assert.Len(t, proposals, 1, "same (kind,key) must produce exactly one proposed rule")
	// The merged proposal must carry both providers
	assert.Contains(t, proposals[0].Content, "runtime")
	assert.Contains(t, proposals[0].Content, "contract")
}

// TestProposeRules_Determinism runs ProposeRules twice and requires byte-identical output.
func TestProposeRules_Determinism(t *testing.T) {
	gaps := []evidence.EdgeSummary{
		{Kind: "http_call", Key: "GET /api/users", From: "frontend", To: "backend", Sources: []string{"runtime"}},
		{Kind: "http_call", Key: "POST /api/orders", From: "web", To: "orders", Sources: []string{"contract", "runtime"}},
		{Kind: "publishes", Key: "user.created", From: "users", To: "notify", Sources: []string{"runtime"}},
	}

	run := func() []byte {
		proposals := evidence.ProposeRules(gaps)
		b, err := json.Marshal(proposals)
		require.NoError(t, err)
		return b
	}

	first := run()
	second := run()
	assert.Equal(t, first, second, "ProposeRules must be byte-identical across two runs on same input")
}

func TestProposeRules_ContentContainsKindAndKey(t *testing.T) {
	gaps := []evidence.EdgeSummary{
		{Kind: "http_call", Key: "GET /health", From: "lb", To: "api", Sources: []string{"runtime"}},
	}

	proposals := evidence.ProposeRules(gaps)
	require.Len(t, proposals, 1)
	assert.Contains(t, proposals[0].Content, "http_call")
	assert.Contains(t, proposals[0].Content, "GET /health")
	assert.Contains(t, proposals[0].Content, "proposed: true")
}

// --- Conflict detection integration ---

// TestReconcilerConflictDetection verifies that a gap edge on a channel that is
// already verified by static+confirming evidence is promoted to conflicting (F.4).
// This is the "runtime shows an edge static proved covered but missed" scenario.
func TestReconcilerConflictDetection(t *testing.T) {
	// Static edge: A→B on channel "GET /api/users" confirmed by contract → verified.
	nodes := []graph.Node{
		{ID: "a", Service: "svc-a", File: "a.go", Line: 1},
		{ID: "b", Service: "svc-b", File: "b.go", Line: 2},
	}
	staticEdge := graph.Edge{
		ID: "e-static", From: "a", To: "b",
		Type: graph.EdgeTypeHTTPCall, Label: "GET /api/users",
		Confidence: graph.ConfidenceStatic,
	}

	// Contract confirms A→B — this makes e-static verified.
	contractConfirm := graph.Edge{
		ID: "c1", From: "svc-a", To: "svc-b",
		Type: graph.EdgeTypeHTTPCall, Label: "GET /api/users",
		Sources: []graph.SourceRef{{Provider: "contract", Confidence: graph.ConfidenceDeclared}},
	}

	// Runtime sees a DIFFERENT service pair (C→D) on the same channel.
	runtimeGap := graph.Edge{
		ID: "r1", From: "svc-c", To: "svc-d",
		Type: graph.EdgeTypeHTTPCall, Label: "GET /api/users",
		Sources: []graph.SourceRef{{Provider: "runtime", Confidence: graph.ConfidenceObserved}},
	}

	sp := evidence.NewStaticProvider(nodes, []graph.Edge{staticEdge}, nil)
	contractProv := &fakeProvider{name: "contract", ev: evidence.Evidence{Edges: []graph.Edge{contractConfirm}}}
	runtimeProv := &fakeProvider{name: "runtime", ev: evidence.Evidence{Edges: []graph.Edge{runtimeGap}}}

	rec, err := evidence.NewReconciler(sp, contractProv, runtimeProv)
	require.NoError(t, err)
	result, err := rec.Reconcile(t.Context(), nil)
	require.NoError(t, err)

	byID := make(map[string]graph.Edge)
	for _, e := range result.Edges {
		byID[e.ID] = e
	}

	// The static edge should be verified (static + contract agree).
	require.Equal(t, graph.StateVerified, byID["e-static"].VerificationState,
		"static edge confirmed by contract must be verified")

	// The runtime gap edge on the same channel must be conflicting (F.4).
	var gapEdge *graph.Edge
	for i := range result.Edges {
		if result.Edges[i].VerificationState == graph.StateConflicting {
			e := result.Edges[i]
			gapEdge = &e
			break
		}
	}
	require.NotNil(t, gapEdge, "gap on a verified channel must surface as conflicting")
	assert.Equal(t, "GET /api/users", gapEdge.Label)
}

// TestReconcilerConflict_NoConflictWithoutVerified asserts that a gap edge on
// a non-verified channel stays as observed_only_gap (not conflicting).
func TestReconcilerConflict_NoConflictWithoutVerified(t *testing.T) {
	// No static edges at all — no verified channels.
	sp := evidence.NewStaticProvider(nil, nil, nil)
	runtimeGap := graph.Edge{
		ID: "r1", From: "svc-a", To: "svc-b",
		Type: graph.EdgeTypeHTTPCall, Label: "GET /api/users",
		Sources: []graph.SourceRef{{Provider: "runtime", Confidence: graph.ConfidenceObserved}},
	}
	runtimeProv := &fakeProvider{name: "runtime", ev: evidence.Evidence{Edges: []graph.Edge{runtimeGap}}}

	rec, err := evidence.NewReconciler(sp, runtimeProv)
	require.NoError(t, err)
	result, err := rec.Reconcile(t.Context(), nil)
	require.NoError(t, err)

	for _, e := range result.Edges {
		assert.NotEqual(t, graph.StateConflicting, e.VerificationState,
			"gap edge on an unverified channel must remain observed_only_gap, not conflicting")
		assert.Equal(t, graph.StateObservedOnlyGap, e.VerificationState)
	}
}
