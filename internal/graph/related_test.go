package graph_test

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildRelatedIndex builds a graph spread across five files:
//
//	svc-a/store.go:    v1 (variable), f0 (function)
//	svc-a/reader.go:   r1, r2 (functions)  — both read v1, r1 also calls f0
//	svc-a/deep.go:     d1 (function)       — called by r1 (depth 2 from store.go)
//	svc-a/island.go:   i1 (function)       — no edges to store.go's neighborhood
//	svc-b/client.js:   c1 (http_client)    — calls f0 cross-service
func buildRelatedIndex() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "v1", Type: graph.NodeTypeVariable, Label: "config", Service: "svc-a", File: "svc-a/store.go", Line: 5})
	idx.AddNode(&graph.Node{ID: "f0", Type: graph.NodeTypeFunction, Label: "load", Service: "svc-a", File: "svc-a/store.go", Line: 20})
	idx.AddNode(&graph.Node{ID: "r1", Type: graph.NodeTypeFunction, Label: "readA", Service: "svc-a", File: "svc-a/reader.go", Line: 3})
	idx.AddNode(&graph.Node{ID: "r2", Type: graph.NodeTypeFunction, Label: "readB", Service: "svc-a", File: "svc-a/reader.go", Line: 30})
	idx.AddNode(&graph.Node{ID: "d1", Type: graph.NodeTypeFunction, Label: "deep", Service: "svc-a", File: "svc-a/deep.go", Line: 8})
	idx.AddNode(&graph.Node{ID: "i1", Type: graph.NodeTypeFunction, Label: "island", Service: "svc-a", File: "svc-a/island.go", Line: 1})
	idx.AddNode(&graph.Node{ID: "c1", Type: graph.NodeTypeHTTPClient, Label: "fetch", Service: "svc-b", File: "svc-b/client.js", Line: 12})
	idx.AddEdge(&graph.Edge{ID: "e1", From: "r1", To: "v1", Type: graph.EdgeTypeReads})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "r2", To: "v1", Type: graph.EdgeTypeReads})
	idx.AddEdge(&graph.Edge{ID: "e3", From: "r1", To: "f0", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e4", From: "r1", To: "d1", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e5", From: "c1", To: "f0", Type: graph.EdgeTypeHTTPCall})
	return idx
}

func TestRelatedFilesRanking(t *testing.T) {
	idx := buildRelatedIndex()

	seeds, related, missing := graph.RelatedFiles(idx, "", []string{"svc-a/store.go"}, 2)
	assert.Empty(t, missing)
	assert.Equal(t, []string{"svc-a/store.go"}, seeds)
	require.Len(t, related, 3) // reader.go, client.js, deep.go — island.go excluded

	// reader.go: 3 direct edges (e1, e2, e3) — top rank.
	assert.Equal(t, "svc-a/reader.go", related[0].File)
	assert.Equal(t, 3, related[0].Refs)
	assert.Equal(t, 1, related[0].MinDepth)
	assert.Equal(t, 2, related[0].Nodes)
	assert.Equal(t, []string{"calls", "reads"}, related[0].EdgeTypes)

	// client.js: 1 direct edge, cross-service.
	assert.Equal(t, "svc-b/client.js", related[1].File)
	assert.Equal(t, 1, related[1].Refs)
	assert.Equal(t, "svc-b", related[1].Service)

	// deep.go: no direct edge, reached at depth 2 via reader.go.
	assert.Equal(t, "svc-a/deep.go", related[2].File)
	assert.Equal(t, 0, related[2].Refs)
	assert.Equal(t, 2, related[2].MinDepth)
	assert.Equal(t, 1, related[2].Nodes)
}

func TestRelatedFilesDepthLimit(t *testing.T) {
	idx := buildRelatedIndex()

	_, related, _ := graph.RelatedFiles(idx, "", []string{"svc-a/store.go"}, 1)
	files := make([]string, 0, len(related))
	for _, e := range related {
		files = append(files, e.File)
	}
	assert.NotContains(t, files, "svc-a/deep.go")
}

func TestRelatedFilesMultipleSeeds(t *testing.T) {
	idx := buildRelatedIndex()

	// Both store.go and reader.go as seeds: edges between them do not rank,
	// deep.go becomes a direct (depth-1) reference of the seed set.
	seeds, related, missing := graph.RelatedFiles(idx, "", []string{"svc-a/store.go", "svc-a/reader.go"}, 2)
	assert.Empty(t, missing)
	assert.Equal(t, []string{"svc-a/reader.go", "svc-a/store.go"}, seeds)
	require.Len(t, related, 2)
	assert.Equal(t, "svc-a/deep.go", related[0].File)
	assert.Equal(t, 1, related[0].Refs)
	assert.Equal(t, 1, related[0].MinDepth)
	assert.Equal(t, "svc-b/client.js", related[1].File)
}

func TestRelatedFilesSuffixResolutionAndMissing(t *testing.T) {
	idx := buildRelatedIndex()

	// Short relative path resolves by suffix; unknown path lands in missing.
	seeds, related, missing := graph.RelatedFiles(idx, "", []string{"store.go", "nope.go"}, 2)
	assert.Equal(t, []string{"svc-a/store.go"}, seeds)
	assert.Equal(t, []string{"nope.go"}, missing)
	assert.NotEmpty(t, related)
}

func TestRelatedFilesServiceFilterOnSeeds(t *testing.T) {
	idx := buildRelatedIndex()

	// Seed resolution is service-scoped; neighbors in other services still rank.
	_, related, missing := graph.RelatedFiles(idx, "svc-b", []string{"client.js"}, 2)
	assert.Empty(t, missing)
	require.NotEmpty(t, related)
	assert.Equal(t, "svc-a/store.go", related[0].File)
	assert.Equal(t, 1, related[0].Refs)
}
