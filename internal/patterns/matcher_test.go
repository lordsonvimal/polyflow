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
	assert.Len(t, edges, 3)

	// http_handle_func → contains "handler" → NodeTypeHTTPHandler
	assert.Equal(t, graph.NodeTypeHTTPHandler, nodes[0].Type)
	assert.Equal(t, "mysvc", nodes[0].Service)
	assert.Equal(t, "service/routes.go", nodes[0].File)
	assert.Equal(t, 10, nodes[0].Line)
	// Node ID must follow design doc format: service:file:type:name:line
	assert.Equal(t, "mysvc:service/routes.go:http_handler:http_handle_func:10", nodes[0].ID)
	assert.Equal(t, nodes[0].ID+":edge", edges[0].ID)

	// http_get → contains "get" → NodeTypeHTTPClient
	assert.Equal(t, graph.NodeTypeHTTPClient, nodes[1].Type)

	// go_statement → default → NodeTypeFunction
	assert.Equal(t, graph.NodeTypeFunction, nodes[2].Type)

	// Check edge types
	assert.Equal(t, graph.EdgeTypeHTTPCall, edges[0].Type)
	assert.Equal(t, graph.EdgeTypeHTTPCall, edges[1].Type)
	assert.Equal(t, graph.EdgeTypeCalls, edges[2].Type)
}
