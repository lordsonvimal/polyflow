package linker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

func monolithConfig() *workspace.WorkspaceConfig {
	return &workspace.WorkspaceConfig{
		Services: []workspace.Service{{Name: "app"}},
	}
}

// A datastar action (data-on:click={ @post('/…') }) and its gin handler live in
// the same service and are NOT joined by a "calls" edge, so the linker must emit
// the edge that closes the route→handler→component→action→handler loop.
func TestLink_DatastarSameService(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:play.templ:http_client:POST:/play/*/draw:12", Type: graph.NodeTypeHTTPClient,
			Service: "app", File: "play.templ", Line: 12, Language: "templ",
			Meta: map[string]string{"path": "/play/*/draw", "method": "POST", "datastar": "true", "confidence": "partial"},
		},
		{
			ID: "app:handlers/play.go:http_handler:POST /play/:id/draw:40", Type: graph.NodeTypeHTTPHandler,
			Service: "app", File: "handlers/play.go", Line: 40,
			Meta: map[string]string{"path": "/play/:id/draw", "method": "POST"},
		},
	}
	edges, err := New(monolithConfig()).Link(nodes, nil)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, graph.EdgeTypeHTTPCall, edges[0].Type)
	assert.Equal(t, nodes[0].ID, edges[0].From)
	assert.Equal(t, nodes[1].ID, edges[0].To)
	assert.Equal(t, "datastar_action", edges[0].Meta["via"])
}

// A literal-path datastar action matches its handler exactly (static confidence)
// same-service, just like the wildcard case.
func TestLink_DatastarSameServiceStatic(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:rows.templ:http_client:GET:/rows:8", Type: graph.NodeTypeHTTPClient,
			Service: "app", File: "rows.templ", Line: 8, Language: "templ",
			Meta: map[string]string{"path": "/rows", "method": "GET", "datastar": "true", "confidence": "static"},
		},
		{
			ID: "app:handlers/rows.go:http_handler:GET /rows:20", Type: graph.NodeTypeHTTPHandler,
			Service: "app", File: "handlers/rows.go", Line: 20,
			Meta: map[string]string{"path": "/rows", "method": "GET"},
		},
	}
	edges, err := New(monolithConfig()).Link(nodes, nil)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, graph.EdgeTypeHTTPCall, edges[0].Type)
	assert.Equal(t, "datastar_action", edges[0].Meta["via"])
	assert.Equal(t, "static", edges[0].Confidence)
}

// T.2 partial paths put the wildcard on the CLIENT side (interpolated Go
// expression for the game id) while the route pattern puts it on the handler
// side (:gameID). The symmetric wildcard match reconciles both and, with the
// query string stripped, links the action to its handler.
func TestLink_DatastarClientSideWildcardAndQuery(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:board.templ:http_client:POST:navigate:30", Type: graph.NodeTypeHTTPClient,
			Service: "app", File: "board.templ", Line: 30, Language: "templ",
			Meta: map[string]string{"path": "*/*/history/navigate?direction=1", "method": "POST", "datastar": "true", "confidence": "partial"},
		},
		{
			ID: "app:server/routes.go:http_handler:POST /play/:gameID/history/navigate:64", Type: graph.NodeTypeHTTPHandler,
			Service: "app", File: "server/routes.go", Line: 64,
			Meta: map[string]string{"path": "/play/:gameID/history/navigate", "method": "POST"},
		},
	}
	edges, err := New(monolithConfig()).Link(nodes, nil)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, nodes[1].ID, edges[0].To)
	assert.Equal(t, "datastar_action", edges[0].Meta["via"])
}

// A fully-wildcarded datastar path (@get(url) → "*") carries no anchor, so it
// must NOT blind-match an arbitrary single-segment handler. It surfaces as an
// unresolved edge (never silently dropped) rather than a spurious link.
func TestLink_DatastarAllWildcardIsUnresolved(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:x.templ:http_client:GET:*:5", Type: graph.NodeTypeHTTPClient,
			Service: "app", File: "x.templ", Line: 5, Language: "templ",
			Meta: map[string]string{"path": "*", "method": "GET", "datastar": "true", "confidence": "partial"},
		},
		{
			ID: "app:handlers/foo.go:http_handler:GET /:id:20", Type: graph.NodeTypeHTTPHandler,
			Service: "app", File: "handlers/foo.go", Line: 20,
			Meta: map[string]string{"path": "/:id", "method": "GET"},
		},
	}
	edges, err := New(monolithConfig()).Link(nodes, nil)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, "unresolved", edges[0].To, "all-wildcard path must not match a specific handler")
	assert.Equal(t, graph.ConfidenceUnknown, edges[0].Confidence)
}

// A datastar partial path must not match a same-shape handler it shares no
// concrete segment with: "/play/*/draw" and "/*/goto/*" both normalize to three
// segments with a wildcard, but they are unrelated routes. Without a shared
// literal anchor the wildcards alone would align them — this must be rejected
// (surfaced as unresolved instead of a wrong link).
func TestLink_DatastarNoSharedAnchorIsUnresolved(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:play.templ:http_client:POST:/play/*/draw:12", Type: graph.NodeTypeHTTPClient,
			Service: "app", File: "play.templ", Line: 12, Language: "templ",
			Meta: map[string]string{"path": "/play/*/draw", "method": "POST", "datastar": "true", "confidence": "partial"},
		},
		{
			ID: "app:server/routes.go:http_handler:POST /:id/goto/:nodeID:80", Type: graph.NodeTypeHTTPHandler,
			Service: "app", File: "server/routes.go", Line: 80,
			Meta: map[string]string{"path": "/:id/goto/:nodeID", "method": "POST"},
		},
	}
	edges, err := New(monolithConfig()).Link(nodes, nil)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, "unresolved", edges[0].To, "unrelated same-shape route must not match on wildcards alone")
}

// Plain same-service HTTP (a non-datastar client) is still skipped: a "calls"
// edge already covers intra-service calls, so re-linking would double-count.
func TestLink_PlainHTTPSameServiceSkipped(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:client.go:http_client:GET:/rows:8", Type: graph.NodeTypeHTTPClient,
			Service: "app", File: "client.go", Line: 8, Language: "go",
			Meta: map[string]string{"path": "/rows", "method": "GET"},
		},
		{
			ID: "app:handlers/rows.go:http_handler:GET /rows:20", Type: graph.NodeTypeHTTPHandler,
			Service: "app", File: "handlers/rows.go", Line: 20,
			Meta: map[string]string{"path": "/rows", "method": "GET"},
		},
	}
	edges, err := New(monolithConfig()).Link(nodes, nil)
	require.NoError(t, err)
	assert.Empty(t, edges, "plain same-service HTTP is covered by a calls edge and must be skipped")
}
