package linker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

func navLinkConfig() *workspace.WorkspaceConfig {
	return &workspace.WorkspaceConfig{
		Services: []workspace.Service{{Name: "app"}, {Name: "web"}},
	}
}

// Nav links produce navigates_to edges to the matching route handler —
// including same-service pairs (a server-rendered template linking to its
// own routes is the common case).
func TestLink_NavLinkSameService(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:page.templ:http_client:href:/reports:5", Type: graph.NodeTypeHTTPClient,
			Service: "app", File: "page.templ", Line: 5,
			Meta: map[string]string{"path": "/reports", "method": "GET", "nav_link": "true"},
		},
		{
			ID: "app:routes.go:http_handler:GET /reports:10", Type: graph.NodeTypeHTTPHandler,
			Service: "app", File: "routes.go", Line: 10,
			Meta: map[string]string{"path": "/reports", "method": "GET"},
		},
	}
	edges, err := New(navLinkConfig()).Link(nodes, nil)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, graph.EdgeTypeNavigatesTo, edges[0].Type)
	assert.Equal(t, nodes[0].ID, edges[0].From)
	assert.Equal(t, nodes[1].ID, edges[0].To)
	assert.Equal(t, "static", edges[0].Confidence)
	assert.Equal(t, "nav_link", edges[0].Meta["via"])
}

// Cross-service nav links (SPA linking to another service's page) work too.
func TestLink_NavLinkCrossService(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "web:Nav.tsx:http_client:nav_link_jsx:3", Type: graph.NodeTypeHTTPClient,
			Service: "web", File: "Nav.tsx", Line: 3,
			Meta: map[string]string{"path": "/reports", "method": "GET", "nav_link": "true"},
		},
		{
			ID: "app:routes.go:http_handler:GET /reports:10", Type: graph.NodeTypeHTTPHandler,
			Service: "app", File: "routes.go", Line: 10,
			Meta: map[string]string{"path": "/reports", "method": "GET"},
		},
	}
	edges, err := New(navLinkConfig()).Link(nodes, nil)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, graph.EdgeTypeNavigatesTo, edges[0].Type)
}

// Unmatched nav links are dropped silently — no unresolved-edge noise for
// page links that don't correspond to an indexed route.
func TestLink_NavLinkUnmatchedIsSilent(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "web:Nav.tsx:http_client:nav_link_jsx:3", Type: graph.NodeTypeHTTPClient,
			Service: "web", File: "Nav.tsx", Line: 3,
			Meta: map[string]string{"path": "/no/such/page", "method": "GET", "nav_link": "true"},
		},
	}
	edges, err := New(navLinkConfig()).Link(nodes, nil)
	require.NoError(t, err)
	assert.Empty(t, edges, "unmatched nav link must not emit an unresolved edge")
}
