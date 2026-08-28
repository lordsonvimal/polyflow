package contract

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestFilterEdgesByConfidence_ThresholdAndTypeAgnostic(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "n1", Service: "svc", File: "a.go", Line: 1})
	idx.AddNode(&graph.Node{ID: "n2", Service: "svc", File: "b.go", Line: 2})
	idx.AddNode(&graph.Node{ID: "n3", Service: "svc", File: "c.go", Line: 3})
	idx.AddNode(&graph.Node{ID: "unresolved", Service: ""})

	idx.AddEdge(&graph.Edge{ID: "e1", From: "n1", To: "unresolved", Type: "http_call", Confidence: graph.ConfidenceUnknown})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "n2", To: "n3", Type: "http_call", Confidence: graph.ConfidenceStatic})
	idx.AddEdge(&graph.Edge{ID: "e3", From: "n2", To: "n3", Type: "calls"}) // no confidence — must never appear

	unknown := FilterEdgesByConfidence(idx, graph.ConfidenceUnknown)
	assert.Len(t, unknown, 1)
	assert.Equal(t, "e1", unknown[0].ID)

	everything := FilterEdgesByConfidence(idx, graph.ConfidenceStatic)
	assert.Len(t, everything, 2)
}

// TestFilterEdgesByConfidence_ProducerResolvedElsewhereExcluded is the
// regression case for the overcounting bug found integrating this against a
// live fleet: a producer with a local-only "unknown" edge must not be
// reported once a better-resolved edge for the same producer (From node)
// exists anywhere in the same index.
func TestFilterEdgesByConfidence_ProducerResolvedElsewhereExcluded(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "prod1", Service: "svc", File: "client.go", Line: 10})
	idx.AddNode(&graph.Node{ID: "unresolved", Service: ""})
	idx.AddNode(&graph.Node{ID: "remote_handler", Service: "other"})

	// Same producer, two edges: one local-only unresolved view, one
	// resolved (as if merged in from bridge.db's wider visibility).
	idx.AddEdge(&graph.Edge{ID: "local_unknown", From: "prod1", To: "unresolved", Type: "http_call", Confidence: graph.ConfidenceUnknown})
	idx.AddEdge(&graph.Edge{ID: "bridge_resolved", From: "prod1", To: "remote_handler", Type: "http_call", Confidence: graph.ConfidenceInferred})

	unknown := FilterEdgesByConfidence(idx, graph.ConfidenceUnknown)
	assert.Empty(t, unknown, "prod1 is resolved elsewhere in the index; its stale unknown edge must not be reported")
}

func TestFilterEdgesByConfidence_SortedByProducerLocation(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "z", Service: "svc", File: "z.go", Line: 1})
	idx.AddNode(&graph.Node{ID: "a", Service: "svc", File: "a.go", Line: 1})
	idx.AddNode(&graph.Node{ID: "sink", Service: ""})

	idx.AddEdge(&graph.Edge{ID: "e_z", From: "z", To: "sink", Type: "http_call", Confidence: graph.ConfidenceUnknown})
	idx.AddEdge(&graph.Edge{ID: "e_a", From: "a", To: "sink", Type: "http_call", Confidence: graph.ConfidenceUnknown})

	matched := FilterEdgesByConfidence(idx, graph.ConfidenceUnknown)
	assert.Len(t, matched, 2)
	assert.Equal(t, "e_a", matched[0].ID, "a.go sorts before z.go")
	assert.Equal(t, "e_z", matched[1].ID)
}
