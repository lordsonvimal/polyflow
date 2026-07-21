package impact_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/impact"
)

// fixtureIndex builds:
//
//	frontend:fetchUser (http_client, api.js) --http_call--> backend:getUser (handler.go)
//	backend:getUser --calls--> backend:queryDB (db.go)
func fixtureIndex() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "fe:fetchUser", Type: graph.NodeTypeHTTPClient, Label: "fetchUser", Service: "frontend", File: "api.js", Line: 10})
	idx.AddNode(&graph.Node{ID: "be:getUser", Type: graph.NodeTypeHTTPHandler, Label: "GET /api/user", Service: "backend", File: "handler.go", Line: 20})
	idx.AddNode(&graph.Node{ID: "be:queryDB", Type: graph.NodeTypeFunction, Label: "queryDB", Service: "backend", File: "db.go", Line: 40})
	idx.AddEdge(&graph.Edge{ID: "e1", From: "fe:fetchUser", To: "be:getUser", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceStatic})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "be:getUser", To: "be:queryDB", Type: graph.EdgeTypeCalls})
	return idx
}

func TestBuild_BlastRadius(t *testing.T) {
	idx := fixtureIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], 10, "", false, 0)

	require.Equal(t, 2, out.TotalCallers)
	assert.Equal(t, "be:getUser", out.Callers[0].ID)
	assert.Equal(t, 1, out.Callers[0].Depth)
	assert.Equal(t, "fe:fetchUser", out.Callers[1].ID)
	assert.Equal(t, 2, out.Callers[1].Depth)

	// fetchUser has no incoming edges → entry point.
	require.Len(t, out.EntryPoints, 1)
	assert.Equal(t, "fe:fetchUser", out.EntryPoints[0].ID)

	assert.ElementsMatch(t, []string{"frontend", "backend"}, out.ServicesAffected)

	// The http_call edge arrives at backend from frontend.
	require.Len(t, out.CrossServiceTriggers, 1)
	assert.Equal(t, "frontend", out.CrossServiceTriggers[0].FromService)
	assert.Equal(t, 1, out.CrossServiceTriggers[0].EdgeCount)
}

func TestBuild_ServiceFilterExcludesOtherServices(t *testing.T) {
	idx := fixtureIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], 10, "backend", false, 0)

	require.Equal(t, 1, out.TotalCallers)
	assert.Equal(t, "be:getUser", out.Callers[0].ID)
	assert.Equal(t, []string{"backend"}, out.ServicesAffected)
}

func TestAttachUnresolved_ScopedToTraversedFiles(t *testing.T) {
	idx := fixtureIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], 10, "", false, 0)

	out.AttachUnresolved([]graph.UnresolvedRef{
		{Service: "frontend", File: "api.js", Line: 11, Name: "dynCall", Kind: "call_ref"},
		{Service: "backend", File: "unrelated.go", Line: 3, Name: "other", Kind: "call_ref"},
	})

	require.Len(t, out.Unresolved, 1)
	assert.Equal(t, "dynCall", out.Unresolved[0].Name)
	assert.Contains(t, out.UnresolvedNote, "verify this 1 unresolved reference manually")
}

func TestBuild_UnresolvedDefaultsToEmptyNotNull(t *testing.T) {
	idx := fixtureIndex()
	out := impact.Build(idx, idx.Nodes["be:queryDB"], 10, "", false, 0)

	data, err := json.Marshal(out)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"unresolved":[]`)
}
