package mcpserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestExplain_ReturnsProvenance(t *testing.T) {
	store, idx := fixture()
	idx.AddNode(&graph.Node{ID: "be:h", Type: graph.NodeTypeHTTPHandler, Label: "GetThing", Service: "backend", File: "h.go", Line: 7})
	idx.AddNode(&graph.Node{ID: "fe:c", Type: graph.NodeTypeHTTPClient, Label: "getThing", Service: "frontend"})
	idx.AddEdge(&graph.Edge{
		ID: "e_explain", From: "be:h", To: "fe:c", Type: graph.EdgeTypeHTTPCall, Label: "GET /thing",
		Confidence: graph.ConfidenceStatic,
		Sources: []graph.SourceRef{
			{Provider: "static", Confidence: "candidate", Layer: "L5", Rule: "contract_engine", Ref: "h.go:7"},
		},
	})

	cs := connect(t, store, idx)
	var out graph.EdgeExplanation
	callJSON(t, cs, "explain", map[string]any{"edge_id": "e_explain"}, &out)

	assert.Equal(t, "e_explain", out.Edge.ID)
	require.Len(t, out.RuleChain, 1)
	assert.Equal(t, "contract_engine", out.RuleChain[0].Rule)
	assert.Equal(t, "L5", out.RuleChain[0].Layer)
}

func TestExplain_UnknownEdge(t *testing.T) {
	store, idx := fixture()
	cs := connect(t, store, idx)
	var out map[string]any
	callJSON(t, cs, "explain", map[string]any{"edge_id": "nope"}, &out)
	assert.Contains(t, out["error"], "nope")
}
