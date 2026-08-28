package mcpserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestUnknownEdges_DefaultsToUnknownConfidence(t *testing.T) {
	store, idx := fixture()
	idx.AddNode(&graph.Node{ID: "unresolved", Type: graph.NodeTypeService, Label: "unresolved", Service: ""})
	// A fresh producer, distinct from the fixture's own fe:fetchUser (which
	// already has a static-confidence edge) — this one has no other edge, so
	// it is genuinely unresolved, not excluded by the
	// resolved-elsewhere-in-idx check.
	idx.AddNode(&graph.Node{ID: "fe:probeStatus", Type: graph.NodeTypeHTTPClient, Label: "probeStatus", Service: "frontend", File: "api.js", Line: 30, Language: "javascript"})
	idx.AddEdge(&graph.Edge{
		ID: "e_unknown", From: "fe:probeStatus", To: "unresolved",
		Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceUnknown, Label: "GET /missing",
	})

	cs := connect(t, store, idx)
	var out unknownEdgesOutput
	callJSON(t, cs, "unknown_edges", map[string]any{}, &out)

	require.Len(t, out.Edges, 1)
	assert.Equal(t, "unknown", out.Edges[0].Confidence)
	assert.Equal(t, "fe:probeStatus", out.Edges[0].FromID)
	assert.Equal(t, "probeStatus", out.Edges[0].From)
	assert.Equal(t, "unresolved", out.Edges[0].To)
	assert.Equal(t, 1, out.Total)
	assert.Equal(t, 1, out.ByConfidence["unknown"])

	// The fixture's own static-confidence edge (e1) must not appear at the
	// default "unknown" threshold.
	for _, e := range out.Edges {
		assert.NotEqual(t, "static", e.Confidence)
	}
}

func TestUnknownEdges_MinConfidenceWidensReport(t *testing.T) {
	store, idx := fixture()
	idx.AddNode(&graph.Node{ID: "unresolved", Type: graph.NodeTypeService, Label: "unresolved", Service: ""})
	idx.AddEdge(&graph.Edge{
		ID: "e_unknown", From: "fe:fetchUser", To: "unresolved",
		Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceUnknown,
	})

	cs := connect(t, store, idx)
	var out unknownEdgesOutput
	callJSON(t, cs, "unknown_edges", map[string]any{"min_confidence": "static"}, &out)

	// Both the unknown edge and the fixture's static e1 (fe:fetchUser -> be:getUser)
	// qualify at the "static" threshold.
	assert.Equal(t, 2, out.Total)
	assert.Equal(t, 1, out.ByConfidence["unknown"])
	assert.Equal(t, 1, out.ByConfidence["static"])
}

func TestUnknownEdges_ServiceAndEdgeTypeFilters(t *testing.T) {
	store, idx := fixture()
	idx.AddNode(&graph.Node{ID: "unresolved", Type: graph.NodeTypeService, Label: "unresolved", Service: ""})
	idx.AddNode(&graph.Node{ID: "fe:probeStatus", Type: graph.NodeTypeHTTPClient, Label: "probeStatus", Service: "frontend", File: "api.js", Line: 30, Language: "javascript"})
	idx.AddEdge(&graph.Edge{
		ID: "e_unknown", From: "fe:probeStatus", To: "unresolved",
		Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceUnknown,
	})

	cs := connect(t, store, idx)

	var byService unknownEdgesOutput
	callJSON(t, cs, "unknown_edges", map[string]any{"service": "backend"}, &byService)
	assert.Empty(t, byService.Edges, "the unknown edge's producer is in frontend, not backend")

	var byType unknownEdgesOutput
	callJSON(t, cs, "unknown_edges", map[string]any{"edge_type": "publishes"}, &byType)
	assert.Empty(t, byType.Edges, "the fixture only has an http_call at unknown confidence")
}

// TestUnknownEdges_ProducerResolvedElsewhereExcluded proves the MCP tool
// shares FilterEdgesByConfidence's overcounting fix with the CLI: a
// producer with a stale local-only unknown edge is excluded once a
// better-resolved edge exists for the same producer elsewhere in idx.
func TestUnknownEdges_ProducerResolvedElsewhereExcluded(t *testing.T) {
	store, idx := fixture()
	idx.AddNode(&graph.Node{ID: "unresolved", Type: graph.NodeTypeService, Label: "unresolved", Service: ""})
	idx.AddNode(&graph.Node{ID: "remote_handler", Type: graph.NodeTypeHTTPHandler, Label: "remote handler", Service: "other"})
	idx.AddEdge(&graph.Edge{ID: "local_unknown", From: "fe:fetchUser", To: "unresolved", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceUnknown})
	idx.AddEdge(&graph.Edge{ID: "bridge_resolved", From: "fe:fetchUser", To: "remote_handler", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceInferred})

	cs := connect(t, store, idx)
	var out unknownEdgesOutput
	callJSON(t, cs, "unknown_edges", map[string]any{}, &out)

	assert.Empty(t, out.Edges, "fe:fetchUser is resolved elsewhere in idx; its stale unknown edge must not be reported")
}
