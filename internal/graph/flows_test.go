package graph_test

import (
	"encoding/json"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildFlowsIndex builds the UB.5 fixture:
//
//	svc-a: h1 (http_handler, entrypoint) -calls-> f1 (function) -publishes-> ch1 (channel)
//	       cb1 (function, meta.root_kind=callback — must land in Entrypoints' skipped, not the list)
//	svc-b: ch1 -subscribes-> c1 (subscriber) -calls-> f2 (function)
//	svc-c: ch1 -subscribes-> c2 (subscriber) -calls-> f3 (function)
//
// ch1 has one producer (f1) and two consumers (c1, c2) — the rule-1 fan-out
// case seam isolation must return both, never first-match.
//
// A separate diamond D0->D1->D3, D0->D2->D3 (all svc-a, `calls` edges) gives
// two equal-length paths for k-shortest-paths ranking, and an isolated node
// Iso1 gives an unreachable pair.
func buildFlowsIndex() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()

	idx.AddNode(&graph.Node{ID: "h1", Type: graph.NodeTypeHTTPHandler, Label: "CreateOrder", Service: "svc-a", File: "handlers.go", Line: 10, EndLine: 20, Meta: map[string]string{"method": "POST", "path": "/orders"}})
	idx.AddNode(&graph.Node{ID: "f1", Type: graph.NodeTypeFunction, Label: "validateOrder", Service: "svc-a", File: "orders.go", Line: 30})
	idx.AddNode(&graph.Node{ID: "ch1", Type: graph.NodeTypeChannel, Label: "orders.created", Service: "svc-a", Meta: map[string]string{"exchange": "orders", "routing_key": "created"}})
	idx.AddNode(&graph.Node{ID: "cb1", Type: graph.NodeTypeFunction, Label: "onRetry", Service: "svc-a", File: "retry.go", Line: 5, Meta: map[string]string{"root_kind": "callback"}})

	idx.AddNode(&graph.Node{ID: "c1", Type: graph.NodeTypeSubscriber, Label: "ConsumerOne", Service: "svc-b", File: "consumer1.go", Line: 8})
	idx.AddNode(&graph.Node{ID: "f2", Type: graph.NodeTypeFunction, Label: "processOrder", Service: "svc-b", File: "consumer1.go", Line: 20})

	idx.AddNode(&graph.Node{ID: "c2", Type: graph.NodeTypeSubscriber, Label: "ConsumerTwo", Service: "svc-c", File: "consumer2.go", Line: 8})
	idx.AddNode(&graph.Node{ID: "f3", Type: graph.NodeTypeFunction, Label: "logOrder", Service: "svc-c", File: "consumer2.go", Line: 20})

	idx.AddEdge(&graph.Edge{ID: "e-h1-f1", From: "h1", To: "f1", Type: graph.EdgeTypeCalls, Confidence: graph.ConfidenceStatic, VerificationState: graph.StateVerified})
	idx.AddEdge(&graph.Edge{ID: "e-f1-ch1", From: "f1", To: "ch1", Type: graph.EdgeTypePublishes, Confidence: graph.ConfidenceStatic, VerificationState: graph.StateVerified})
	idx.AddEdge(&graph.Edge{ID: "e-ch1-c1", From: "ch1", To: "c1", Type: graph.EdgeTypeSubscribes, Confidence: graph.ConfidenceStatic, VerificationState: graph.StateCandidate})
	idx.AddEdge(&graph.Edge{ID: "e-ch1-c2", From: "ch1", To: "c2", Type: graph.EdgeTypeSubscribes, Confidence: graph.ConfidenceStatic, VerificationState: graph.StateCandidate})
	idx.AddEdge(&graph.Edge{ID: "e-c1-f2", From: "c1", To: "f2", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e-c2-f3", From: "c2", To: "f3", Type: graph.EdgeTypeCalls})

	idx.AddNode(&graph.Node{ID: "D0", Type: graph.NodeTypeFunction, Label: "D0", Service: "svc-a", File: "diamond.go", Line: 1})
	idx.AddNode(&graph.Node{ID: "D1", Type: graph.NodeTypeFunction, Label: "D1", Service: "svc-a", File: "diamond.go", Line: 5})
	idx.AddNode(&graph.Node{ID: "D2", Type: graph.NodeTypeFunction, Label: "D2", Service: "svc-a", File: "diamond.go", Line: 10})
	idx.AddNode(&graph.Node{ID: "D3", Type: graph.NodeTypeFunction, Label: "D3", Service: "svc-a", File: "diamond.go", Line: 15})
	idx.AddEdge(&graph.Edge{ID: "e-d0-d1", From: "D0", To: "D1", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e-d0-d2", From: "D0", To: "D2", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e-d1-d3", From: "D1", To: "D3", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e-d2-d3", From: "D2", To: "D3", Type: graph.EdgeTypeCalls})

	idx.AddNode(&graph.Node{ID: "Iso1", Type: graph.NodeTypeFunction, Label: "Isolated", Service: "svc-a", File: "isolated.go", Line: 1})

	return idx
}

func TestEntrypoints_Structure(t *testing.T) {
	idx := buildFlowsIndex()
	result := graph.Entrypoints(idx, "", "")

	var ids []string
	for _, e := range result.Entrypoints {
		ids = append(ids, e.NodeID)
	}
	assert.Contains(t, ids, "h1")
	assert.Contains(t, ids, "c1")
	assert.Contains(t, ids, "c2")
	assert.NotContains(t, ids, "cb1", "callback-kind function must not appear as an entrypoint")
	assert.NotContains(t, ids, "f1")

	for _, e := range result.Entrypoints {
		if e.NodeID == "h1" {
			assert.Equal(t, "http_handler", e.Kind)
			assert.Equal(t, "POST /orders", e.Channel)
		}
		if e.NodeID == "c1" {
			assert.Equal(t, "subscriber", e.Kind)
			assert.Equal(t, "orders created", e.Channel)
		}
	}

	require.Len(t, result.Skipped, 1)
	assert.Equal(t, "callback", result.Skipped[0].Type)
	assert.Equal(t, 1, result.Skipped[0].Count)
}

func TestEntrypoints_KindFilter(t *testing.T) {
	idx := buildFlowsIndex()
	result := graph.Entrypoints(idx, "", "subscriber")
	require.Len(t, result.Entrypoints, 2)
	for _, e := range result.Entrypoints {
		assert.Equal(t, "subscriber", e.Kind)
	}
}

func TestEntrypoints_ServiceFilter(t *testing.T) {
	idx := buildFlowsIndex()
	result := graph.Entrypoints(idx, "svc-b", "")
	require.Len(t, result.Entrypoints, 1)
	assert.Equal(t, "c1", result.Entrypoints[0].NodeID)
}

func TestEntrypoints_Determinism(t *testing.T) {
	idx := buildFlowsIndex()
	r1 := graph.Entrypoints(idx, "", "")
	r2 := graph.Entrypoints(idx, "", "")
	b1, _ := json.Marshal(r1)
	b2, _ := json.Marshal(r2)
	assert.Equal(t, string(b1), string(b2))
}

func TestKShortestFlowPaths_Diamond(t *testing.T) {
	idx := buildFlowsIndex()
	result, err := graph.KShortestFlowPaths(idx, "D0", "D3", 5, 10)
	require.NoError(t, err)
	assert.True(t, result.Reachable)
	require.Len(t, result.Paths, 2)
	for _, p := range result.Paths {
		require.Len(t, p.Chain, 3)
		assert.Equal(t, "D0", p.Chain[0].NodeID)
		assert.Equal(t, "D3", p.Chain[2].NodeID)
	}
	// Ranked by lexical edge-id sequence: "e-d0-d1\x00e-d1-d3" < "e-d0-d2\x00e-d2-d3".
	assert.Equal(t, "D1", result.Paths[0].Chain[1].NodeID)
	assert.Equal(t, "D2", result.Paths[1].Chain[1].NodeID)
}

func TestKShortestFlowPaths_Unreachable(t *testing.T) {
	idx := buildFlowsIndex()
	result, err := graph.KShortestFlowPaths(idx, "D0", "Iso1", 5, 10)
	require.NoError(t, err)
	assert.False(t, result.Reachable)
	assert.Empty(t, result.Paths)
}

func TestKShortestFlowPaths_UnknownNode(t *testing.T) {
	idx := buildFlowsIndex()
	_, err := graph.KShortestFlowPaths(idx, "D0", "nope", 5, 10)
	assert.Error(t, err)
}

func TestKShortestFlowPaths_Determinism(t *testing.T) {
	idx := buildFlowsIndex()
	r1, _ := graph.KShortestFlowPaths(idx, "D0", "D3", 5, 10)
	r2, _ := graph.KShortestFlowPaths(idx, "D0", "D3", 5, 10)
	b1, _ := json.Marshal(r1)
	b2, _ := json.Marshal(r2)
	assert.Equal(t, string(b1), string(b2))
}

func TestRefineWaypoints_Happy(t *testing.T) {
	idx := buildFlowsIndex()
	result, err := graph.RefineWaypoints(idx, []string{"D0", "D1", "D3"}, "forward")
	require.NoError(t, err)
	require.Len(t, result.Chain, 3)
	assert.Equal(t, "D0", result.Chain[0].NodeID)
	assert.Equal(t, "D1", result.Chain[1].NodeID)
	assert.Equal(t, "D3", result.Chain[2].NodeID)
}

func TestRefineWaypoints_Disconnected(t *testing.T) {
	idx := buildFlowsIndex()
	_, err := graph.RefineWaypoints(idx, []string{"D0", "Iso1"}, "forward")
	assert.Error(t, err)
}

func TestRefineWaypoints_UnknownNode(t *testing.T) {
	idx := buildFlowsIndex()
	_, err := graph.RefineWaypoints(idx, []string{"D0", "nope"}, "forward")
	assert.Error(t, err)
}

func TestSeam_TwoConsumers(t *testing.T) {
	idx := buildFlowsIndex()
	result, err := graph.Seam(idx, "e-f1-ch1")
	require.NoError(t, err)

	require.Len(t, result.Producers, 1)
	assert.Equal(t, "f1", result.Producers[0].Node.ID)

	require.Len(t, result.Consumers, 2, "rule 1: both consumers on the shared channel must appear")
	var consumerIDs []string
	for _, c := range result.Consumers {
		consumerIDs = append(consumerIDs, c.Node.ID)
	}
	assert.Contains(t, consumerIDs, "c1")
	assert.Contains(t, consumerIDs, "c2")
}

func TestSeam_FromConsumerEdge(t *testing.T) {
	idx := buildFlowsIndex()
	result, err := graph.Seam(idx, "e-ch1-c1")
	require.NoError(t, err)
	require.Len(t, result.Consumers, 2)
	require.Len(t, result.Producers, 1)
}

func TestSeam_Expanded(t *testing.T) {
	idx := buildFlowsIndex()
	result, err := graph.Seam(idx, "e-f1-ch1")
	require.NoError(t, err)
	assert.True(t, result.Expanded, "channel_key/NodeTypeChannel path must report Expanded")
}

func TestSeam_NotExpanded_DirectEdge(t *testing.T) {
	idx := buildFlowsIndex()
	// e-h1-f1 is a plain `calls` edge between two functions — no channel
	// node, no channel_key — the default fallback case.
	result, err := graph.Seam(idx, "e-h1-f1")
	require.NoError(t, err)
	assert.False(t, result.Expanded, "a lone edge pair must report Expanded=false")
	assert.Len(t, result.Producers, 1)
	assert.Len(t, result.Consumers, 1)
}

func TestSeam_UnknownEdge(t *testing.T) {
	idx := buildFlowsIndex()
	_, err := graph.Seam(idx, "nope")
	assert.Error(t, err)
}

func TestSeam_Determinism(t *testing.T) {
	idx := buildFlowsIndex()
	r1, _ := graph.Seam(idx, "e-f1-ch1")
	r2, _ := graph.Seam(idx, "e-f1-ch1")
	b1, _ := json.Marshal(r1)
	b2, _ := json.Marshal(r2)
	assert.Equal(t, string(b1), string(b2))
}

func TestBuildStack_Basic(t *testing.T) {
	idx := buildFlowsIndex()
	deps := []*graph.Dependency{
		{Service: "svc-a", Ecosystem: "go", Name: "gin", Version: "1.9.0", Kind: "prod"},
	}
	stacks := graph.BuildStack(idx, deps)

	var svcA *graph.ServiceStack
	for i := range stacks {
		if stacks[i].Name == "svc-a" {
			svcA = &stacks[i]
		}
	}
	require.NotNil(t, svcA)
	require.Len(t, svcA.Deps, 1)
	assert.Equal(t, "gin", svcA.Deps[0].Name)
	assert.True(t, svcA.NodeCounts["function"] > 0)
	assert.True(t, svcA.Files > 0)
}

func TestFilterUnresolvedRefs(t *testing.T) {
	refs := []graph.UnresolvedRef{
		{Service: "svc-a", File: "a.go", Line: 1, Name: "Foo", Kind: "call_ref"},
		{Service: "svc-b", File: "b.go", Line: 2, Name: "Bar", Kind: "import_ref"},
	}
	got := graph.FilterUnresolvedRefs(refs, "svc-a", "", "")
	require.Len(t, got, 1)
	assert.Equal(t, "Foo", got[0].Name)

	got = graph.FilterUnresolvedRefs(refs, "", "import_ref", "")
	require.Len(t, got, 1)
	assert.Equal(t, "Bar", got[0].Name)

	got = graph.FilterUnresolvedRefs(refs, "", "", "foo")
	require.Len(t, got, 1)
	assert.Equal(t, "Foo", got[0].Name)
}
