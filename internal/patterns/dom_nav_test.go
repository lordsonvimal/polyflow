package patterns_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// DOM nodes get their classified edge kind (dom_listen, dom_read, …) from
// the enclosing function instead of a generic calls edge.
func TestMatchToGraph_DOMEdgeTypes(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "func_decl",
			File:        "app.js",
			Line:        1,
			EndLine:     20,
			Captures:    map[string]string{"name": "setup"},
		},
		{
			PatternName: "add_event_listener",
			File:        "app.js",
			Line:        3,
			Captures:    map[string]string{"event_type": `"click"`, "handler": "handleClick"},
		},
		{
			PatternName: "query_selector",
			File:        "app.js",
			Line:        2,
			Captures:    map[string]string{"fn": "querySelector", "selector": `"#x"`},
		},
	}
	_, edges := patterns.MatchToGraph("web", results)

	types := map[graph.EdgeType]int{}
	for _, e := range edges {
		types[e.Type]++
	}
	assert.Equal(t, 1, types[graph.EdgeTypeDOMListen], "addEventListener → dom_listen edge")
	assert.Equal(t, 1, types[graph.EdgeTypeDOMRead], "querySelector → dom_read edge")
	assert.Zero(t, types[graph.EdgeTypeCalls], "no generic calls edges for DOM nodes")
}

// Listener nodes whose handler capture is a plain identifier get a calls
// edge to the handler function declared in the same file.
func TestMatchToGraph_ListenerHandlerEdge(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "func_decl",
			File:        "app.js",
			Line:        1,
			EndLine:     3,
			Captures:    map[string]string{"name": "handleClick"},
		},
		{
			PatternName: "func_decl",
			File:        "app.js",
			Line:        5,
			EndLine:     9,
			Captures:    map[string]string{"name": "setup"},
		},
		{
			PatternName: "dom_event_prop_assign",
			File:        "app.js",
			Line:        6,
			Captures:    map[string]string{"prop": "onclick", "handler": "handleClick"},
		},
		{
			PatternName: "add_event_listener", // inline arrow: no handler edge
			File:        "app.js",
			Line:        7,
			Captures:    map[string]string{"event_type": `"input"`, "handler": "(e) => validate(e)"},
		},
	}
	nodes, edges := patterns.MatchToGraph("web", results)

	labelByID := map[string]string{}
	var domNode *graph.Node
	for i := range nodes {
		labelByID[nodes[i].ID] = nodes[i].Label
		if nodes[i].Meta["pattern"] == "dom_event_prop_assign" {
			domNode = &nodes[i]
		}
	}
	require.NotNil(t, domNode)
	assert.Equal(t, "onclick handler", domNode.Label)

	var handlerEdges []graph.Edge
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && labelByID[e.To] == "handleClick" {
			handlerEdges = append(handlerEdges, e)
		}
	}
	require.Len(t, handlerEdges, 1, "exactly one listener→handler edge (arrow handler excluded)")
	assert.Equal(t, domNode.ID, handlerEdges[0].From)
}

// Nav-link patterns produce http_client nodes stamped nav_link=true with a
// GET default, so the linker can route them to navigates_to edges.
func TestMatchToGraph_NavLinkMeta(t *testing.T) {
	results := []patterns.MatchResult{
		{
			PatternName: "nav_link_jsx",
			File:        "Nav.tsx",
			Line:        3,
			Captures:    map[string]string{"prop": "href", "path": "/reports"},
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
	assert.Equal(t, "true", client.Meta["nav_link"])
	assert.Equal(t, "GET", client.Meta["method"])
	assert.Equal(t, "/reports", client.Meta["path"])
}
