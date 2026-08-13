package mcpserver

import (
	"context"
	"fmt"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// fixtureCrossServiceFlow builds a five-hop, three-service flow:
//
//	browser:fetchOrder --http_call(verified)--> api:createOrder
//	  --calls(candidate)--> api:publishOrderCreated
//	  --publishes(candidate, the "dynamic" grep-worthy hop)--> worker:consumeOrderCreated
//	  --calls(candidate)--> worker:saveOrder
//	  --persists(candidate)--> worker:orders_table
func fixtureCrossServiceFlow() (*fakeStore, *graph.AdjacencyIndex) {
	nodes := []*graph.Node{
		{ID: "browser:fetchOrder", Type: graph.NodeTypeHTTPClient, Label: "fetchOrder", Service: "browser", File: "app.js", Line: 5, Language: "javascript"},
		{ID: "api:createOrder", Type: graph.NodeTypeHTTPHandler, Label: "POST /orders", Service: "api", File: "handler.go", Line: 10, Language: "go", Meta: map[string]string{"method": "POST", "path": "/orders"}},
		{ID: "api:publishOrderCreated", Type: graph.NodeTypeFunction, Label: "publishOrderCreated", Service: "api", File: "publish.go", Line: 20, Language: "go"},
		{ID: "worker:consumeOrderCreated", Type: graph.NodeTypeSubscriber, Label: "consumeOrderCreated", Service: "worker", File: "consumer.go", Line: 8, Language: "go"},
		{ID: "worker:saveOrder", Type: graph.NodeTypeFunction, Label: "saveOrder", Service: "worker", File: "db.go", Line: 15, Language: "go"},
		{ID: "worker:orders_table", Type: graph.NodeTypeDatastore, Label: "orders_table", Service: "worker", File: "db.go", Line: 1, Language: "go"},
	}
	idx := graph.NewAdjacencyIndex()
	for _, n := range nodes {
		idx.AddNode(n)
	}
	idx.AddEdge(&graph.Edge{ID: "f1", From: "browser:fetchOrder", To: "api:createOrder", Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceStatic, VerificationState: graph.StateVerified})
	idx.AddEdge(&graph.Edge{ID: "f2", From: "api:createOrder", To: "api:publishOrderCreated", Type: graph.EdgeTypeCalls, VerificationState: graph.StateCandidate})
	idx.AddEdge(&graph.Edge{ID: "f3", From: "api:publishOrderCreated", To: "worker:consumeOrderCreated", Type: graph.EdgeTypePublishes, VerificationState: graph.StateCandidate})
	idx.AddEdge(&graph.Edge{ID: "f4", From: "worker:consumeOrderCreated", To: "worker:saveOrder", Type: graph.EdgeTypeCalls, VerificationState: graph.StateCandidate})
	idx.AddEdge(&graph.Edge{ID: "f5", From: "worker:saveOrder", To: "worker:orders_table", Type: graph.EdgeTypePersists, VerificationState: graph.StateCandidate})

	store := &fakeStore{
		nodes: nodes,
		unresolved: []graph.UnresolvedRef{
			{Service: "api", File: "publish.go", Line: 25, Name: "dynTopic", Kind: "dynamic_topic"},
			{Service: "unrelated", File: "unrelated.go", Line: 5, Name: "other", Kind: "call_ref"},
		},
	}
	return store, idx
}

func TestFlowsTool_CrossServiceGolden(t *testing.T) {
	store, idx := fixtureCrossServiceFlow()
	cs := connect(t, store, idx)

	var out flowsOutput
	callJSON(t, cs, "flows", map[string]any{"target": "browser:fetchOrder", "direction": "downstream", "detail": true}, &out)

	require.Len(t, out.Flows, 1, "single linear path from root to the DB write")
	flow := out.Flows[0]
	require.Len(t, flow, 5)

	wantServices := []string{"api", "api", "worker", "worker", "worker"}
	wantEdges := []graph.EdgeType{graph.EdgeTypeHTTPCall, graph.EdgeTypeCalls, graph.EdgeTypePublishes, graph.EdgeTypeCalls, graph.EdgeTypePersists}
	wantVerification := []string{graph.StateVerified, graph.StateCandidate, graph.StateCandidate, graph.StateCandidate, graph.StateCandidate}
	for i, h := range flow {
		assert.Equal(t, wantServices[i], h.Service, "hop %d service", i)
		assert.Equal(t, wantEdges[i], h.Edge, "hop %d edge", i)
		assert.Equal(t, wantVerification[i], h.Verification, "hop %d verification", i)
	}

	// Coverage: 1 verified + 4 candidate, zero observed_only_gap.
	assert.Equal(t, 1, out.Coverage.Verified)
	assert.Equal(t, 4, out.Coverage.Candidate)
	assert.Equal(t, 0, out.Coverage.ObservedOnlyGap)

	// The dynamic_topic ledger entry in publish.go (a file the flow touches)
	// is the one grep-worthy residue; the unrelated.go entry must not appear.
	require.Len(t, out.Coverage.Unresolved, 1)
	assert.Equal(t, "dynTopic", out.Coverage.Unresolved[0].Name)
	require.Len(t, out.Unresolved, 1)
	assert.Equal(t, "dynTopic", out.Unresolved[0].Name)
}

func TestFlowsTool_Determinism(t *testing.T) {
	store, idx := fixtureCrossServiceFlow()

	run := func() string {
		cs := connect(t, store, idx)
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "flows",
			Arguments: map[string]any{"target": "browser:fetchOrder"},
		})
		require.NoError(t, err)
		text := res.Content[0].(*mcp.TextContent).Text
		return text
	}

	a := run()
	b := run()
	assert.Equal(t, a, b, "two runs on the same input must be byte-identical")
}

func TestFlowsTool_MinVerificationFiltersFlows(t *testing.T) {
	store, idx := fixtureCrossServiceFlow()
	cs := connect(t, store, idx)

	var out flowsOutput
	callJSON(t, cs, "flows", map[string]any{
		"target":           "browser:fetchOrder",
		"min_verification": "verified",
		"detail":           true,
	}, &out)

	// Every flow contains a candidate hop past the first (calls to
	// publishOrderCreated) — the whole flow must be dropped.
	assert.Empty(t, out.Flows, "flow with any non-verified hop must be filtered")
}

func TestFlowsTool_HTTPRouteTarget(t *testing.T) {
	store, idx := fixtureCrossServiceFlow()
	cs := connect(t, store, idx)

	var out flowsOutput
	callJSON(t, cs, "flows", map[string]any{"target": "POST /orders", "detail": true}, &out)

	require.Len(t, out.Flows, 1)
	assert.Equal(t, "api:publishOrderCreated", out.Flows[0][0].To)
}

func TestFlowsTool_UpstreamDirection(t *testing.T) {
	store, idx := fixtureCrossServiceFlow()
	cs := connect(t, store, idx)

	var out flowsOutput
	callJSON(t, cs, "flows", map[string]any{"target": "worker:orders_table", "direction": "upstream", "detail": true}, &out)

	require.Len(t, out.Flows, 1)
	flow := out.Flows[0]
	require.Len(t, flow, 5)
	// Upstream hops still read in real edge direction (From -> To).
	assert.Equal(t, "browser:fetchOrder", flow[0].From)
	assert.Equal(t, "worker:saveOrder", flow[len(flow)-1].From)
	assert.Equal(t, "worker:orders_table", flow[len(flow)-1].To)
}

// fixtureHubFanout builds one node ("api:dispatch") with 12 "calls" edges
// into 12 distinct handlers all within one service ("worker") — over
// hubFanoutThreshold (8) — to exercise the per-service rollup path instead
// of dumping every branch.
func fixtureHubFanout() (*fakeStore, *graph.AdjacencyIndex) {
	root := &graph.Node{ID: "api:dispatch", Type: graph.NodeTypeFunction, Label: "dispatch", Service: "api", File: "dispatch.go", Line: 1}
	idx := graph.NewAdjacencyIndex()
	idx.AddNode(root)
	nodes := []*graph.Node{root}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("worker:handler%02d", i)
		n := &graph.Node{ID: id, Type: graph.NodeTypeFunction, Label: fmt.Sprintf("handler%02d", i), Service: "worker", File: "handler.go", Line: i + 1}
		idx.AddNode(n)
		nodes = append(nodes, n)
		idx.AddEdge(&graph.Edge{ID: "e" + id, From: root.ID, To: n.ID, Type: graph.EdgeTypeCalls, VerificationState: graph.StateCandidate})
	}
	store := &fakeStore{nodes: nodes}
	return store, idx
}

func TestFlowsTool_HubFanoutRollsUp(t *testing.T) {
	store, idx := fixtureHubFanout()
	cs := connect(t, store, idx)

	var out flowsOutput
	callJSON(t, cs, "flows", map[string]any{"target": "api:dispatch", "detail": true}, &out)

	require.Len(t, out.Flows, 1, "fan-out beyond the threshold collapses into one rollup flow")
	flow := out.Flows[0]
	require.Len(t, flow, 1)
	assert.Equal(t, "rollup", flow[0].Verification)
	assert.Contains(t, flow[0].To, "12")
	// All 12 branch edges still counted in the coverage tally (bug-class #12:
	// the rollup does not silently drop the fanned-out edges).
	assert.Equal(t, 12, out.Coverage.Candidate)
}

func TestEntrypointsTool_FiltersByServiceAndType(t *testing.T) {
	store, idx := fixtureCrossServiceFlow()
	cs := connect(t, store, idx)

	var out entrypointsOutput
	callJSON(t, cs, "entrypoints", map[string]any{"service": "worker"}, &out)

	require.Len(t, out.Entrypoints, 1)
	assert.Equal(t, "worker:consumeOrderCreated", out.Entrypoints[0].ID)
	assert.Equal(t, "subscriber", out.Entrypoints[0].Type)

	var outAll entrypointsOutput
	callJSON(t, cs, "entrypoints", map[string]any{}, &outAll)
	// http_handler (createOrder) + subscriber (consumeOrderCreated); functions
	// and datastore nodes are not entrypoints.
	require.Len(t, outAll.Entrypoints, 2)

	var outType entrypointsOutput
	callJSON(t, cs, "entrypoints", map[string]any{"type": "http_handler"}, &outType)
	require.Len(t, outType.Entrypoints, 1)
	assert.Equal(t, "api:createOrder", outType.Entrypoints[0].ID)
	assert.Equal(t, "POST", outType.Entrypoints[0].Method)
	assert.Equal(t, "/orders", outType.Entrypoints[0].Path)
}

func TestEntrypointsTool_FeatureFilter(t *testing.T) {
	store, idx := fixtureCrossServiceFlow()
	cs := connect(t, store, idx)

	var out entrypointsOutput
	callJSON(t, cs, "entrypoints", map[string]any{"feature": "consume"}, &out)
	require.Len(t, out.Entrypoints, 1)
	assert.Equal(t, "worker:consumeOrderCreated", out.Entrypoints[0].ID)
}

func TestEntrypointsTool_Determinism(t *testing.T) {
	store, idx := fixtureCrossServiceFlow()

	run := func() string {
		cs := connect(t, store, idx)
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "entrypoints", Arguments: map[string]any{}})
		require.NoError(t, err)
		return res.Content[0].(*mcp.TextContent).Text
	}
	assert.Equal(t, run(), run())
}

func TestResolveTool_ReturnsCandidatesAndRoot(t *testing.T) {
	store, idx := fixtureAmbiguous()
	cs := connect(t, store, idx)

	var out struct {
		Root             *graph.Node              `json:"root"`
		Candidates       []map[string]any         `json:"candidates"`
		TargetCandidates []graph.TargetCandidate `json:"target_candidates"`
	}
	callJSON(t, cs, "resolve", map[string]any{"query": "Login"}, &out)

	require.NotNil(t, out.Root)
	assert.NotEmpty(t, out.Candidates)
	assert.Len(t, out.TargetCandidates, 2, "ambiguous exact match must populate target_candidates")
}

func TestResolveTool_TargetServiceFilter(t *testing.T) {
	store, idx := fixtureAmbiguous()
	cs := connect(t, store, idx)

	var out struct {
		Root *graph.Node `json:"root"`
	}
	callJSON(t, cs, "resolve", map[string]any{"query": "Login", "target_service": "server"}, &out)
	require.NotNil(t, out.Root)
	assert.Equal(t, "srv:Login", out.Root.ID)
}

func TestResolveTool_EmptyQueryIsError(t *testing.T) {
	store, idx := fixtureCrossServiceFlow()
	cs := connect(t, store, idx)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "resolve", Arguments: map[string]any{"query": ""}})
	require.NoError(t, err)
	assert.True(t, res.IsError)
}
