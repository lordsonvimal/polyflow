package graph_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// policyIndex: a --contains--> b --calls--> c, plus a captured local and a
// package variable hanging off b.
func policyIndex() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "a", Type: graph.NodeTypeStruct, Label: "a"})
	idx.AddNode(&graph.Node{ID: "b", Type: graph.NodeTypeFunction, Label: "b"})
	idx.AddNode(&graph.Node{ID: "c", Type: graph.NodeTypeFunction, Label: "c"})
	idx.AddNode(&graph.Node{ID: "loc", Type: graph.NodeTypeVariable, Label: "loc",
		Meta: map[string]string{"scope": "captured"}})
	idx.AddNode(&graph.Node{ID: "pkg", Type: graph.NodeTypeVariable, Label: "pkg",
		Meta: map[string]string{"scope": "package"}})
	idx.AddEdge(&graph.Edge{ID: "e1", From: "a", To: "b", Type: graph.EdgeTypeContains})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "b", To: "c", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e3", From: "b", To: "loc", Type: graph.EdgeTypeCaptures})
	idx.AddEdge(&graph.Edge{ID: "e4", From: "b", To: "pkg", Type: graph.EdgeTypeCaptures})
	return idx
}

func seen(rs []graph.TraversalResult) map[string]bool {
	m := map[string]bool{}
	for _, r := range rs {
		m[r.Node.Label] = true
	}
	return m
}

// TestTraverse_ZeroPolicyIsUnchanged — Traverse is shared with the graph-shape
// consumers (mermaid, trace, context), which want the raw walk. Shaping must
// be something a caller opts into, never a change to what Traverse means.
func TestTraverse_ZeroPolicyIsUnchanged(t *testing.T) {
	idx := policyIndex()
	got := seen(graph.Traverse(idx, "b", "out", graph.BFS, 10))
	assert.Equal(t, map[string]bool{"c": true, "loc": true, "pkg": true}, got)
	assert.Equal(t,
		seen(graph.TraverseWithPolicy(idx, "b", "out", graph.BFS, 10, graph.TraversalPolicy{})),
		got, "the zero policy is exactly Traverse")
}

func TestTraversalPolicy_DropLocals(t *testing.T) {
	idx := policyIndex()
	got := seen(graph.TraverseWithPolicy(idx, "b", "out", graph.BFS, 10, graph.BlastRadiusPolicy()))
	assert.NotContains(t, got, "loc", "closure-captured local")
	assert.Contains(t, got, "pkg", "package-scope state is shared, not local")
	assert.Contains(t, got, "c")
}

// TestTraversalPolicy_TerminalEdgesReportButDoNotExpand — the container is
// still an answer; what it leads to is not.
func TestTraversalPolicy_TerminalEdgesReportButDoNotExpand(t *testing.T) {
	idx := policyIndex()
	// Backward from c: b (calls), then a (contains from the struct).
	full := seen(graph.TraverseWithPolicy(idx, "c", "in", graph.BFS, 10, graph.BlastRadiusPolicy()))
	assert.Equal(t, map[string]bool{"b": true, "a": true}, full)

	// With containment terminal, a is reported but not walked out of. Give a
	// an inbound edge to prove the walk actually stops there.
	idx.AddNode(&graph.Node{ID: "z", Type: graph.NodeTypeFunction, Label: "z"})
	idx.AddEdge(&graph.Edge{ID: "e5", From: "z", To: "a", Type: graph.EdgeTypeInstantiates})

	assert.Contains(t, seen(graph.TraverseWithPolicy(idx, "c", "in", graph.BFS, 10, graph.BlastRadiusPolicy())), "z",
		"the default expands containment — this is the lobsters path")

	tight := seen(graph.TraverseWithPolicy(idx, "c", "in", graph.BFS, 10, graph.ContainmentTerminal()))
	assert.Contains(t, tight, "a", "the container is reported")
	assert.NotContains(t, tight, "z", "but the walk stops there")
}

// TestTraversalPolicy_DroppedLocalIsNotARoadblock — dropping a node must not
// depend on which edge happened to reach it first.
func TestTraversalPolicy_DroppedLocalIsNotARoadblock(t *testing.T) {
	idx := policyIndex()
	idx.AddEdge(&graph.Edge{ID: "e6", From: "c", To: "loc", Type: graph.EdgeTypeReads})
	got := seen(graph.TraverseWithPolicy(idx, "b", "out", graph.BFS, 10, graph.BlastRadiusPolicy()))
	assert.NotContains(t, got, "loc", "reached twice, dropped both times")
	assert.Contains(t, got, "c")
}
