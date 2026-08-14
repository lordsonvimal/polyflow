package graph_test

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLinkExplorerAdjacency_Downstream(t *testing.T) {
	idx := buildFlowsIndex()

	result := graph.LinkExplorerAdjacency(idx, "h1", "downstream", 1, 0, 0, "", "")
	require.Len(t, result.Rows, 1)
	assert.Equal(t, "f1", result.Rows[0].NodeID)
	assert.Equal(t, string(graph.EdgeTypeCalls), result.Rows[0].EdgeType)
	assert.Equal(t, 1, result.Rows[0].Depth)
	assert.Equal(t, 1, result.Total)
	assert.False(t, result.Truncated)
}

func TestLinkExplorerAdjacency_Upstream(t *testing.T) {
	idx := buildFlowsIndex()

	result := graph.LinkExplorerAdjacency(idx, "f1", "upstream", 1, 0, 0, "", "")
	require.Len(t, result.Rows, 1)
	assert.Equal(t, "h1", result.Rows[0].NodeID)
}

func TestLinkExplorerAdjacency_DepthGroupsWithViaPath(t *testing.T) {
	idx := buildFlowsIndex()

	result := graph.LinkExplorerAdjacency(idx, "h1", "downstream", 3, 0, 0, "", "")

	var byID = map[string]graph.LinkRow{}
	for _, r := range result.Rows {
		byID[r.NodeID] = r
	}

	require.Contains(t, byID, "f1")
	assert.Equal(t, 1, byID["f1"].Depth)
	assert.Empty(t, byID["f1"].Via)

	require.Contains(t, byID, "ch1")
	assert.Equal(t, 2, byID["ch1"].Depth)
	assert.Equal(t, []string{"validateOrder"}, byID["ch1"].Via)

	require.Contains(t, byID, "c1")
	assert.Equal(t, 3, byID["c1"].Depth)
	assert.Equal(t, []string{"validateOrder", "orders.created"}, byID["c1"].Via)
	assert.True(t, byID["c1"].CrossService, "svc-a -> svc-b crosses a service boundary")
}

func TestLinkExplorerAdjacency_KindAndServiceFilter(t *testing.T) {
	idx := buildFlowsIndex()

	result := graph.LinkExplorerAdjacency(idx, "h1", "downstream", 3, 0, 0, "channel", "")
	require.Len(t, result.Rows, 1)
	assert.Equal(t, "ch1", result.Rows[0].NodeID)

	result = graph.LinkExplorerAdjacency(idx, "h1", "downstream", 3, 0, 0, "", "svc-c")
	require.Len(t, result.Rows, 1)
	assert.Equal(t, "c2", result.Rows[0].NodeID)
}

func TestLinkExplorerAdjacency_Channel(t *testing.T) {
	idx := buildFlowsIndex()

	result := graph.LinkExplorerAdjacency(idx, "f1", "downstream", 1, 0, 0, "", "")
	require.Len(t, result.Rows, 1)
	assert.Equal(t, "orders created", result.Rows[0].Channel)
}

func TestLinkExplorerAdjacency_PaginationExactTotal(t *testing.T) {
	idx := buildFlowsIndex()

	result := graph.LinkExplorerAdjacency(idx, "ch1", "downstream", 1, 0, 1, "", "")
	require.Len(t, result.Rows, 1)
	assert.Equal(t, 2, result.Total, "total reflects the full adjacency even though the page is truncated")
	assert.True(t, result.Truncated)

	result = graph.LinkExplorerAdjacency(idx, "ch1", "downstream", 1, 1, 1, "", "")
	require.Len(t, result.Rows, 1)
	assert.False(t, result.Truncated)
}

func TestLinkExplorerAdjacency_UnknownNode(t *testing.T) {
	idx := buildFlowsIndex()

	result := graph.LinkExplorerAdjacency(idx, "does-not-exist", "downstream", 1, 0, 0, "", "")
	assert.Empty(t, result.Rows)
	assert.Equal(t, 0, result.Total)
}

func TestLinkExplorerAdjacency_IsolatedNode(t *testing.T) {
	idx := buildFlowsIndex()

	result := graph.LinkExplorerAdjacency(idx, "Iso1", "downstream", 2, 0, 0, "", "")
	assert.Empty(t, result.Rows)
	assert.Equal(t, 0, result.Total)
}
