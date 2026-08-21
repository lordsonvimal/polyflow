package deadcode_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/deadcode"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// fixtureIndex builds:
//
//	backend:handler (http_handler, zero inbound calls — an entry point, must NOT be flagged)
//	backend:handler --calls--> backend:used (real caller, must NOT be flagged)
//	backend:orphan (zero inbound edges at all, must be flagged)
//	backend:File --contains--> backend:contained (only a structural inbound edge, must still be flagged)
func fixtureIndex() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "be:handler", Type: graph.NodeTypeHTTPHandler, Label: "GET /api/x", Service: "backend", File: "handler.go", Line: 10})
	idx.AddNode(&graph.Node{ID: "be:used", Type: graph.NodeTypeFunction, Label: "used", Service: "backend", File: "used.go", Line: 20})
	idx.AddNode(&graph.Node{ID: "be:orphan", Type: graph.NodeTypeFunction, Label: "orphan", Service: "backend", File: "orphan.go", Line: 30})
	idx.AddNode(&graph.Node{ID: "be:file", Type: graph.NodeTypeFile, Label: "contained.go", Service: "backend", File: "contained.go", Line: 0})
	idx.AddNode(&graph.Node{ID: "be:contained", Type: graph.NodeTypeFunction, Label: "contained", Service: "backend", File: "contained.go", Line: 5})
	idx.AddEdge(&graph.Edge{ID: "e1", From: "be:handler", To: "be:used", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "be:file", To: "be:contained", Type: graph.EdgeTypeContains})
	return idx
}

func TestBuild_FlagsOnlyZeroCallerNonEntrypoints(t *testing.T) {
	idx := fixtureIndex()
	out := deadcode.Build(idx, deadcode.Options{})

	require.Equal(t, 2, out.Total)
	ids := []string{out.Functions[0].ID, out.Functions[1].ID}
	assert.ElementsMatch(t, []string{"be:orphan", "be:contained"}, ids)
}

func TestBuild_EntrypointNotFlaggedDespiteZeroCallers(t *testing.T) {
	idx := fixtureIndex()
	out := deadcode.Build(idx, deadcode.Options{})

	for _, f := range out.Functions {
		assert.NotEqual(t, "be:handler", f.ID)
	}
}

func TestBuild_ContainsEdgeIsNotARealCaller(t *testing.T) {
	idx := fixtureIndex()
	out := deadcode.Build(idx, deadcode.Options{})

	var found bool
	for _, f := range out.Functions {
		if f.ID == "be:contained" {
			found = true
		}
	}
	assert.True(t, found, "a node reached only by a structural contains edge must still be flagged dead")
}

func TestBuild_RealCallerExcludesFromResult(t *testing.T) {
	idx := fixtureIndex()
	out := deadcode.Build(idx, deadcode.Options{})

	for _, f := range out.Functions {
		assert.NotEqual(t, "be:used", f.ID)
	}
}

func TestBuild_ServiceFilter(t *testing.T) {
	idx := fixtureIndex()
	idx.AddNode(&graph.Node{ID: "fe:orphan", Type: graph.NodeTypeFunction, Label: "orphan", Service: "frontend", File: "orphan.js", Line: 1})

	out := deadcode.Build(idx, deadcode.Options{Service: "backend"})
	for _, f := range out.Functions {
		assert.Equal(t, "backend", f.Service)
	}
}

func TestBuild_FileFilter(t *testing.T) {
	idx := fixtureIndex()
	out := deadcode.Build(idx, deadcode.Options{File: "orphan.go"})

	require.Equal(t, 1, out.Total)
	assert.Equal(t, "be:orphan", out.Functions[0].ID)
}

func TestBuild_EmptyResultIsEmptySliceNotNil(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "be:handler", Type: graph.NodeTypeHTTPHandler, Label: "GET /x", Service: "backend", File: "h.go", Line: 1})

	out := deadcode.Build(idx, deadcode.Options{})
	require.NotNil(t, out.Functions)
	assert.Equal(t, 0, out.Total)
}
