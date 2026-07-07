package graph_test

import (
	"fmt"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
)

// buildLinearIndex builds: n1 -> n2 -> n3 -> n4
func buildLinearIndex() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	for _, id := range []string{"n1", "n2", "n3", "n4"} {
		idx.AddNode(&graph.Node{ID: id, Label: id})
	}
	idx.AddEdge(&graph.Edge{ID: "e1", From: "n1", To: "n2", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "n2", To: "n3", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e3", From: "n3", To: "n4", Type: graph.EdgeTypeCalls})
	return idx
}

func TestDescendants(t *testing.T) {
	idx := buildLinearIndex()

	results := graph.Descendants(idx, "n1", 0)
	ids := nodeIDs(results)
	assert.ElementsMatch(t, []string{"n2", "n3", "n4"}, ids)
}

func TestDescendantsMaxDepth(t *testing.T) {
	idx := buildLinearIndex()

	results := graph.Descendants(idx, "n1", 2)
	ids := nodeIDs(results)
	assert.ElementsMatch(t, []string{"n2", "n3"}, ids)
}

func TestAncestors(t *testing.T) {
	idx := buildLinearIndex()

	results := graph.Ancestors(idx, "n4", 0)
	ids := nodeIDs(results)
	assert.ElementsMatch(t, []string{"n1", "n2", "n3"}, ids)
}

func TestAncestorsMaxDepth(t *testing.T) {
	idx := buildLinearIndex()

	results := graph.Ancestors(idx, "n4", 1)
	ids := nodeIDs(results)
	assert.ElementsMatch(t, []string{"n3"}, ids)
}

func TestTraverseUnknownStart(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	results := graph.Descendants(idx, "nonexistent", 0)
	assert.Nil(t, results)
}

func TestTraverseCycleDoesNotLoop(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	for _, id := range []string{"a", "b", "c"} {
		idx.AddNode(&graph.Node{ID: id, Label: id})
	}
	// a -> b -> c -> a (cycle)
	idx.AddEdge(&graph.Edge{ID: "e1", From: "a", To: "b", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "b", To: "c", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e3", From: "c", To: "a", Type: graph.EdgeTypeCalls})

	results := graph.Descendants(idx, "a", 0)
	ids := nodeIDs(results)
	assert.ElementsMatch(t, []string{"b", "c"}, ids, "cycle should not cause infinite loop")
}

func TestTraverseDepthRecorded(t *testing.T) {
	idx := buildLinearIndex()
	results := graph.Descendants(idx, "n1", 0)
	byID := make(map[string]int)
	for _, r := range results {
		byID[r.Node.ID] = r.Depth
	}
	assert.Equal(t, 1, byID["n2"])
	assert.Equal(t, 2, byID["n3"])
	assert.Equal(t, 3, byID["n4"])
}

func TestTraverseDFS(t *testing.T) {
	idx := buildLinearIndex()
	results := graph.Traverse(idx, "n1", "out", graph.DFS, 0)
	ids := nodeIDs(results)
	assert.ElementsMatch(t, []string{"n2", "n3", "n4"}, ids)
}

func nodeIDs(results []graph.TraversalResult) []string {
	ids := make([]string, len(results))
	for i, r := range results {
		ids[i] = r.Node.ID
	}
	return ids
}

// BenchmarkTraverse_Depth10 measures BFS traversal on a 10k node linear graph at depth 10.
func BenchmarkTraverse_Depth10(b *testing.B) {
	const nodeCount = 10000
	idx := graph.NewAdjacencyIndex()
	for i := 0; i < nodeCount; i++ {
		id := fmt.Sprintf("n%d", i)
		idx.AddNode(&graph.Node{ID: id, Label: id})
		if i > 0 {
			idx.AddEdge(&graph.Edge{
				ID:   fmt.Sprintf("e%d", i),
				From: fmt.Sprintf("n%d", i-1),
				To:   id,
				Type: graph.EdgeTypeCalls,
			})
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		graph.Descendants(idx, "n0", 10)
	}
}
