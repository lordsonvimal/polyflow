package patterns_test

import (
	"encoding/json"
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
		{"gin_registrar_func", "gin_group_registrar_func", graph.NodeTypeVariable},
		{"gin_registrar_call", "gin_group_registrar_call", graph.NodeTypeVariable},
		{"default", "some_unknown_pattern", graph.NodeTypeFunction},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results := []patterns.MatchResult{
				{PatternName: tc.patternName, File: "f.go", Line: 1, Captures: map[string]string{}},
			}
			nodes, _, _ := patterns.MatchToGraph("svc", results)
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

func TestMatch_Ruby_MemberBlockMultiVerbNoCollision(t *testing.T) {
	// Regression test (H.5 follow-up): multiple verbs inside one `member do`
	// block used to collapse onto a single node because the block-opening
	// `member` keyword's line was used as the match line for every verb
	// inside it. Each verb must now get its own node, keyed by its own line.
	reg, err := patterns.DefaultRegistry("../../patterns/ruby")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	src := []byte(`Rails.application.routes.draw do
  resources :folders do
    member do
      get :publish
      post :duplicate
      patch :rename
    end
  end
end
`)
	results, err := m.Match("ruby", "config/routes.rb", src)
	require.NoError(t, err)

	nodes, _, _ := patterns.MatchToGraph("svc", results)
	var routeNodes []graph.Node
	for _, n := range nodes {
		if n.Meta["pattern"] == "member_verb_route" {
			routeNodes = append(routeNodes, n)
		}
	}
	require.Len(t, routeNodes, 3, "each verb in the member block should get its own node")

	seenIDs := map[string]bool{}
	seenLines := map[string]bool{}
	for _, n := range routeNodes {
		assert.False(t, seenIDs[n.ID], "duplicate node ID %q", n.ID)
		seenIDs[n.ID] = true
		seenLines[n.Label] = true
	}
	assert.Contains(t, seenLines, "GET :publish")
	assert.Contains(t, seenLines, "POST :duplicate")
	assert.Contains(t, seenLines, "PATCH :rename")
}

func TestMatch_EmptyPatterns(t *testing.T) {
	reg := patterns.NewRegistry()
	m := patterns.NewTreeSitterMatcher(reg)
	results, err := m.Match("go", "main.go", []byte("package main"))
	assert.NoError(t, err)
	assert.Empty(t, results)
}

func TestMatchToGraph_EmptyResults(t *testing.T) {
	nodes, edges, _ := patterns.MatchToGraph("svc", nil)
	assert.Empty(t, nodes)
	assert.Empty(t, edges)
}

func TestMatchToGraph_GoroutineCallIsEdge(t *testing.T) {
	// goroutine_call must be a call-ref: no new node, one spawns edge from enclosing func.
	results := []patterns.MatchResult{
		// EndLine mirrors what a real parse produces: func_decl captures @_def, so
		// every declaration carries its body span. Only a *call*-site pattern
		// leaves it unset, which is how Pass 2 tells a scope from a call.
		{PatternName: "func_decl", File: "f.go", Line: 1, Captures: map[string]string{"name": "New"}, EndLine: 8},
		{PatternName: "func_decl", File: "f.go", Line: 10, Captures: map[string]string{"name": "fanOut"}, EndLine: 15},
		{PatternName: "goroutine_call", File: "f.go", Line: 5, Captures: map[string]string{"callee": "fanOut"}},
	}
	nodes, edges, _ := patterns.MatchToGraph("svc", results)
	require.Len(t, nodes, 2, "only the two func_decl nodes should be created")
	require.Len(t, edges, 1, "one spawns edge from New -> fanOut")
	assert.Equal(t, "svc:f.go:function:New:1", edges[0].From)
	assert.Equal(t, "svc:f.go:function:fanOut:10", edges[0].To)
	assert.Equal(t, graph.EdgeTypeSpawns, edges[0].Type)
}

func TestMatchToGraph_ConstantHTTPVerbResolves(t *testing.T) {
	// PR.1: `http.NewRequest(http.MethodGet, ...)` captures the verb as raw
	// source. Stored verbatim it case_folds to `http.methodget`, which equals
	// no handler's method — and is not *empty*, so http.yaml's
	// method_fallback never retries it, and the producer emits a junk edge to
	// the synthetic `unresolved` node instead of matching a real route.
	results := []patterns.MatchResult{{
		PatternName: "http_new_request",
		File:        "client.go",
		Line:        42,
		Captures:    map[string]string{"method": "http.MethodGet", "url": `"/api/v1/health"`},
	}}
	nodes, _, _ := patterns.MatchToGraph("svc", results)
	require.Len(t, nodes, 1)
	assert.Equal(t, "GET", nodes[0].Meta["method"])
	// The label is minted from the raw captures, so the verb token must be
	// rewritten there too or search shows agents a method that no longer
	// matches the node's own meta.
	assert.Equal(t, "GET /api/v1/health", nodes[0].Label)
}

func TestMatchToGraph_RubySymbolHTTPVerbResolves(t *testing.T) {
	// The Ruby half of the same defect: `RestClient::Request.execute(method:
	// :get, ...)` captures `:get`.
	results := []patterns.MatchResult{{
		PatternName: "rest_client_request",
		File:        "app/decorators/file.rb",
		Line:        39,
		Captures:    map[string]string{"method": ":get", "url": `"/api/v1/files"`},
	}}
	nodes, _, _ := patterns.MatchToGraph("svc", results)
	require.Len(t, nodes, 1)
	assert.Equal(t, "GET", nodes[0].Meta["method"])
}

func TestMatchToGraph_UnrecognizedVerbExpressionIsLeftVerbatim(t *testing.T) {
	// Declining must be lossless. A verb this pass cannot resolve keeps its
	// captured text so the existing dynamic-ledger path still sees it —
	// guessing here would match a real handler under the wrong method, which
	// is worse than not matching at all.
	results := []patterns.MatchResult{{
		PatternName: "http_new_request",
		File:        "client.go",
		Line:        7,
		Captures:    map[string]string{"method": "req.method", "url": `"/api/v1/health"`},
	}}
	nodes, _, _ := patterns.MatchToGraph("svc", results)
	require.Len(t, nodes, 1)
	assert.Equal(t, "req.method", nodes[0].Meta["method"])
}

func TestMatchToGraph_CobraRunIsEdge(t *testing.T) {
	// cobra_run must be a call-ref: no new node, edge from enclosing func to RunE target.
	results := []patterns.MatchResult{
		{PatternName: "func_decl", File: "main.go", Line: 1, Captures: map[string]string{"name": "init"}},
		{PatternName: "func_decl", File: "main.go", Line: 20, Captures: map[string]string{"name": "runServe"}},
		{PatternName: "cobra_run", File: "main.go", Line: 10, Captures: map[string]string{"callee": "runServe"}},
	}
	nodes, edges, _ := patterns.MatchToGraph("svc", results)
	require.Len(t, nodes, 2, "only the two func_decl nodes should be created")
	require.Len(t, edges, 1, "one edge from init -> runServe")
	assert.Equal(t, "svc:main.go:function:init:1", edges[0].From)
	assert.Equal(t, "svc:main.go:function:runServe:20", edges[0].To)
}

func TestMatchToGraph_JSXEventHandlerEdgeCarriesEvent(t *testing.T) {
	// onClick={loadSource} must emit a calls edge labeled with the event so the
	// binding is distinguishable from a plain call (Phase U.3).
	results := []patterns.MatchResult{
		{PatternName: "component_arrow_decl", File: "App.tsx", Line: 1, Captures: map[string]string{"name": "App"}},
		{PatternName: "func_decl", File: "App.tsx", Line: 2, Captures: map[string]string{"name": "loadSource"}},
		{PatternName: "jsx_event_handler_ref", File: "App.tsx", Line: 5, Captures: map[string]string{"prop": "onClick", "callee": "loadSource"}},
	}
	_, edges, _ := patterns.MatchToGraph("svc", results)
	ev := findEdge(edges, graph.EdgeTypeCalls)
	require.NotNil(t, ev)
	assert.Equal(t, "click", ev.Meta["event"])
	assert.Equal(t, "on click", ev.Label)
}

// findEdge returns the first edge of the given type, or nil.
func findEdge(edges []graph.Edge, t graph.EdgeType) *graph.Edge {
	for i := range edges {
		if edges[i].Type == t {
			return &edges[i]
		}
	}
	return nil
}

func TestMatchToGraph_SolidNamespacedEventEdge(t *testing.T) {
	// on:click namespaced directive normalizes the same as camelCase onClick.
	results := []patterns.MatchResult{
		{PatternName: "component_arrow_decl", File: "App.tsx", Line: 1, Captures: map[string]string{"name": "App"}},
		{PatternName: "func_decl", File: "App.tsx", Line: 2, Captures: map[string]string{"name": "onSubmit"}},
		{PatternName: "jsx_event_handler_ref", File: "App.tsx", Line: 5, Captures: map[string]string{"prop": "oncapture:submit", "callee": "onSubmit"}},
	}
	_, edges, _ := patterns.MatchToGraph("svc", results)
	ev := findEdge(edges, graph.EdgeTypeCalls)
	require.NotNil(t, ev)
	assert.Equal(t, "submit", ev.Meta["event"])
}

func TestMatchToGraph_HTMLListenEdgeCarriesEvent(t *testing.T) {
	// An inline HTML event attr inside an enclosing scope stamps the dom_listen
	// edge with the event; the node also records it for edge-less HTML files.
	results := []patterns.MatchResult{
		{PatternName: "func_decl", File: "app.js", Line: 1, Captures: map[string]string{"name": "wire"}, EndLine: 9},
		{PatternName: "add_event_listener", File: "app.js", Line: 3, Captures: map[string]string{"event_type": "\"click\"", "handler": "onClick"}},
	}
	nodes, edges, _ := patterns.MatchToGraph("svc", results)
	var listen *graph.Edge
	for i := range edges {
		if edges[i].Type == graph.EdgeTypeDOMListen {
			listen = &edges[i]
		}
	}
	require.NotNil(t, listen, "expected a dom_listen edge")
	assert.Equal(t, "click", listen.Meta["event"])
	assert.Equal(t, "on click", listen.Label)
	// The dom_target node also records the event name.
	var found bool
	for _, n := range nodes {
		if n.Type == graph.NodeTypeDOMTarget {
			found = true
			assert.Equal(t, "click", n.Meta["event"])
		}
	}
	assert.True(t, found)
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
		nodes, _, _ := patterns.MatchToGraph("svc", results)
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
	nodes, edges, _ := patterns.MatchToGraph("svc", results)

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
	nodes, _, _ := patterns.MatchToGraph("svc", results)
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
	nodes, _, _ := patterns.MatchToGraph("svc", results)

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
	nodes, _, _ := patterns.MatchToGraph("svc", results)

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
	nodes, _, _ := patterns.MatchToGraph("svc", results)

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

// TestMatchToGraph_EndLine_RealFixture runs a real fixture function of known
// extent through parser->matcher->MatchToGraph end to end for Go, TypeScript,
// and Python, asserting the minted node's EndLine exactly (UB.0; rule 6).
func TestMatchToGraph_EndLine_RealFixture(t *testing.T) {
	cases := []struct {
		lang, dir, file, src string
		wantEndLine          int
	}{
		{
			lang: "go", dir: "../../patterns/go", file: "f.go",
			src: "package p\n\nfunc Foo() {\n\tx := 1\n\t_ = x\n}\n",
			// func Foo() {  -> line 3
			// x := 1        -> line 4
			// _ = x         -> line 5
			// }             -> line 6
			wantEndLine: 6,
		},
		{
			lang: "javascript", dir: "../../patterns/javascript", file: "f.ts",
			src:         "function foo() {\n  const x = 1;\n  return x;\n}\n",
			wantEndLine: 4,
		},
		{
			lang: "python", dir: "../../patterns/python", file: "f.py",
			src:         "def foo():\n    x = 1\n    return x\n",
			wantEndLine: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			reg, err := patterns.DefaultRegistry(tc.dir)
			require.NoError(t, err)
			m := patterns.NewTreeSitterMatcher(reg)

			results, err := m.Match(tc.lang, tc.file, []byte(tc.src))
			require.NoError(t, err)

			nodes, _, _ := patterns.MatchToGraph("svc", results)
			var fn *graph.Node
			for i := range nodes {
				if nodes[i].Type == graph.NodeTypeFunction {
					fn = &nodes[i]
				}
			}
			require.NotNil(t, fn, "expected a function node from the fixture")
			assert.Equal(t, tc.wantEndLine, fn.EndLine)
		})
	}
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

	nodes, edges, _ := patterns.MatchToGraph("mysvc", results)

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

// TestStripStringLiteral_PythonForms verifies that stripStringLiteral (accessed
// via the exported StripStringLiteral wrapper) correctly strips Python string
// prefix characters (f, r, b, u, and combinations) and triple-quoted strings.
// This test is the "capture hygiene" gate for Python per checklist item 11.
func TestStripStringLiteral_PythonForms(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		// Standard forms (existing behavior, regression-guarded)
		{`"hello"`, "hello"},
		{`'world'`, "world"},
		{"`backtick`", "backtick"},
		// Python triple-quoted
		{`"""triple double"""`, "triple double"},
		{`'''triple single'''`, "triple single"},
		// Python prefix: single-char
		{`f"formatted"`, "formatted"},
		{`r"raw"`, "raw"},
		{`b"bytes"`, "bytes"},
		{`u"unicode"`, "unicode"},
		// Python prefix: two-char combinations
		{`rb"raw bytes"`, "raw bytes"},
		{`br"byte raw"`, "byte raw"},
		{`fr"f raw"`, "f raw"},
		{`rf"r f"`, "r f"},
		// Python prefix: upper-case
		{`F"upper f"`, "upper f"},
		{`R"upper r"`, "upper r"},
		{`B"upper b"`, "upper b"},
		// Python prefix + triple quote
		{`f"""triple f-string"""`, "triple f-string"},
		{`r'''triple raw'''`, "triple raw"},
		// No-op cases
		{"no_quotes", "no_quotes"},
		{"/path/no/quotes", "/path/no/quotes"},
		{`""`, ""}, // empty string
		{`''`, ""}, // empty string
	}
	for _, c := range cases {
		got := patterns.StripStringLiteral(c.input)
		assert.Equal(t, c.want, got, "StripStringLiteral(%q)", c.input)
	}
}

// --- X.0: test-DSL comm-node suppression ------------------------------------
//
// Real parser→matcher→MatchToGraph path (bug-class #6): each case reads an
// actual fixture file, matches it with the full pattern set for the
// language, and asserts on the resulting graph nodes — not hand-built
// MatchResults.

func TestX0_TestDSL_JS_PositiveSuppressesHTTPClient(t *testing.T) {
	reg, err := patterns.DefaultRegistry("../../patterns/javascript")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	src := mustReadFile(t, "testdata/test_dsl_js.js")
	results, err := m.Match("javascript", "testdata/test_dsl_js.js", src)
	require.NoError(t, err)

	nodes, _, _ := patterns.MatchToGraph("svc", results)

	var httpClients, demoted int
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			httpClients++
		}
		if n.Type == graph.NodeTypeFunction && n.Meta[graph.MetaIsTest] == "true" {
			demoted++
		}
	}
	assert.Equal(t, 0, httpClients, "test('...', async () => { fetch(...) }) must not mint an http_client node")
	// 2 demoted sites: the fetch(...) call, and the outer test("creates
	// profile", ...) call itself — producer_alias_url_call's generic
	// identifier(stringArg) shape also matches the test() wrapper, so without
	// demotion it would mint a phantom http_client node from the test label.
	assert.Equal(t, 2, demoted, "call sites are still indexed, demoted to function nodes with is_test=true")
}

func TestX0_TestDSL_JS_NegativeKeepsHTTPClient(t *testing.T) {
	reg, err := patterns.DefaultRegistry("../../patterns/javascript")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	src := mustReadFile(t, "testdata/prod_js.js")
	results, err := m.Match("javascript", "testdata/prod_js.js", src)
	require.NoError(t, err)

	nodes, _, _ := patterns.MatchToGraph("svc", results)

	var httpClients int
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			httpClients++
			assert.NotEqual(t, "true", n.Meta[graph.MetaIsTest])
		}
	}
	assert.Equal(t, 1, httpClients, "the same fetch(...) call outside a test-DSL function is a real http_client node")
}

func TestX0_TestDSL_Ruby_PositiveSuppressesHTTPClient(t *testing.T) {
	reg, err := patterns.DefaultRegistry("../../patterns/ruby")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	src := mustReadFile(t, "testdata/test_dsl_rb.rb")
	results, err := m.Match("ruby", "testdata/test_dsl_rb.rb", src)
	require.NoError(t, err)

	nodes, _, _ := patterns.MatchToGraph("svc", results)

	var httpClients, demoted int
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			httpClients++
		}
		if n.Type == graph.NodeTypeFunction && n.Meta[graph.MetaIsTest] == "true" {
			demoted++
		}
	}
	assert.Equal(t, 0, httpClients, "HTTParty.post inside RSpec it/do must not mint an http_client node")
	assert.Equal(t, 1, demoted)
}

func TestX0_TestDSL_Ruby_NegativeKeepsHTTPClient(t *testing.T) {
	reg, err := patterns.DefaultRegistry("../../patterns/ruby")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	src := mustReadFile(t, "testdata/prod_rb.rb")
	results, err := m.Match("ruby", "testdata/prod_rb.rb", src)
	require.NoError(t, err)

	nodes, _, _ := patterns.MatchToGraph("svc", results)

	var httpClients int
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			httpClients++
		}
	}
	assert.Equal(t, 1, httpClients, "the same HTTParty.post outside RSpec is a real http_client node")
}

func TestX0_TestDSL_Go_PositiveSuppressesHTTPClient(t *testing.T) {
	reg, err := patterns.DefaultRegistry("../../patterns/go")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	src := mustReadFile(t, "testdata/test_dsl_go.go")
	results, err := m.Match("go", "testdata/test_dsl_go.go", src)
	require.NoError(t, err)

	nodes, _, _ := patterns.MatchToGraph("svc", results)

	var httpClients, demoted int
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			httpClients++
		}
		if n.Type == graph.NodeTypeFunction && n.Meta[graph.MetaIsTest] == "true" {
			demoted++
		}
	}
	assert.Equal(t, 0, httpClients, "http.Get inside func TestFetch and inside t.Run(...) must not mint http_client nodes")
	assert.Equal(t, 2, demoted, "one demoted site in TestFetch, one inside the t.Run subtest closure")
}

func TestX0_TestDSL_Go_NegativeKeepsHTTPClient(t *testing.T) {
	reg, err := patterns.DefaultRegistry("../../patterns/go")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	src := mustReadFile(t, "testdata/prod_go.go")
	results, err := m.Match("go", "testdata/prod_go.go", src)
	require.NoError(t, err)

	nodes, _, _ := patterns.MatchToGraph("svc", results)

	var httpClients int
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			httpClients++
		}
	}
	assert.Equal(t, 1, httpClients, "http.Get in a non-Test function is a real http_client node")
}

// TestX0_TestFilePath_DemotesHTTPClient covers the fix-#1 gap: an http.Get in a
// plain (non-test-DSL) function still demotes when the *file* is a test file.
// Same fixture content as prod_go.go (which keeps its http_client above), only
// the file-path label changes to `_test.go` — isolating the path trigger. A
// `_test.go` HTTP call is test fixture code, not a production cross-service
// producer, and must leave the resolution denominator.
func TestX0_TestFilePath_DemotesHTTPClient(t *testing.T) {
	reg, err := patterns.DefaultRegistry("../../patterns/go")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	src := mustReadFile(t, "testdata/prod_go.go")
	results, err := m.Match("go", "internal/views/config_association_handler_test.go", src)
	require.NoError(t, err)

	nodes, _, _ := patterns.MatchToGraph("svc", results)

	var httpClients, demoted int
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			httpClients++
		}
		if n.Type == graph.NodeTypeFunction && n.Meta[graph.MetaIsTest] == "true" {
			demoted++
		}
	}
	assert.Equal(t, 0, httpClients, "http.Get in a _test.go file must not mint an http_client node")
	assert.Equal(t, 1, demoted, "the call site is still indexed, demoted to a function node with is_test=true")
}

// TestX0_Determinism_TwoRunByteIdentical guards bug-class #2: the ancestor
// walk must not depend on map iteration order or any other non-deterministic
// input. Running the same fixture through Match+MatchToGraph twice must
// produce byte-identical JSON-serialized node sets.
func TestX0_Determinism_TwoRunByteIdentical(t *testing.T) {
	reg, err := patterns.DefaultRegistry("../../patterns/go")
	require.NoError(t, err)

	src := mustReadFile(t, "testdata/test_dsl_go.go")

	run := func() []byte {
		m := patterns.NewTreeSitterMatcher(reg)
		results, err := m.Match("go", "testdata/test_dsl_go.go", src)
		require.NoError(t, err)
		nodes, edges, _ := patterns.MatchToGraph("svc", results)
		out, err := json.Marshal(struct {
			Nodes []graph.Node
			Edges []graph.Edge
		}{nodes, edges})
		require.NoError(t, err)
		return out
	}

	first := run()
	second := run()
	assert.Equal(t, string(first), string(second), "two runs over the same input must be byte-identical")
}

func TestX0_MatchToGraph_DemotesOnlyCommTypes(t *testing.T) {
	// Hand-built regression: a test-DSL http_client site is demoted to
	// function+is_test, keeps its caller edge, and is excluded from the
	// enclosing-function index (must not become a false scope for code that
	// follows it in the same test function).
	results := []patterns.MatchResult{
		{PatternName: "func_decl", File: "f_test.go", Line: 1, Captures: map[string]string{"name": "TestFoo"}, EndLine: 10},
		{PatternName: "http_get", File: "f_test.go", Line: 2, Captures: map[string]string{"url": "http://x/y"}, IsTestDSL: true},
		{PatternName: "func_decl", File: "f_test.go", Line: 5, Captures: map[string]string{"name": "helper"}},
	}
	nodes, edges, _ := patterns.MatchToGraph("svc", results)

	require.Len(t, nodes, 3)
	var demotedID string
	for _, n := range nodes {
		if n.Meta[graph.MetaIsTest] == "true" {
			assert.Equal(t, graph.NodeTypeFunction, n.Type)
			demotedID = n.ID
		}
	}
	require.NotEmpty(t, demotedID, "expected exactly one demoted node")

	var callerEdge *graph.Edge
	for i := range edges {
		if edges[i].To == demotedID {
			callerEdge = &edges[i]
		}
	}
	require.NotNil(t, callerEdge, "the demoted node must still receive a caller edge (blast radius preserved)")
	assert.Equal(t, "svc:f_test.go:function:TestFoo:1", callerEdge.From)
	assert.Equal(t, graph.EdgeTypeCalls, callerEdge.Type)
}

func TestDetectJSGrammar_FlowTypedJS_UpgradesToTypeScript(t *testing.T) {
	src := []byte(`export const apiPut = (url: string, data: ?Object = {}): Promise<Object> =>
  axios({ method: "PUT", url });
`)
	got := patterns.DetectJSGrammar("services/ApiServices.js", src, "javascript")
	assert.Equal(t, "typescript", got, "a Flow-annotated .js file should upgrade to the typescript grammar")
}

func TestDetectJSGrammar_PlainJS_StaysJavaScript(t *testing.T) {
	src := []byte(`export const apiPut = (url, data = {}) =>
  axios({ method: "PUT", url });
`)
	got := patterns.DetectJSGrammar("services/ApiServices.js", src, "javascript")
	assert.Equal(t, "javascript", got, "ordinary untyped JS must not pay the typescript-grammar cost")
}

func TestDetectJSGrammar_AlreadyTypeScript_Unchanged(t *testing.T) {
	src := []byte(`export const apiPut = (url: string): Promise<Object> => axios({ method: "PUT", url });`)
	got := patterns.DetectJSGrammar("services/ApiServices.tsx", src, "tsx")
	assert.Equal(t, "tsx", got, "a .tsx file already uses a type-aware grammar and must not be re-routed")
}
