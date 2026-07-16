package contract_test

// Tests for multi-consumer fan-out and deterministic matching: every consumer
// sharing a matched key gets an edge (recall over precision), and the
// wildcard tier scans keys in node-input order so repeated runs produce the
// same edge set.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func edgeIDs(edges []graph.Edge) []string {
	ids := make([]string, len(edges))
	for i, e := range edges {
		ids[i] = e.ID
	}
	return ids
}

// Two services expose the same route and no target_service hint restricts the
// producer: both handlers must be linked, not just the first one indexed.
func TestEngine_FanOut_SharedKeyAcrossServices(t *testing.T) {
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "/health"),
		handler("h1", "svc-b", "GET", "/health"),
		handler("h2", "svc-c", "GET", "/health"),
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{httpRule("skip", contract.UnmatchedUnknownEdge)}, nil)

	require.Len(t, result.Edges, 2, "both same-key handlers get an edge")
	assert.ElementsMatch(t, []string{"link:c1->h1", "link:c1->h2"}, edgeIDs(result.Edges))
	for _, e := range result.Edges {
		assert.Equal(t, graph.ConfidenceStatic, e.Confidence)
	}
	assert.Empty(t, result.Unresolved, "a fanned-out producer is matched, not unresolved")
}

// Hub broadcast (empty key) must fan out to every subscriber in the service,
// not only the first one occupying the index slot (the G.2 limitation).
func TestEngine_FanOut_HubBroadcastAllSubscribers(t *testing.T) {
	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)

	nodes := []graph.Node{
		{ID: "svc:pub", Type: graph.NodeTypePublisher, Service: "svc",
			Meta: map[string]string{"pattern": "hub_broadcast_call"}},
		{ID: "svc:sub1", Type: graph.NodeTypeSubscriber, Service: "svc",
			Meta: map[string]string{"pattern": "hub_subscribe_call"}},
		{ID: "svc:sub2", Type: graph.NodeTypeSubscriber, Service: "svc",
			Meta: map[string]string{"pattern": "hub_subscribe_call"}},
		{ID: "svc:sub3", Type: graph.NodeTypeSubscriber, Service: "svc",
			Meta: map[string]string{"pattern": "hub_subscribe_call"}},
	}
	e := &contract.Engine{}
	result := e.Link(nodes, rules, nil)

	var hubEdges []graph.Edge
	for _, ed := range result.Edges {
		if ed.Type == graph.EdgeTypeHubBroadcast {
			hubEdges = append(hubEdges, ed)
		}
	}
	require.Len(t, hubEdges, 3, "broadcast links every subscriber")
	assert.ElementsMatch(t,
		[]string{"hub:svc:pub->svc:sub1", "hub:svc:pub->svc:sub2", "hub:svc:pub->svc:sub3"},
		edgeIDs(hubEdges))
}

// The wildcard tier must produce the same edges in the same order on every
// run (it previously iterated a Go map — random order). All wildcard-matching
// consumers are linked.
func TestEngine_WildcardTier_DeterministicFanOut(t *testing.T) {
	nodes := []graph.Node{
		client("c1", "svc-a", "GET", "/users/42"),
		handler("h1", "svc-b", "GET", "/users/:id"),
		handler("h2", "svc-c", "GET", "/users/{id}"),
	}
	rule := httpRule("skip", contract.UnmatchedUnknownEdge)

	e := &contract.Engine{}
	first := e.Link(nodes, []contract.Rule{rule}, nil)
	require.Len(t, first.Edges, 2, "every wildcard-matching handler is linked")

	for i := 0; i < 20; i++ {
		again := e.Link(nodes, []contract.Rule{rule}, nil)
		require.Equal(t, edgeIDs(first.Edges), edgeIDs(again.Edges),
			"edge set and order must be stable across runs")
	}
}

// key_candidates fan-out composes with consumer fan-out: each candidate that
// matches links every consumer sharing that key.
func TestEngine_FanOut_KeyCandidatesTimesConsumers(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{
				"method":         "GET",
				"key_candidates": `["/admin","/dashboard"]`,
			}},
		handler("h1", "svc-b", "GET", "/admin"),
		handler("h2", "svc-c", "GET", "/admin"),
		handler("h3", "svc-b", "GET", "/dashboard"),
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{httpRule("skip", contract.UnmatchedUnknownEdge)}, nil)

	require.Len(t, result.Edges, 3, "2 consumers for /admin + 1 for /dashboard")
	assert.ElementsMatch(t,
		[]string{"link:c1->h1", "link:c1->h2", "link:c1->h3"},
		edgeIDs(result.Edges))
	for _, ed := range result.Edges {
		assert.Equal(t, "branch_enum", ed.Meta["via"])
	}
}
