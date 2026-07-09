package context_test

import (
	"testing"

	ctx "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixture builds a small graph:
//
//	frontend:fetchUser (http_client) --http_call--> backend:getUser (http_handler)
//	                                                backend:getUser --calls--> backend:queryDB (function)
func fixtureIndex() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()

	frontend := &graph.Node{ID: "fe:fetchUser", Type: graph.NodeTypeHTTPClient, Label: "fetchUser", Service: "frontend", File: "api.js", Line: 10, Language: "javascript"}
	handler := &graph.Node{ID: "be:getUser", Type: graph.NodeTypeHTTPHandler, Label: "GET /api/user", Service: "backend", File: "handler.go", Line: 20, Language: "go"}
	db := &graph.Node{ID: "be:queryDB", Type: graph.NodeTypeFunction, Label: "queryDB", Service: "backend", File: "db.go", Line: 40, Language: "go"}

	idx.AddNode(frontend)
	idx.AddNode(handler)
	idx.AddNode(db)

	idx.AddEdge(&graph.Edge{
		ID: "e1", From: "fe:fetchUser", To: "be:getUser",
		Type: graph.EdgeTypeHTTPCall, Label: "GET /api/user",
		Confidence: graph.ConfidenceStatic, Method: "GET", Path: "/api/user",
	})
	idx.AddEdge(&graph.Edge{
		ID: "e2", From: "be:getUser", To: "be:queryDB",
		Type: graph.EdgeTypeCalls,
	})

	return idx
}

func TestBuild_Debug(t *testing.T) {
	idx := fixtureIndex()
	result := ctx.Build(idx, "be:getUser", "debug", 5)
	require.NotNil(t, result)

	assert.Equal(t, "be:getUser", result.Target.ID)
	assert.Equal(t, "debug", result.Task)

	// Upstream: fetchUser calls getUser
	require.Len(t, result.Upstream, 1)
	assert.Equal(t, "fe:fetchUser", result.Upstream[0].ID)
	assert.Equal(t, "http_call", result.Upstream[0].EdgeType)

	// Downstream: getUser calls queryDB
	require.Len(t, result.Downstream, 1)
	assert.Equal(t, "be:queryDB", result.Downstream[0].ID)
	assert.Equal(t, "calls", result.Downstream[0].EdgeType)
}

func TestBuild_Impact(t *testing.T) {
	idx := fixtureIndex()
	result := ctx.Build(idx, "be:getUser", "impact", 0)
	require.NotNil(t, result)

	// impact = backward only
	require.Len(t, result.Upstream, 1)
	assert.Equal(t, "fe:fetchUser", result.Upstream[0].ID)
	assert.Empty(t, result.Downstream)
}

func TestBuild_Generate(t *testing.T) {
	idx := fixtureIndex()
	result := ctx.Build(idx, "be:getUser", "generate", 3)
	require.NotNil(t, result)

	// generate = forward only
	assert.Empty(t, result.Upstream)
	require.Len(t, result.Downstream, 1)
	assert.Equal(t, "be:queryDB", result.Downstream[0].ID)
}

func TestBuild_CrossService(t *testing.T) {
	idx := fixtureIndex()
	result := ctx.Build(idx, "be:getUser", "debug", 5)
	require.NotNil(t, result)

	// fe:fetchUser -> be:getUser is cross-service; should appear in cross_service
	require.Len(t, result.CrossService, 1)
	cs := result.CrossService[0]
	assert.Equal(t, "frontend", cs.FromService)
	assert.Equal(t, "backend", cs.ToService)
	assert.Equal(t, graph.ConfidenceStatic, cs.Confidence)
	assert.Equal(t, "GET", cs.Method)
	assert.Equal(t, "/api/user", cs.Path)
}

func TestBuild_UnknownNode(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	result := ctx.Build(idx, "nonexistent", "debug", 5)
	require.NotNil(t, result)
	assert.Nil(t, result.Target)
	assert.Empty(t, result.Upstream)
	assert.Empty(t, result.Downstream)
}

func TestBuild_TotalCounts(t *testing.T) {
	idx := fixtureIndex()
	result := ctx.Build(idx, "be:getUser", "debug", 5)
	require.NotNil(t, result)

	// 2 trace nodes (fetchUser + queryDB) + 1 target = 3
	assert.Equal(t, 3, result.TotalNodes)
}

func TestBuild_JSONCarriesNodeAndEdgeMeta(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	fn := &graph.Node{ID: "agent:upload", Type: graph.NodeTypeFunction, Label: "UploadReport", Service: "dsw-agent"}
	s3 := &graph.Node{ID: "agent:s3", Type: graph.NodeTypeExternalService, Label: "PutObject", Service: "dsw-agent",
		Meta: map[string]string{"package": "github.com/aws/aws-sdk-go", "resolved_version": "1.55.8", "cloud_service": "s3"}}
	idx.AddNode(fn)
	idx.AddNode(s3)
	idx.AddEdge(&graph.Edge{ID: "e1", From: fn.ID, To: s3.ID, Type: graph.EdgeTypeCloudCall,
		Confidence: graph.ConfidenceInferred, Meta: map[string]string{"via": "sdk"}})

	result := ctx.Build(idx, fn.ID, "debug", 5)
	require.NotNil(t, result)
	require.Len(t, result.Downstream, 1)

	d := result.Downstream[0]
	assert.Equal(t, "github.com/aws/aws-sdk-go", d.Meta["package"],
		"context JSON must answer 'what breaks if I bump aws-sdk-go to v2'")
	assert.Equal(t, "1.55.8", d.Meta["resolved_version"])
	assert.Equal(t, graph.ConfidenceInferred, d.Confidence)
	assert.Equal(t, "sdk", d.EdgeMeta["via"])
}
