package patterns_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// A call site after a nested helper's body must attribute to the outer
// function, not the helper (regression: section→edgeRow, checkbox→…).
func TestMatchToGraph_EnclosingFunctionContainment(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "arrow_func_var", // outer component: lines 10-100
			File:        "Filters.tsx",
			Line:        10,
			EndLine:     100,
			Captures:    map[string]string{"name": "Filters"},
		},
		{
			PatternName: "arrow_func_var", // nested helper: lines 12-17
			File:        "Filters.tsx",
			Line:        12,
			EndLine:     17,
			Captures:    map[string]string{"name": "checkbox"},
		},
		{
			PatternName: "fetch_call", // call site at line 68: inside Filters, outside checkbox
			File:        "Filters.tsx",
			Line:        68,
			Captures:    map[string]string{"url": `"/api/x"`},
		},
		{
			PatternName: "fetch_call", // call site at line 14: inside checkbox
			File:        "Filters.tsx",
			Line:        14,
			Captures:    map[string]string{"url": `"/api/y"`},
		},
	}
	nodes, edges := patterns.MatchToGraph("web", results)

	byID := map[string]graph.Node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	fromLabelByToLine := map[int]string{}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls {
			fromLabelByToLine[byID[e.To].Line] = byID[e.From].Label
		}
	}
	assert.Equal(t, "Filters", fromLabelByToLine[68],
		"call after the nested helper's end must attribute to the outer function")
	assert.Equal(t, "checkbox", fromLabelByToLine[14],
		"call inside the nested helper must attribute to the helper")
}

// Call-reference results (component_fn_call) use the same containment rule.
func TestMatchToGraph_CallRefContainment(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "arrow_func_var",
			File:        "Detail.tsx",
			Line:        90,
			EndLine:     500,
			Captures:    map[string]string{"name": "Detail"},
		},
		{
			PatternName: "arrow_func_var",
			File:        "Detail.tsx",
			Line:        389,
			EndLine:     414,
			Captures:    map[string]string{"name": "section"},
		},
		{
			PatternName: "arrow_func_var",
			File:        "Detail.tsx",
			Line:        171,
			EndLine:     190,
			Captures:    map[string]string{"name": "edgeRow"},
		},
		{
			PatternName: "component_fn_call", // edgeRow(...) at line 435: inside Detail only
			File:        "Detail.tsx",
			Line:        435,
			Captures:    map[string]string{"callee": "edgeRow"},
		},
	}
	nodes, edges := patterns.MatchToGraph("web", results)

	labelByID := map[string]string{}
	for _, n := range nodes {
		labelByID[n.ID] = n.Label
	}
	require.Len(t, edges, 1)
	assert.Equal(t, "Detail", labelByID[edges[0].From])
	assert.Equal(t, "edgeRow", labelByID[edges[0].To])
}

// Module-level JSX usage (render(<App/>) in index.tsx) gets a synthetic
// per-file module node as its caller instead of being dropped.
func TestMatchToGraph_ModuleLevelRender(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "jsx_component_self_closing",
			File:        "src/index.tsx",
			Line:        7,
			Captures:    map[string]string{"name": "App"},
		},
	}
	nodes, edges := patterns.MatchToGraph("web", results)

	var moduleNode *graph.Node
	for i := range nodes {
		if nodes[i].Label == "(module)" {
			moduleNode = &nodes[i]
		}
	}
	require.NotNil(t, moduleNode, "expected a synthetic module node")
	assert.Equal(t, "src/index.tsx", moduleNode.File)

	require.Len(t, edges, 1)
	assert.Equal(t, graph.EdgeTypeRenders, edges[0].Type)
	assert.Equal(t, moduleNode.ID, edges[0].From)
}

// Non-JS files must not grow module nodes: Go has no top-level statements.
func TestMatchToGraph_NoModuleNodeForGo(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "http_get",
			File:        "client.go",
			Line:        20,
			Captures:    map[string]string{"url": `"/x"`},
		},
	}
	nodes, edges := patterns.MatchToGraph("svc", results)
	for _, n := range nodes {
		assert.NotEqual(t, "(module)", n.Label)
	}
	assert.Empty(t, edges)
}

// Anonymous goroutines produce a worker node spawned by the enclosing function.
func TestMatchToGraph_AnonGoroutineSpawns(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "func_decl",
			File:        "main.go",
			Line:        340,
			EndLine:     380,
			Captures:    map[string]string{"name": "watchDB"},
		},
		{
			PatternName: "goroutine_anon",
			File:        "main.go",
			Line:        351,
			Captures:    map[string]string{},
		},
	}
	nodes, edges := patterns.MatchToGraph("svc", results)

	var worker *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeWorker {
			worker = &nodes[i]
		}
	}
	require.NotNil(t, worker)
	assert.Equal(t, "go func()", worker.Label)

	require.Len(t, edges, 1)
	assert.Equal(t, graph.EdgeTypeSpawns, edges[0].Type)
	assert.Equal(t, worker.ID, edges[0].To)
}

// URL-builder helpers: fetch(mermaidURL(...)) resolves through the helper's
// returned template-literal prefix.
func TestMatchToGraph_URLBuilderFunctionResolution(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "fn_return_template_prefix",
			File:        "export.ts",
			Line:        14,
			Captures:    map[string]string{"name": "mermaidURL", "value": "`/api/export/mermaid?${sp.toString()}`"},
		},
		{
			PatternName: "func_decl",
			File:        "export.ts",
			Line:        30,
			EndLine:     34,
			Captures:    map[string]string{"name": "fetchMermaid"},
		},
		{
			PatternName: "fetch_call",
			File:        "export.ts",
			Line:        31,
			Captures:    map[string]string{"url": "mermaidURL(level, scope)"},
		},
	}
	nodes, _ := patterns.MatchToGraph("web", results)

	var client *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeHTTPClient {
			client = &nodes[i]
		}
	}
	require.NotNil(t, client)
	assert.Equal(t, "/api/export/mermaid?${sp.toString()}", client.Meta["url"])
	assert.Equal(t, "inferred", client.Meta["url_confidence"])
}

// Event-handler assignment nodes get a readable label, not the pattern name.
func TestMatchToGraph_HandlerAssignLabel(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "ws_onmessage_assign",
			File:        "Notification.tsx",
			Line:        24,
			Captures:    map[string]string{"prop": "onmessage", "handler": "(evt) => {}"},
		},
	}
	nodes, _ := patterns.MatchToGraph("web", results)

	var sub *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeSubscriber {
			sub = &nodes[i]
		}
	}
	require.NotNil(t, sub)
	assert.Equal(t, "onmessage handler", sub.Label)
}
