package graph_test

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
)

// buildFileIndex builds a two-service graph spread across three files:
//
//	svc-a/handlers.go: h1 (http_handler), f1 (function)
//	svc-a/util.go:     u1 (function)
//	svc-b/client.js:   c1 (http_client)
//
// Edges: h1 -> f1 (calls), f1 -> u1 (calls), c1 -> h1 (http_call)
func buildFileIndex() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "h1", Type: graph.NodeTypeHTTPHandler, Label: "GetUser", Service: "svc-a", File: "svc-a/handlers.go", Line: 10})
	idx.AddNode(&graph.Node{ID: "f1", Type: graph.NodeTypeFunction, Label: "loadUser", Service: "svc-a", File: "svc-a/handlers.go", Line: 30})
	idx.AddNode(&graph.Node{ID: "u1", Type: graph.NodeTypeFunction, Label: "normalize", Service: "svc-a", File: "svc-a/util.go", Line: 5})
	idx.AddNode(&graph.Node{ID: "c1", Type: graph.NodeTypeHTTPClient, Label: "fetchUser", Service: "svc-b", File: "svc-b/client.js", Line: 8})
	idx.AddEdge(&graph.Edge{ID: "e1", From: "h1", To: "f1", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "f1", To: "u1", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e3", From: "c1", To: "h1", Type: graph.EdgeTypeHTTPCall})
	return idx
}

func TestListFiles(t *testing.T) {
	idx := buildFileIndex()

	files := graph.ListFiles(idx, "", 0)
	assert.Len(t, files, 3)
	// Sorted by path.
	assert.Equal(t, "svc-a/handlers.go", files[0].File)
	assert.Equal(t, 1, files[0].Counts[graph.NodeTypeHTTPHandler])
	assert.Equal(t, 1, files[0].Counts[graph.NodeTypeFunction])
}

func TestListFilesQueryAndLimit(t *testing.T) {
	idx := buildFileIndex()

	files := graph.ListFiles(idx, "HANDLERS", 0)
	assert.Len(t, files, 1)
	assert.Equal(t, "svc-a/handlers.go", files[0].File)

	files = graph.ListFiles(idx, "", 2)
	assert.Len(t, files, 2)
}

func TestNodesInFileExact(t *testing.T) {
	idx := buildFileIndex()

	nodes := graph.NodesInFile(idx, "", "svc-a/handlers.go")
	assert.Len(t, nodes, 2)
	// Sorted by line.
	assert.Equal(t, "h1", nodes[0].ID)
	assert.Equal(t, "f1", nodes[1].ID)
}

func TestNodesInFileSuffix(t *testing.T) {
	idx := buildFileIndex()

	nodes := graph.NodesInFile(idx, "", "handlers.go")
	assert.Len(t, nodes, 2)

	nodes = graph.NodesInFile(idx, "svc-b", "handlers.go")
	assert.Empty(t, nodes)
}

func TestFileImpactForward(t *testing.T) {
	idx := buildFileIndex()

	entries := graph.FileImpact(idx, "svc-a", "svc-a/handlers.go", "forward", 0)
	assert.Len(t, entries, 1)
	assert.Equal(t, "svc-a/util.go", entries[0].File)
	assert.Equal(t, 1, entries[0].Nodes)
	// u1 sits at depth 2 from h1 but depth 1 from f1 — minimum wins.
	assert.Equal(t, 1, entries[0].MinDepth)
	assert.Equal(t, []string{"calls"}, entries[0].EdgeTypes)
}

func TestFileImpactBackward(t *testing.T) {
	idx := buildFileIndex()

	entries := graph.FileImpact(idx, "svc-a", "svc-a/handlers.go", "backward", 0)
	assert.Len(t, entries, 1)
	assert.Equal(t, "svc-b/client.js", entries[0].File)
	assert.Equal(t, "svc-b", entries[0].Service)
	assert.Equal(t, 1, entries[0].MinDepth)
	assert.Equal(t, []string{"http_call"}, entries[0].EdgeTypes)
}

func TestFileImpactBoth(t *testing.T) {
	idx := buildFileIndex()

	entries := graph.FileImpact(idx, "svc-a", "svc-a/handlers.go", "both", 0)
	assert.Len(t, entries, 2)
}

func TestFileImpactUnknownFile(t *testing.T) {
	idx := buildFileIndex()
	assert.Nil(t, graph.FileImpact(idx, "", "nope.go", "forward", 0))
}

func TestFileImpactMinDepthFromMultipleSeeds(t *testing.T) {
	// u1 is reached at depth 2 from h1 but depth 1 from f1 — the entry must
	// keep the minimum and count the node once.
	idx := buildFileIndex()

	entries := graph.FileImpact(idx, "svc-a", "svc-a/handlers.go", "forward", 0)
	assert.Len(t, entries, 1)
	assert.Equal(t, 1, entries[0].Nodes)
	// h1's traversal runs first (sorted by line), reaching u1 at depth 2;
	// f1's traversal then improves it to depth 1.
	assert.Equal(t, 1, entries[0].MinDepth)
}
