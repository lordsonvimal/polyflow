package graph_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTreeIndex builds one service spread across nested dirs, mirroring the
// plan's worked example (docs/plan-9-ui-backend.md UB.1):
//
//	app/jobs/sync.rb:   SyncJob (class) -> perform (method).
//	  containment.go never wires a file->class `contains` edge (only
//	  internal/parser/ruby.go's linkRubyClassMembers wires class->method),
//	  so SyncJob itself is the orphan symbol the file-path fallback must
//	  surface — but it still has its own `contains` child (perform), which
//	  the fallback must walk rather than truncate.
//	app/models/user.rb: User (class), no children at all — the plain
//	  childless-orphan case.
//
// Plus an unrelated service "svc-b" to prove the tree is scoped per-service.
func buildTreeIndex() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "svc:rails-svc", Type: graph.NodeTypeService, Label: "rails-svc"})
	idx.AddNode(&graph.Node{ID: "class:syncjob", Type: graph.NodeTypeClass, Label: "SyncJob", Service: "rails-svc", File: "app/jobs/sync.rb", Line: 3, EndLine: 40})
	idx.AddNode(&graph.Node{ID: "method:perform", Type: graph.NodeTypeMethod, Label: "perform", Service: "rails-svc", File: "app/jobs/sync.rb", Line: 5, EndLine: 22})
	idx.AddNode(&graph.Node{ID: "class:user", Type: graph.NodeTypeClass, Label: "User", Service: "rails-svc", File: "app/models/user.rb", Line: 1, EndLine: 10})

	// class -> method, exactly as linkRubyClassMembers wires it. No file
	// node and no file->class edge exist anywhere for either class.
	idx.AddEdge(&graph.Edge{ID: "ce1", From: "class:syncjob", To: "method:perform", Type: graph.EdgeTypeContains})

	idx.AddNode(&graph.Node{ID: "other-svc-node", Type: graph.NodeTypeFunction, Label: "unrelated", Service: "svc-b", File: "main.go", Line: 1})
	return idx
}

func TestBuildTree_Structure(t *testing.T) {
	idx := buildTreeIndex()

	res, err := graph.BuildTree(idx, "rails-svc")
	require.NoError(t, err)
	assert.Equal(t, "rails-svc", res.Service)

	// Root: one folder "app".
	require.Len(t, res.Tree, 1)
	app := res.Tree[0]
	assert.Equal(t, "folder", app.Kind)
	assert.Equal(t, "app", app.Name)
	assert.Equal(t, "app", app.Path)

	// app -> jobs, models (folders first, alphabetical).
	require.Len(t, app.Children, 2)
	jobs := app.Children[0]
	models := app.Children[1]
	assert.Equal(t, "jobs", jobs.Name)
	assert.Equal(t, "models", models.Name)

	// jobs -> sync.rb file (synthesized, no NodeTypeFile node was ever
	// minted for it — class nodes get no file->class contains edge) ->
	// SyncJob class (orphan-attached) -> perform method (walked from the
	// orphan, not truncated).
	require.Len(t, jobs.Children, 1)
	syncFile := jobs.Children[0]
	assert.Equal(t, "file", syncFile.Kind)
	assert.Equal(t, "sync.rb", syncFile.Name)
	assert.Equal(t, "app/jobs/sync.rb", syncFile.Path)
	assert.Empty(t, syncFile.NodeID)

	require.Len(t, syncFile.Children, 1)
	syncJob := syncFile.Children[0]
	assert.Equal(t, "class", syncJob.Kind)
	assert.Equal(t, "SyncJob", syncJob.Name)
	assert.Equal(t, 3, syncJob.Line)
	assert.Equal(t, 40, syncJob.EndLine)

	require.Len(t, syncJob.Children, 1)
	perform := syncJob.Children[0]
	assert.Equal(t, "method", perform.Kind)
	assert.Equal(t, "perform", perform.Name)
	assert.Equal(t, 5, perform.Line)
	assert.Equal(t, 22, perform.EndLine)
	assert.Empty(t, perform.Children)

	// models -> user.rb file -> User class, the plain childless-orphan case.
	require.Len(t, models.Children, 1)
	userFile := models.Children[0]
	assert.Equal(t, "file", userFile.Kind)
	assert.Equal(t, "user.rb", userFile.Name)
	assert.Empty(t, userFile.NodeID)

	require.Len(t, userFile.Children, 1)
	userClass := userFile.Children[0]
	assert.Equal(t, "class", userClass.Kind)
	assert.Equal(t, "User", userClass.Name)
	assert.Empty(t, userClass.Children)

	assert.Equal(t, graph.TreeCounts{Folders: 3, Files: 2, Symbols: 3}, res.Counts)
}

func TestBuildTree_UnknownService(t *testing.T) {
	idx := buildTreeIndex()

	_, err := graph.BuildTree(idx, "no-such-service")
	assert.Error(t, err)
}

// TestBuildTree_ContainsCycleDoesNotStackOverflow guards against a
// `contains` cycle (a duplicate/vendored symbol whose containment edges loop
// back on themselves has been observed live on a real fleet) sending walk
// into infinite recursion. A -> B -> A must terminate, cutting the cycle at
// the node already on the current path rather than crashing the server.
func TestBuildTree_ContainsCycleDoesNotStackOverflow(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "file:a.go", Type: graph.NodeTypeFile, Label: "a.go", Service: "svc", File: "a.go"})
	idx.AddNode(&graph.Node{ID: "struct:A", Type: graph.NodeTypeStruct, Label: "A", Service: "svc", File: "a.go", Line: 3})
	idx.AddNode(&graph.Node{ID: "struct:B", Type: graph.NodeTypeStruct, Label: "B", Service: "svc", File: "a.go", Line: 10})

	idx.AddEdge(&graph.Edge{ID: "e1", From: "file:a.go", To: "struct:A", Type: graph.EdgeTypeContains})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "struct:A", To: "struct:B", Type: graph.EdgeTypeContains})
	idx.AddEdge(&graph.Edge{ID: "e3", From: "struct:B", To: "struct:A", Type: graph.EdgeTypeContains})

	done := make(chan struct {
		res *graph.TreeResult
		err error
	}, 1)
	go func() {
		res, err := graph.BuildTree(idx, "svc")
		done <- struct {
			res *graph.TreeResult
			err error
		}{res, err}
	}()

	select {
	case out := <-done:
		require.NoError(t, out.err)
		require.Len(t, out.res.Tree, 1)
		fileNode := out.res.Tree[0]
		require.Len(t, fileNode.Children, 1)
		structA := fileNode.Children[0]
		assert.Equal(t, "A", structA.Name)
		require.Len(t, structA.Children, 1)
		structB := structA.Children[0]
		assert.Equal(t, "B", structB.Name)
		assert.Empty(t, structB.Children, "cycle back to A must be cut off, not re-descended")
	case <-time.After(5 * time.Second):
		t.Fatal("BuildTree did not terminate on a contains cycle (stack overflow / infinite recursion)")
	}
}

func TestBuildTree_Determinism(t *testing.T) {
	idx := buildTreeIndex()

	res1, err := graph.BuildTree(idx, "rails-svc")
	require.NoError(t, err)
	res2, err := graph.BuildTree(idx, "rails-svc")
	require.NoError(t, err)

	b1, err := json.Marshal(res1)
	require.NoError(t, err)
	b2, err := json.Marshal(res2)
	require.NoError(t, err)
	assert.Equal(t, string(b1), string(b2))
}
