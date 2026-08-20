package context_test

import (
	"encoding/json"
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
	result := ctx.Build(idx, "be:getUser", "debug", 5, false, 0, nil)
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
	result := ctx.Build(idx, "be:getUser", "impact", 0, false, 0, nil)
	require.NotNil(t, result)

	// impact = backward only
	require.Len(t, result.Upstream, 1)
	assert.Equal(t, "fe:fetchUser", result.Upstream[0].ID)
	assert.Empty(t, result.Downstream)
}

func TestBuild_Generate(t *testing.T) {
	idx := fixtureIndex()
	result := ctx.Build(idx, "be:getUser", "generate", 3, false, 0, nil)
	require.NotNil(t, result)

	// generate = forward only
	assert.Empty(t, result.Upstream)
	require.Len(t, result.Downstream, 1)
	assert.Equal(t, "be:queryDB", result.Downstream[0].ID)
}

func TestBuild_CrossService(t *testing.T) {
	idx := fixtureIndex()
	result := ctx.Build(idx, "be:getUser", "debug", 5, false, 0, nil)
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
	result := ctx.Build(idx, "nonexistent", "debug", 5, false, 0, nil)
	require.NotNil(t, result)
	assert.Nil(t, result.Target)
	assert.Empty(t, result.Upstream)
	assert.Empty(t, result.Downstream)
}

func TestBuild_TotalCounts(t *testing.T) {
	idx := fixtureIndex()
	result := ctx.Build(idx, "be:getUser", "debug", 5, false, 0, nil)
	require.NotNil(t, result)

	// 2 trace nodes (fetchUser + queryDB) + 1 target = 3
	assert.Equal(t, 3, result.TotalNodes)
}

func TestBuild_JSONCarriesNodeAndEdgeMeta(t *testing.T) {
	idx := graph.NewAdjacencyIndex()
	fn := &graph.Node{ID: "agent:upload", Type: graph.NodeTypeFunction, Label: "UploadReport", Service: "svc-c-agent"}
	s3 := &graph.Node{ID: "agent:s3", Type: graph.NodeTypeExternalService, Label: "PutObject", Service: "svc-c-agent",
		Meta: map[string]string{"package": "github.com/aws/aws-sdk-go", "resolved_version": "1.55.8", "cloud_service": "s3"}}
	idx.AddNode(fn)
	idx.AddNode(s3)
	idx.AddEdge(&graph.Edge{ID: "e1", From: fn.ID, To: s3.ID, Type: graph.EdgeTypeCloudCall,
		Confidence: graph.ConfidenceInferred, Meta: map[string]string{"via": "sdk"}})

	result := ctx.Build(idx, fn.ID, "debug", 5, false, 0, nil)
	require.NotNil(t, result)
	require.Len(t, result.Downstream, 1)

	d := result.Downstream[0]
	assert.Equal(t, "github.com/aws/aws-sdk-go", d.Meta["package"],
		"context JSON must answer 'what breaks if I bump aws-sdk-go to v2'")
	assert.Equal(t, "1.55.8", d.Meta["resolved_version"])
	assert.Equal(t, graph.ConfidenceInferred, d.Confidence)
	assert.Equal(t, "sdk", d.EdgeMeta["via"])
}

func TestAttachUnresolved_ScopedToTraversedFiles(t *testing.T) {
	idx := fixtureIndex()
	result := ctx.Build(idx, "be:getUser", "debug", 5, false, 0, nil)
	require.NotNil(t, result)

	result.AttachUnresolved([]graph.UnresolvedRef{
		{Service: "backend", File: "db.go", Line: 41, Name: "dynDispatch", Kind: "call_ref"},
		{Service: "backend", File: "unrelated.go", Line: 5, Name: "other", Kind: "call_ref"},
	})

	// db.go is downstream of the target; unrelated.go was never traversed.
	require.Len(t, result.Unresolved, 1)
	assert.Equal(t, "dynDispatch", result.Unresolved[0].Name)
	assert.Contains(t, result.UnresolvedNote, "verify this 1 unresolved reference manually")
}

// noiseFixture builds a root with 5 outgoing edges, one per Tier NV noise
// class plus one plain behavioral edge: a -calls-> behavioral (NoiseNone),
// a -calls(via=rails_filter)-> filtered, a -inherits-> mixin,
// a -contains-> contained, a -calls-> rendered (an element node).
func noiseFixture() *graph.AdjacencyIndex {
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(&graph.Node{ID: "a", Label: "A", Service: "s", Type: graph.NodeTypeFunction})
	idx.AddNode(&graph.Node{ID: "behavioral", Label: "Behavioral", Service: "s", Type: graph.NodeTypeFunction})
	idx.AddNode(&graph.Node{ID: "filtered", Label: "Filtered", Service: "s", Type: graph.NodeTypeFunction})
	idx.AddNode(&graph.Node{ID: "mixin", Label: "Mixin", Service: "s", Type: graph.NodeTypeFunction})
	idx.AddNode(&graph.Node{ID: "contained", Label: "Contained", Service: "s", Type: graph.NodeTypeFunction})
	idx.AddNode(&graph.Node{ID: "rendered", Label: "Rendered", Service: "s", Type: graph.NodeTypeElement})

	idx.AddEdge(&graph.Edge{ID: "e1", From: "a", To: "behavioral", Type: graph.EdgeTypeCalls})
	idx.AddEdge(&graph.Edge{ID: "e2", From: "a", To: "filtered", Type: graph.EdgeTypeCalls, Meta: map[string]string{"via": "rails_filter"}})
	idx.AddEdge(&graph.Edge{ID: "e3", From: "a", To: "mixin", Type: graph.EdgeTypeInherits})
	idx.AddEdge(&graph.Edge{ID: "e4", From: "a", To: "contained", Type: graph.EdgeTypeContains})
	idx.AddEdge(&graph.Edge{ID: "e5", From: "a", To: "rendered", Type: graph.EdgeTypeCalls})
	return idx
}

func TestBuild_NoiseFiltering_TaskDebugDefaultHidesAllFourClasses(t *testing.T) {
	include := graph.DefaultNoiseInclude("debug")
	result := ctx.Build(noiseFixture(), "a", "generate", 1, false, 0, include)
	require.NotNil(t, result)

	require.Len(t, result.Downstream, 1)
	assert.Equal(t, "behavioral", result.Downstream[0].ID)

	assert.Equal(t, map[graph.NoiseClass]int{
		graph.NoiseFilterChain: 1,
		graph.NoiseMixin:       1,
		graph.NoiseContainment: 1,
		graph.NoiseRenderTree:  1,
	}, result.HiddenByClass)
}

func TestBuild_NoiseFiltering_TaskGenerateDefaultShowsRenderTreeOnly(t *testing.T) {
	include := graph.DefaultNoiseInclude("generate")
	result := ctx.Build(noiseFixture(), "a", "generate", 1, false, 0, include)
	require.NotNil(t, result)

	require.Len(t, result.Downstream, 2)
	var ids []string
	for _, n := range result.Downstream {
		ids = append(ids, n.ID)
	}
	assert.Contains(t, ids, "behavioral")
	assert.Contains(t, ids, "rendered")

	assert.Equal(t, map[graph.NoiseClass]int{
		graph.NoiseFilterChain: 1,
		graph.NoiseMixin:       1,
		graph.NoiseContainment: 1,
	}, result.HiddenByClass)
}

func TestBuild_NoiseFiltering_ExplicitIncludeOverridesTaskDefaultBothDirections(t *testing.T) {
	// task=generate would default to showing render_tree, but an explicit
	// empty include-set must override that default entirely (never merge),
	// hiding render_tree too.
	result := ctx.Build(noiseFixture(), "a", "generate", 1, false, 0, graph.NoiseInclude{})
	require.NotNil(t, result)
	require.Len(t, result.Downstream, 1)
	assert.Equal(t, "behavioral", result.Downstream[0].ID)
	assert.Equal(t, 1, result.HiddenByClass[graph.NoiseRenderTree])

	// task=debug would default to hiding everything, but an explicit include
	// must still surface the requested class.
	result2 := ctx.Build(noiseFixture(), "a", "debug", 1, false, 0, graph.NoiseInclude{graph.NoiseMixin: true})
	require.NotNil(t, result2)
	var ids []string
	for _, n := range result2.Downstream {
		ids = append(ids, n.ID)
	}
	assert.Contains(t, ids, "behavioral")
	assert.Contains(t, ids, "mixin")
	assert.Equal(t, 1, result2.HiddenByClass[graph.NoiseRenderTree])
	assert.Equal(t, 0, result2.HiddenByClass[graph.NoiseMixin])
}

func TestBuild_UnresolvedDefaultsToEmptyNotNull(t *testing.T) {
	idx := fixtureIndex()
	result := ctx.Build(idx, "be:getUser", "debug", 5, false, 0, nil)
	require.NotNil(t, result)

	data, err := json.Marshal(result)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"unresolved":[]`)
	assert.NotContains(t, string(data), "unresolved_note")
}
