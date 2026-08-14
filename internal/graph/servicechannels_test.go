package graph_test

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reuses buildFlowsIndex's fixture (see flows_test.go): ch1 (svc-a) fans out
// to c1 (svc-b) and c2 (svc-c) via subscribes edges, so svc-a is the channel
// node's service (channel nodes inherit the publisher's service) and each
// subscribes edge already crosses the svc-a->svc-b / svc-a->svc-c pair
// directly — the same edges the overview's client-side aggregation groups
// into one pill per pair.
func TestServiceChannels_FanOut(t *testing.T) {
	idx := buildFlowsIndex()

	result, err := graph.ServiceChannels(idx, "svc-a", "svc-b")
	require.NoError(t, err)
	assert.Equal(t, "svc-a", result.From)
	assert.Equal(t, "svc-b", result.To)
	require.Len(t, result.Channels, 1)

	ch := result.Channels[0]
	assert.Equal(t, "subscribes", ch.Kind)
	assert.Equal(t, "orders created", ch.Channel)
	assert.Equal(t, "e-ch1-c1", ch.EdgeID)
	assert.Equal(t, "candidate", ch.VerificationState)
	assert.Equal(t, 1, ch.ProducerCount)
	assert.Equal(t, 1, ch.ConsumerCount)
}

func TestServiceChannels_NoCrossingEdges(t *testing.T) {
	idx := buildFlowsIndex()

	result, err := graph.ServiceChannels(idx, "svc-b", "svc-c")
	require.NoError(t, err)
	assert.Empty(t, result.Channels)
}

func TestServiceChannels_MissingParams(t *testing.T) {
	idx := buildFlowsIndex()

	_, err := graph.ServiceChannels(idx, "", "svc-b")
	assert.Error(t, err)

	_, err = graph.ServiceChannels(idx, "svc-a", "")
	assert.Error(t, err)
}

// Two producer edges into the same channel identity (from distinct producer
// nodes) must dedupe into one channel entry with producer_count reflecting
// both — rule 1's fan-out, mirrored on the producer side this time.
func TestServiceChannels_MultipleProducersSameChannel(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "p1", Type: graph.NodeTypeFunction, Label: "p1", Service: "svc-a"})
	idx.AddNode(&graph.Node{ID: "p2", Type: graph.NodeTypeFunction, Label: "p2", Service: "svc-a"})
	idx.AddNode(&graph.Node{ID: "h1", Type: graph.NodeTypeHTTPHandler, Label: "h1", Service: "svc-b"})
	idx.AddEdge(&graph.Edge{ID: "e1", From: "p1", To: "h1", Type: graph.EdgeTypeHTTPCall, Label: "GET /x", VerificationState: graph.StateVerified})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "p2", To: "h1", Type: graph.EdgeTypeHTTPCall, Label: "GET /x", VerificationState: graph.StateConflicting})

	result, err := graph.ServiceChannels(idx, "svc-a", "svc-b")
	require.NoError(t, err)
	require.Len(t, result.Channels, 1)
	ch := result.Channels[0]
	assert.Equal(t, "GET /x", ch.Channel)
	assert.Equal(t, "e1", ch.EdgeID)
	assert.Equal(t, "conflicting", ch.VerificationState)
	assert.Equal(t, 2, ch.ProducerCount)
	assert.Equal(t, 1, ch.ConsumerCount)
}
