package trace_ingest

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

func covEdge(id, edgeType, label, from, to, state string) graph.Edge {
	return graph.Edge{
		ID:                id,
		Type:              graph.EdgeType(edgeType),
		Label:             label,
		From:              from,
		To:                to,
		VerificationState: state,
	}
}

func covFlow(kind, key, from, to string) FlowRecord {
	return FlowRecord{Kind: contract.Kind(kind), Key: key, FromService: from, ToService: to}
}

// ─── TestComputeCoverage_Basic ────────────────────────────────────────────────
//
// Cumulative mode: edges with pre-stamped states are tallied correctly.
func TestComputeCoverage_Basic(t *testing.T) {
	edges := []graph.Edge{
		covEdge("e1", "http_call", "get /a", "web", "api", graph.StateVerified),
		covEdge("e2", "http_call", "get /b", "web", "api", graph.StateCandidate),
		covEdge("e3", "http_call", "get /c", "web", "api", graph.StateCandidate),
		covEdge("e4", "publishes", "orders", "svc", "queue", graph.StateObservedOnlyGap),
	}
	ledger := []IngestLedgerEntry{
		{Reason: "unknown_service"},
		{Reason: "unknown_service"},
		{Reason: "no_route_or_path"},
	}

	r := ComputeCoverage(edges, ledger)

	assert.Equal(t, 3, r.TotalChannels)   // e1 + e2 + e3 (not gap)
	assert.Equal(t, 1, r.VerifiedChannels)
	assert.Equal(t, 2, r.CandidateChannels)
	assert.Equal(t, 1, r.GapChannels)

	require.Len(t, r.Rows, 2, "two kinds: http_call and publishes")
	httpRow := r.Rows[0]
	assert.Equal(t, "http_call", httpRow.Kind)
	assert.Equal(t, 3, httpRow.Total)
	assert.Equal(t, 1, httpRow.Verified)
	assert.Equal(t, 2, httpRow.Candidate)
	assert.Equal(t, 0, httpRow.Gap)
	assert.InDelta(t, 33.33, httpRow.Pct, 0.1)

	pubRow := r.Rows[1]
	assert.Equal(t, "publishes", pubRow.Kind)
	assert.Equal(t, 0, pubRow.Total)
	assert.Equal(t, 0, pubRow.Verified)
	assert.Equal(t, 1, pubRow.Gap)
	assert.Equal(t, 0.0, pubRow.Pct)

	assert.Equal(t, map[string]int{"unknown_service": 2, "no_route_or_path": 1}, r.LedgerByReason)

	require.Len(t, r.ObservedOnlyGaps, 1)
	assert.Equal(t, ObservedOnlyGap{Kind: "publishes", Key: "orders", From: "svc", To: "queue"}, r.ObservedOnlyGaps[0])
}

// ─── TestComputeSessionCoverage_Basic ────────────────────────────────────────
//
// Session mode: flow records are joined against the edge baseline.
func TestComputeSessionCoverage_Basic(t *testing.T) {
	edges := []graph.Edge{
		covEdge("e1", "http_call", "get /games/*", "web", "api", graph.StateCandidate),
		covEdge("e2", "http_call", "post /orders", "web", "api", graph.StateCandidate),
		covEdge("e3", "publishes", "events.orders", "api", "queue", graph.StateCandidate),
	}

	// Session observed "get /games/*" (http → http_call) only.
	flows := []FlowRecord{
		covFlow("http", "get /games/*", "web", "api"),
	}

	r := ComputeSessionCoverage(flows, edges, nil)

	assert.Equal(t, 3, r.TotalChannels)
	assert.Equal(t, 1, r.VerifiedChannels)  // get /games/* matched
	assert.Equal(t, 2, r.CandidateChannels) // post /orders + events.orders not matched
	assert.Equal(t, 0, r.GapChannels)
	assert.Empty(t, r.ObservedOnlyGaps)

	// Pct = 1/3 * 100 = 33.3% for http_call; publishes has 0%.
	// Kind rows sorted alphabetically.
	require.Len(t, r.Rows, 2)
	assert.Equal(t, "http_call", r.Rows[0].Kind)
	assert.InDelta(t, 50.0, r.Rows[0].Pct, 0.1) // 1/2 http_call edges
	assert.Equal(t, "publishes", r.Rows[1].Kind)
	assert.Equal(t, 0.0, r.Rows[1].Pct)
}

// ─── TestComputeSessionCoverage_ZeroChannels ─────────────────────────────────
//
// A session with no flow records reports 0% without downgrading any edge.
// The input edges are not modified (pure function).
func TestComputeSessionCoverage_ZeroChannels(t *testing.T) {
	edges := []graph.Edge{
		covEdge("e1", "http_call", "get /a", "web", "api", graph.StateVerified),
		covEdge("e2", "http_call", "get /b", "web", "api", graph.StateCandidate),
	}
	// Snapshot states before calling.
	statesBefore := []string{edges[0].VerificationState, edges[1].VerificationState}

	r := ComputeSessionCoverage(nil, edges, nil) // nil flows = cumulative mode

	// In cumulative mode with pre-stamped states: verified=1, candidate=1.
	assert.Equal(t, 1, r.VerifiedChannels)
	assert.Equal(t, 1, r.CandidateChannels)

	// Input edges must not be modified.
	assert.Equal(t, statesBefore[0], edges[0].VerificationState)
	assert.Equal(t, statesBefore[1], edges[1].VerificationState)
}

// TestComputeSessionCoverage_EmptyFlowsZeroPct verifies the spec requirement:
// a session covering 0 channels reports 0% for all kinds.
func TestComputeSessionCoverage_EmptyFlowsZeroPct(t *testing.T) {
	edges := []graph.Edge{
		covEdge("e1", "http_call", "get /a", "web", "api", graph.StateVerified),
		covEdge("e2", "http_call", "get /b", "web", "api", graph.StateCandidate),
	}

	r := ComputeSessionCoverage([]FlowRecord{}, edges, nil)

	assert.Equal(t, 0, r.VerifiedChannels, "0 flow records → 0 session-verified")
	assert.Equal(t, 2, r.CandidateChannels)
	assert.Equal(t, 2, r.TotalChannels)
	require.Len(t, r.Rows, 1)
	assert.Equal(t, 0.0, r.Rows[0].Pct)
}

// ─── TestComputeCoverage_DeterminismTwoRun ───────────────────────────────────
//
// Running ComputeCoverage twice on the same input must produce byte-identical
// JSON output (bug-class rule 2 — no map-order leakage).
func TestComputeCoverage_DeterminismTwoRun(t *testing.T) {
	edges := []graph.Edge{
		covEdge("e1", "http_call", "get /a", "w", "a", graph.StateVerified),
		covEdge("e2", "http_call", "get /b", "w", "a", graph.StateCandidate),
		covEdge("e3", "publishes", "topic.x", "a", "q", graph.StateObservedOnlyGap),
		covEdge("e4", "kafka_publish", "events", "b", "k", graph.StateCandidate),
		covEdge("e5", "http_call", "get /c", "w", "a", graph.StateVerified),
	}
	ledger := []IngestLedgerEntry{
		{Reason: "unknown_service"},
		{Reason: "no_route_or_path"},
	}

	r1 := ComputeCoverage(edges, ledger)
	r2 := ComputeCoverage(edges, ledger)

	j1, err := json.Marshal(r1)
	require.NoError(t, err)
	j2, err := json.Marshal(r2)
	require.NoError(t, err)
	assert.Equal(t, string(j1), string(j2), "two-run determinism: byte-identical JSON output")
}

// ─── TestComputeCoverage_RowsSortedByKind ────────────────────────────────────
//
// Rows are always sorted by Kind, independent of the order edges are added.
func TestComputeCoverage_RowsSortedByKind(t *testing.T) {
	// Deliberately insert in reverse-alphabetical order.
	edges := []graph.Edge{
		covEdge("e1", "publishes", "q", "a", "b", graph.StateCandidate),
		covEdge("e2", "nats_publish", "x", "a", "b", graph.StateCandidate),
		covEdge("e3", "kafka_publish", "t", "a", "b", graph.StateCandidate),
		covEdge("e4", "http_call", "get /a", "a", "b", graph.StateCandidate),
	}
	r := ComputeCoverage(edges, nil)

	require.Len(t, r.Rows, 4)
	kinds := make([]string, len(r.Rows))
	for i, row := range r.Rows {
		kinds[i] = row.Kind
	}
	assert.Equal(t, []string{"http_call", "kafka_publish", "nats_publish", "publishes"}, kinds)
}

// ─── TestComputeCoverage_LedgerSummary ───────────────────────────────────────
//
// Ledger entries are tallied correctly by reason, including empty ledger.
func TestComputeCoverage_LedgerSummary(t *testing.T) {
	ledger := []IngestLedgerEntry{
		{Reason: "unknown_service"},
		{Reason: "unknown_service"},
		{Reason: "malformed"},
		{Reason: "no_causality"},
	}
	r := ComputeCoverage(nil, ledger)
	assert.Equal(t, map[string]int{
		"unknown_service": 2,
		"malformed":       1,
		"no_causality":    1,
	}, r.LedgerByReason)

	// Empty ledger → empty map (not nil).
	r2 := ComputeCoverage(nil, nil)
	assert.NotNil(t, r2.LedgerByReason)
	assert.Empty(t, r2.LedgerByReason)
}

// ─── TestComputeCoverage_GapsSortedByKindKeyFromTo ───────────────────────────
//
// ObservedOnlyGaps are sorted by (Kind, Key, From, To) regardless of input order.
func TestComputeCoverage_GapsSortedByKindKeyFromTo(t *testing.T) {
	edges := []graph.Edge{
		covEdge("g3", "publishes", "z.topic", "svc-z", "q", graph.StateObservedOnlyGap),
		covEdge("g2", "http_call", "post /b", "web", "api", graph.StateObservedOnlyGap),
		covEdge("g1", "http_call", "get /a", "web", "api", graph.StateObservedOnlyGap),
	}
	r := ComputeCoverage(edges, nil)
	require.Len(t, r.ObservedOnlyGaps, 3)
	assert.Equal(t, "http_call", r.ObservedOnlyGaps[0].Kind)
	assert.Equal(t, "get /a", r.ObservedOnlyGaps[0].Key)
	assert.Equal(t, "http_call", r.ObservedOnlyGaps[1].Kind)
	assert.Equal(t, "post /b", r.ObservedOnlyGaps[1].Key)
	assert.Equal(t, "publishes", r.ObservedOnlyGaps[2].Kind)
}

// ─── TestComputeSessionCoverage_FanOut ───────────────────────────────────────
//
// Two static edges sharing the same (kind, key) are BOTH counted when a flow
// record matches — fan-out, never first-match (bug-class rule 1).
func TestComputeSessionCoverage_FanOut(t *testing.T) {
	edges := []graph.Edge{
		covEdge("e1", "http_call", "get /games/*", "web", "api", graph.StateCandidate),
		covEdge("e2", "http_call", "get /games/*", "mobile", "api", graph.StateCandidate),
	}
	flows := []FlowRecord{
		covFlow("http", "get /games/*", "web", "api"),
	}
	r := ComputeSessionCoverage(flows, edges, nil)
	// Both edges share the key and BOTH should be counted as session-verified.
	assert.Equal(t, 2, r.VerifiedChannels, "fan-out: both edges on same channel get verified")
	assert.Equal(t, 0, r.CandidateChannels)
}

// ─── TestComputeSessionCoverage_Determinism ──────────────────────────────────
//
// Running ComputeSessionCoverage twice on the same inputs yields byte-identical JSON.
func TestComputeSessionCoverage_Determinism(t *testing.T) {
	edges := []graph.Edge{
		covEdge("e1", "http_call", "get /a", "web", "api", graph.StateCandidate),
		covEdge("e2", "http_call", "get /b", "web", "api", graph.StateCandidate),
		covEdge("e3", "publishes", "topic", "svc", "q", graph.StateObservedOnlyGap),
	}
	flows := []FlowRecord{
		covFlow("http", "get /a", "web", "api"),
	}
	ledger := []IngestLedgerEntry{{Reason: "unknown_service"}}

	r1 := ComputeSessionCoverage(flows, edges, ledger)
	r2 := ComputeSessionCoverage(flows, edges, ledger)

	j1, err := json.Marshal(r1)
	require.NoError(t, err)
	j2, err := json.Marshal(r2)
	require.NoError(t, err)
	assert.Equal(t, string(j1), string(j2))
}

