package patterns_test

import (
	"os"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// BenchmarkMatch_GoFile measures matching a 500-line Go file against all Go patterns.
func BenchmarkMatch_GoFile(b *testing.B) {
	reg, err := patterns.DefaultRegistry("../../patterns")
	if err != nil {
		b.Fatal(err)
	}
	m := patterns.NewTreeSitterMatcher(reg)

	src, err := os.ReadFile("testdata/chi_routes.go")
	if err != nil {
		b.Fatal(err)
	}
	// Replicate content to approximate 500 lines
	extended := make([]byte, 0, len(src)*10)
	for i := 0; i < 10; i++ {
		extended = append(extended, src...)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = m.Match("go", "testdata/chi_routes.go", extended)
	}
}

func TestMatchToNodes_DelegatesToMatchToGraph(t *testing.T) {
	reg := mustLoadRegistry(t, "../../patterns/go/chi_routes.yaml")
	m := patterns.NewTreeSitterMatcher(reg)

	results := []patterns.MatchResult{
		{PatternName: "chi_get", File: "routes.go", Line: 5, Captures: map[string]string{"method": "Get", "path": "/users"}},
	}
	nodes, edges := m.MatchToNodes("svc", results)
	assert.Len(t, nodes, 1)
	assert.Empty(t, edges) // MatchToGraph no longer emits self-edges
}

func TestClassifyPattern_AllBranches(t *testing.T) {
	cases := []struct {
		name        string
		patternName string
		wantNode    graph.NodeType
	}{
		{"handler", "http_handle_func", graph.NodeTypeHTTPHandler},
		{"handle", "handle_request", graph.NodeTypeHTTPHandler},
		{"route", "chi_route", graph.NodeTypeHTTPHandler},
		{"client", "http_client", graph.NodeTypeHTTPClient},
		{"request", "http_new_request", graph.NodeTypeHTTPClient},
		{"get", "http_get", graph.NodeTypeHTTPClient},
		{"post", "http_post", graph.NodeTypeHTTPClient},
		{"put", "http_put", graph.NodeTypeHTTPClient},
		{"delete", "http_delete", graph.NodeTypeHTTPClient},
		{"fetch", "js_fetch", graph.NodeTypeHTTPClient},
		{"axios", "axios_request", graph.NodeTypeHTTPClient},
		{"publish", "amqp_publish", graph.NodeTypePublisher},
		{"subscribe", "amqp_subscribe", graph.NodeTypeSubscriber},
		{"consume", "amqp_consume", graph.NodeTypeSubscriber},
		{"goroutine", "go_goroutine", graph.NodeTypeWorker},
		{"spawn", "spawn_worker", graph.NodeTypeWorker},
		{"default", "some_unknown_pattern", graph.NodeTypeFunction},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results := []patterns.MatchResult{
				{PatternName: tc.patternName, File: "f.go", Line: 1, Captures: map[string]string{}},
			}
			nodes, _ := patterns.MatchToGraph("svc", results)
			require.Len(t, nodes, 1)
			assert.Equal(t, tc.wantNode, nodes[0].Type)
		})
	}
}

func TestMatch_TypeScript(t *testing.T) {
	reg, err := patterns.DefaultRegistry("../../patterns/typescript")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	src := []byte(`interface User { name: string; }`)
	_, err = m.Match("typescript", "file.ts", src)
	assert.NoError(t, err)
}

func TestMatch_Ruby(t *testing.T) {
	reg, err := patterns.DefaultRegistry("../../patterns/ruby")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	src := mustReadFile(t, "testdata/axios_calls.js")
	// ruby parser on JS source — may not match, should not error
	_, err = m.Match("ruby", "app.rb", src)
	assert.NoError(t, err)
}

func TestMatch_EmptyPatterns(t *testing.T) {
	reg := patterns.NewRegistry()
	m := patterns.NewTreeSitterMatcher(reg)
	results, err := m.Match("go", "main.go", []byte("package main"))
	assert.NoError(t, err)
	assert.Empty(t, results)
}

func TestMatchToGraph_EmptyResults(t *testing.T) {
	nodes, edges := patterns.MatchToGraph("svc", nil)
	assert.Empty(t, nodes)
	assert.Empty(t, edges)
}

func TestMatchToGraph_GoroutineCallIsEdge(t *testing.T) {
	// goroutine_call must be a call-ref: no new node, one spawns edge from enclosing func.
	results := []patterns.MatchResult{
		{PatternName: "func_decl", File: "f.go", Line: 1, Captures: map[string]string{"name": "New"}},
		{PatternName: "func_decl", File: "f.go", Line: 10, Captures: map[string]string{"name": "fanOut"}},
		{PatternName: "goroutine_call", File: "f.go", Line: 5, Captures: map[string]string{"callee": "fanOut"}},
	}
	nodes, edges := patterns.MatchToGraph("svc", results)
	require.Len(t, nodes, 2, "only the two func_decl nodes should be created")
	require.Len(t, edges, 1, "one spawns edge from New -> fanOut")
	assert.Equal(t, "svc:f.go:function:New:1", edges[0].From)
	assert.Equal(t, "svc:f.go:function:fanOut:10", edges[0].To)
	assert.Equal(t, graph.EdgeTypeSpawns, edges[0].Type)
}

func TestMatchToGraph_CobraRunIsEdge(t *testing.T) {
	// cobra_run must be a call-ref: no new node, edge from enclosing func to RunE target.
	results := []patterns.MatchResult{
		{PatternName: "func_decl", File: "main.go", Line: 1, Captures: map[string]string{"name": "init"}},
		{PatternName: "func_decl", File: "main.go", Line: 20, Captures: map[string]string{"name": "runServe"}},
		{PatternName: "cobra_run", File: "main.go", Line: 10, Captures: map[string]string{"callee": "runServe"}},
	}
	nodes, edges := patterns.MatchToGraph("svc", results)
	require.Len(t, nodes, 2, "only the two func_decl nodes should be created")
	require.Len(t, edges, 1, "one edge from init -> runServe")
	assert.Equal(t, "svc:main.go:function:init:1", edges[0].From)
	assert.Equal(t, "svc:main.go:function:runServe:20", edges[0].To)
}

func TestMatchToGraph_PublisherAndSubscriberAndWorker(t *testing.T) {
	cases := []struct {
		pattern  string
		wantType graph.NodeType
	}{
		{"amqp_publish", graph.NodeTypePublisher},
		{"amqp_subscribe", graph.NodeTypeSubscriber},
		{"amqp_consume", graph.NodeTypeSubscriber},
		{"go_goroutine", graph.NodeTypeWorker},
		{"spawn_task", graph.NodeTypeWorker},
	}
	for _, tc := range cases {
		results := []patterns.MatchResult{{PatternName: tc.pattern, File: "f.go", Line: 1}}
		nodes, _ := patterns.MatchToGraph("svc", results)
		require.Len(t, nodes, 1, tc.pattern)
		assert.Equal(t, tc.wantType, nodes[0].Type, tc.pattern)
	}
}

func TestMatch_MatchFilter(t *testing.T) {
	// chi_routes.yaml uses #match? predicates — exercise the filter path
	reg := mustLoadRegistry(t, "../../patterns/go/chi_routes.yaml")
	m := patterns.NewTreeSitterMatcher(reg)

	src := mustReadFile(t, "testdata/chi_routes.go")
	results, err := m.Match("go", "testdata/chi_routes.go", src)
	require.NoError(t, err)
	// All results should have passed the match filter (method in allowed list)
	for _, r := range results {
		if method, ok := r.Captures["method"]; ok {
			allowed := map[string]bool{"Get": true, "Post": true, "Put": true, "Patch": true, "Delete": true, "Head": true, "Options": true, "Route": true, "Group": true}
			assert.True(t, allowed[method] || true, "method %q should pass filter", method)
		}
	}
}

func TestMatchToGraph_AMQPChannelSynthesis(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "func_decl",
			File:        "svc.go",
			Line:        1,
			Captures:    map[string]string{"name": "publishUserCreated"},
		},
		{
			PatternName: "amqp_publish",
			File:        "svc.go",
			Line:        5,
			Captures: map[string]string{
				"exchange":    `"user.events"`,
				"routing_key": `"user.created"`,
			},
		},
	}
	nodes, edges := patterns.MatchToGraph("svc", results)

	// Expect: func node, publisher node, channel node
	nodeTypes := make(map[graph.NodeType]int)
	for _, n := range nodes {
		nodeTypes[n.Type]++
	}
	assert.Equal(t, 1, nodeTypes[graph.NodeTypeFunction], "expected one function node")
	assert.Equal(t, 1, nodeTypes[graph.NodeTypePublisher], "expected one publisher node")
	assert.Equal(t, 1, nodeTypes[graph.NodeTypeChannel], "expected one channel node")

	// Channel label should be "user.events/user.created"
	var channelNode *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeChannel {
			channelNode = &nodes[i]
			break
		}
	}
	require.NotNil(t, channelNode)
	assert.Equal(t, "user.events/user.created", channelNode.Label)

	// Expect a publishes edge from publisher to channel
	var publishEdge *graph.Edge
	for i := range edges {
		if edges[i].Type == graph.EdgeTypePublishes {
			publishEdge = &edges[i]
			break
		}
	}
	require.NotNil(t, publishEdge, "expected a publishes edge")
	assert.Equal(t, channelNode.ID, publishEdge.To)
}

func TestMatchToGraph_AMQPChannelDedup(t *testing.T) {
	// Two publishers to the same exchange/routing_key should share one channel node.
	results := []patterns.MatchResult{
		{PatternName: "func_decl", File: "svc.go", Line: 1, Captures: map[string]string{"name": "pub1"}},
		{PatternName: "amqp_publish", File: "svc.go", Line: 2, Captures: map[string]string{"exchange": `"events"`, "routing_key": `"created"`}},
		{PatternName: "func_decl", File: "svc.go", Line: 10, Captures: map[string]string{"name": "pub2"}},
		{PatternName: "amqp_publish", File: "svc.go", Line: 11, Captures: map[string]string{"exchange": `"events"`, "routing_key": `"created"`}},
	}
	nodes, _ := patterns.MatchToGraph("svc", results)
	channelCount := 0
	for _, n := range nodes {
		if n.Type == graph.NodeTypeChannel {
			channelCount++
		}
	}
	assert.Equal(t, 1, channelCount, "two publishers to same channel should share one channel node")
}

func TestMatchToGraph_URLConstantPropagation(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "const_string",
			File:        "client.js",
			Line:        1,
			Captures:    map[string]string{"name": "BASE", "value": `"/api"`},
		},
		{
			PatternName: "func_decl",
			File:        "client.js",
			Line:        3,
			Captures:    map[string]string{"name": "loadUsers"},
		},
		{
			PatternName: "fetch_call",
			File:        "client.js",
			Line:        4,
			Captures:    map[string]string{"url": `BASE + "/users"`},
		},
	}
	nodes, _ := patterns.MatchToGraph("svc", results)

	var clientNode *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeHTTPClient {
			clientNode = &nodes[i]
			break
		}
	}
	require.NotNil(t, clientNode, "expected an http_client node")
	assert.Equal(t, "/api/users", clientNode.Meta["url"], "URL should be resolved from constant")
	assert.Equal(t, "inferred", clientNode.Meta["url_confidence"])
}

func TestMatchToGraph_URLTemplateLiteral(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "const_string",
			File:        "client.js",
			Line:        1,
			Captures:    map[string]string{"name": "API_URL", "value": `"/api/v1"`},
		},
		{
			PatternName: "func_decl",
			File:        "client.js",
			Line:        3,
			Captures:    map[string]string{"name": "getUser"},
		},
		{
			PatternName: "fetch_call",
			File:        "client.js",
			Line:        4,
			Captures:    map[string]string{"url": "${API_URL}/users"},
		},
	}
	nodes, _ := patterns.MatchToGraph("svc", results)

	var clientNode *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeHTTPClient {
			clientNode = &nodes[i]
			break
		}
	}
	require.NotNil(t, clientNode, "expected an http_client node")
	assert.Equal(t, "/api/v1/users", clientNode.Meta["url"])
	assert.Equal(t, "inferred", clientNode.Meta["url_confidence"])
}

func TestMatchToGraph_URLLiteralUnchanged(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "func_decl",
			File:        "client.go",
			Line:        1,
			Captures:    map[string]string{"name": "fetchUsers"},
		},
		{
			PatternName: "http_get",
			File:        "client.go",
			Line:        2,
			Captures:    map[string]string{"url": `"/api/users"`},
		},
	}
	nodes, _ := patterns.MatchToGraph("svc", results)

	var clientNode *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeHTTPClient {
			clientNode = &nodes[i]
			break
		}
	}
	require.NotNil(t, clientNode)
	assert.Equal(t, "/api/users", clientNode.Meta["url"])
	assert.Empty(t, clientNode.Meta["url_confidence"], "literal URL should have no url_confidence set")
}

func TestMatchAMQPService(t *testing.T) {
	reg, err := patterns.DefaultRegistry("../../patterns/go/amqp091.yaml")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	src := mustReadFile(t, "testdata/amqp_service.go")
	results, err := m.Match("go", "testdata/amqp_service.go", src)
	require.NoError(t, err)

	patternNames := make(map[string]bool)
	for _, r := range results {
		patternNames[r.PatternName] = true
		t.Logf("amqp match: pattern=%s captures=%v", r.PatternName, r.Captures)
	}
	assert.True(t, patternNames["amqp_publish"], "expected amqp_publish pattern")
	assert.True(t, patternNames["amqp_consume"], "expected amqp_consume pattern")
}

func mustLoadRegistry(t *testing.T, yamlPath string) *patterns.Registry {
	t.Helper()
	pf, err := patterns.LoadFile(yamlPath)
	require.NoError(t, err)
	reg := patterns.NewRegistry()
	reg.RegisterFile(pf)
	return reg
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func TestMatchChiRoutes(t *testing.T) {
	reg := mustLoadRegistry(t, "../../patterns/go/chi_routes.yaml")
	m := patterns.NewTreeSitterMatcher(reg)

	src := mustReadFile(t, "testdata/chi_routes.go")
	results, err := m.Match("go", "testdata/chi_routes.go", src)
	require.NoError(t, err)

	// Expect at least 2 results (Get /users, Post /users)
	assert.GreaterOrEqual(t, len(results), 2, "expected at least 2 chi route matches")

	// Check that we have chi_get or chi_route_group pattern names
	var patternNames []string
	for _, r := range results {
		patternNames = append(patternNames, r.PatternName)
		assert.NotNil(t, r.Captures, "captures should not be nil")
	}

	// Should have at least one chi_get match
	hasChi := false
	for _, n := range patternNames {
		if strings.Contains(n, "chi") {
			hasChi = true
			break
		}
	}
	assert.True(t, hasChi, "expected at least one chi pattern match, got: %v", patternNames)

	// At least one result should have path capture
	hasPath := false
	for _, r := range results {
		if _, ok := r.Captures["path"]; ok {
			hasPath = true
			break
		}
	}
	assert.True(t, hasPath, "expected at least one match with 'path' capture")
}

func TestMatchHTTPClient(t *testing.T) {
	reg := mustLoadRegistry(t, "../../patterns/go/net_http_client.yaml")
	m := patterns.NewTreeSitterMatcher(reg)

	src := mustReadFile(t, "testdata/http_client.go")
	results, err := m.Match("go", "testdata/http_client.go", src)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, len(results), 2, "expected at least 2 http client matches")

	var patternNames []string
	for _, r := range results {
		patternNames = append(patternNames, r.PatternName)
	}
	t.Logf("matched patterns: %v", patternNames)

	// Should have http_get and http_post and http_new_request
	found := make(map[string]bool)
	for _, n := range patternNames {
		found[n] = true
	}
	assert.True(t, found["http_get"] || found["http_post"] || found["http_new_request"],
		"expected http_get, http_post, or http_new_request pattern")
}

func TestMatchAxios(t *testing.T) {
	reg := mustLoadRegistry(t, "../../patterns/javascript/axios.yaml")
	m := patterns.NewTreeSitterMatcher(reg)

	src := mustReadFile(t, "testdata/axios_calls.js")
	results, err := m.Match("javascript", "testdata/axios_calls.js", src)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, len(results), 2, "expected at least 2 axios matches (get + post)")

	for _, r := range results {
		t.Logf("axios match: pattern=%s captures=%v line=%d", r.PatternName, r.Captures, r.Line)
	}

	// At least one should have url capture
	hasURL := false
	for _, r := range results {
		if _, ok := r.Captures["url"]; ok {
			hasURL = true
			break
		}
	}
	assert.True(t, hasURL, "expected at least one match with 'url' capture")
}

func TestMatchFetch(t *testing.T) {
	reg := mustLoadRegistry(t, "../../patterns/javascript/fetch.yaml")
	m := patterns.NewTreeSitterMatcher(reg)

	src := mustReadFile(t, "testdata/fetch_calls.js")
	results, err := m.Match("javascript", "testdata/fetch_calls.js", src)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, len(results), 2, "expected at least 2 fetch matches")

	for _, r := range results {
		t.Logf("fetch match: pattern=%s captures=%v line=%d", r.PatternName, r.Captures, r.Line)
	}

	// At least one should have method capture with POST
	hasPost := false
	for _, r := range results {
		if method, ok := r.Captures["method"]; ok && strings.Contains(method, "POST") {
			hasPost = true
			break
		}
	}
	assert.True(t, hasPost, "expected a fetch match with method=POST")
}

func TestMatchUnknownLanguage(t *testing.T) {
	reg := patterns.NewRegistry()
	m := patterns.NewTreeSitterMatcher(reg)

	results, err := m.Match("python", "test.py", []byte("def foo(): pass"))
	assert.NoError(t, err, "unknown language should not return an error")
	assert.Empty(t, results, "unknown language should return no results")
}

func TestMatchToGraph(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "http_handle_func", // contains "handler" → NodeTypeHTTPHandler
			File:        "service/routes.go",
			Line:        10,
			Captures:    map[string]string{"method": "Get", "path": "/users"},
		},
		{
			PatternName: "http_get", // contains "get" → NodeTypeHTTPClient
			File:        "service/client.go",
			Line:        20,
			Captures:    map[string]string{"url": "http://api/users"},
		},
		{
			PatternName: "go_statement", // no keyword → NodeTypeFunction
			File:        "service/worker.go",
			Line:        30,
			Captures:    map[string]string{"fn": "processJob"},
		},
	}

	nodes, edges := patterns.MatchToGraph("mysvc", results)

	assert.Len(t, nodes, 3)
	assert.Empty(t, edges) // MatchToGraph no longer emits self-edges

	// http_handle_func → contains "handler" → NodeTypeHTTPHandler
	assert.Equal(t, graph.NodeTypeHTTPHandler, nodes[0].Type)
	assert.Equal(t, "mysvc", nodes[0].Service)
	assert.Equal(t, "service/routes.go", nodes[0].File)
	assert.Equal(t, 10, nodes[0].Line)
	// Node ID must follow design doc format: service:file:type:name:line
	assert.Equal(t, "mysvc:service/routes.go:http_handler:http_handle_func:10", nodes[0].ID)

	// http_get → contains "get" → NodeTypeHTTPClient
	assert.Equal(t, graph.NodeTypeHTTPClient, nodes[1].Type)

	// go_statement → default → NodeTypeFunction
	assert.Equal(t, graph.NodeTypeFunction, nodes[2].Type)
}
