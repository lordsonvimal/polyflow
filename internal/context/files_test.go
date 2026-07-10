package context_test

import (
	"testing"

	pfcontext "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// filesFixture: store.go's variable is read from reader.go; reader.go calls
// into deep.go; island.go is disconnected.
func filesFixture() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "v1", Type: graph.NodeTypeVariable, Label: "config", Service: "svc", File: "svc/store.go", Line: 5})
	idx.AddNode(&graph.Node{ID: "r1", Type: graph.NodeTypeFunction, Label: "read", Service: "svc", File: "svc/reader.go", Line: 3})
	idx.AddNode(&graph.Node{ID: "d1", Type: graph.NodeTypeFunction, Label: "deep", Service: "svc", File: "svc/deep.go", Line: 8})
	idx.AddNode(&graph.Node{ID: "i1", Type: graph.NodeTypeFunction, Label: "island", Service: "svc", File: "svc/island.go", Line: 1})
	idx.AddEdge(&graph.Edge{ID: "e1", From: "r1", To: "v1", Type: graph.EdgeTypeReads})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "r1", To: "d1", Type: graph.EdgeTypeCalls})
	return idx
}

func TestBuildFiles(t *testing.T) {
	idx := filesFixture()

	r, err := pfcontext.BuildFiles(idx, "", []string{"store.go"}, 2, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"svc/store.go"}, r.Files)
	require.Len(t, r.Related, 2)
	assert.Equal(t, "svc/reader.go", r.Related[0].File)
	assert.Equal(t, "svc/deep.go", r.Related[1].File)
	// Trust contract: unresolved is present-but-empty, never absent.
	assert.NotNil(t, r.Unresolved)
	assert.Empty(t, r.Unresolved)
}

func TestBuildFilesLimit(t *testing.T) {
	idx := filesFixture()

	r, err := pfcontext.BuildFiles(idx, "", []string{"store.go"}, 2, 1)
	require.NoError(t, err)
	require.Len(t, r.Related, 1)
	assert.Equal(t, "svc/reader.go", r.Related[0].File)
}

func TestBuildFilesMissingErrors(t *testing.T) {
	idx := filesFixture()

	_, err := pfcontext.BuildFiles(idx, "", []string{"store.go", "nope.go"}, 2, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope.go")
}

func TestFilesResultAttachUnresolvedScoping(t *testing.T) {
	idx := filesFixture()

	r, err := pfcontext.BuildFiles(idx, "", []string{"store.go"}, 2, 0)
	require.NoError(t, err)
	r.AttachUnresolved([]graph.UnresolvedRef{
		{Service: "svc", File: "svc/reader.go", Line: 9, Name: "dyn", Kind: "call_ref"},
		{Service: "svc", File: "svc/island.go", Line: 2, Name: "other", Kind: "call_ref"},
	})
	require.Len(t, r.Unresolved, 1)
	assert.Equal(t, "dyn", r.Unresolved[0].Name)
	assert.Contains(t, r.UnresolvedNote, "verify")
}

func TestFilesResultApplyBudget(t *testing.T) {
	idx := filesFixture()

	r, err := pfcontext.BuildFiles(idx, "", []string{"store.go"}, 2, 0)
	require.NoError(t, err)
	r.AttachUnresolved([]graph.UnresolvedRef{
		{Service: "svc", File: "svc/reader.go", Line: 9, Name: "dyn", Kind: "call_ref"},
	})

	// A tiny budget trims the related list to one entry but never the
	// unresolved section.
	r.ApplyBudget(40)
	require.NotNil(t, r.Budget)
	assert.Len(t, r.Related, 1)
	assert.Equal(t, 1, r.Budget.OmittedFiles)
	assert.Len(t, r.Unresolved, 1)

	// A comfortable budget is a no-trim annotation.
	r2, err := pfcontext.BuildFiles(idx, "", []string{"store.go"}, 2, 0)
	require.NoError(t, err)
	r2.ApplyBudget(100000)
	require.NotNil(t, r2.Budget)
	assert.Len(t, r2.Related, 2)
	assert.Zero(t, r2.Budget.OmittedFiles)
}
