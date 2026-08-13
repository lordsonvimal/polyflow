package graph_test

import (
	"encoding/json"
	"testing"

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
