package contract_test

// Tests for the G.3 route-group meta-enrichment pass (EnrichRouteGroups).
// Positive fixtures assert that group prefixes are stamped into route meta;
// negative fixtures assert that routes outside groups, cross-file groups, and
// non-router-group nodes are not spuriously modified.

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func ginGroupNode(id, file, varName, prefix, receiver string, line int) graph.Node {
	return graph.Node{
		ID: id, Type: graph.NodeTypeHTTPHandler, File: file, Line: line,
		Meta: map[string]string{
			"pattern":  "gin_route_group",
			"var_name": varName,
			"prefix":   prefix,
			"receiver": receiver,
		},
	}
}

func ginRouteNode(id, file, router, method, path string, line int) graph.Node {
	return graph.Node{
		ID: id, Type: graph.NodeTypeHTTPHandler, File: file, Line: line,
		Meta: map[string]string{
			"pattern": "gin_route",
			"router":  router,
			"method":  method,
			"path":    path,
		},
	}
}

func chiGroupNode(id, file, prefix string, line, endLine int) graph.Node {
	endStr := ""
	if endLine > 0 {
		endStr = fmt.Sprintf("%d", endLine)
	}
	return graph.Node{
		ID: id, Type: graph.NodeTypeHTTPHandler, File: file, Line: line,
		Meta: map[string]string{
			"pattern":  "chi_route_group",
			"prefix":   prefix,
			"end_line": endStr,
		},
	}
}

func chiRouteNode(id, file, router, method, path string, line int) graph.Node {
	return graph.Node{
		ID: id, Type: graph.NodeTypeHTTPHandler, File: file, Line: line,
		Meta: map[string]string{
			"pattern": "chi_get",
			"router":  router,
			"method":  method,
			"path":    path,
		},
	}
}

// ── Positive: gin single-level group ──────────────────────────────────────────

func TestEnrichRouteGroups_Gin_SingleGroup(t *testing.T) {
	nodes := []graph.Node{
		ginGroupNode("g1", "routes.go", "api", "/api/v1", "r", 3),
		ginRouteNode("h1", "routes.go", "api", "GET", "/users", 4),
		ginRouteNode("h2", "routes.go", "api", "POST", "/users", 5),
		ginRouteNode("h3", "routes.go", "r", "GET", "/health", 2),
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/api/v1/users", byID["h1"].Meta["path"], "group prefix must be prepended")
	assert.Equal(t, "/api/v1/users", byID["h2"].Meta["path"], "group prefix must be prepended")
	assert.Equal(t, "/health", byID["h3"].Meta["path"], "root-level route must be unchanged")
}

// ── Positive: gin nested groups ───────────────────────────────────────────────

func TestEnrichRouteGroups_Gin_NestedGroups(t *testing.T) {
	nodes := []graph.Node{
		ginGroupNode("g1", "routes.go", "v1", "/v1", "r", 3),
		ginGroupNode("g2", "routes.go", "v2", "/v2", "v1", 4),
		ginRouteNode("h1", "routes.go", "v2", "GET", "/users", 5),
		ginRouteNode("h2", "routes.go", "v1", "GET", "/health", 6),
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/v1/v2/users", byID["h1"].Meta["path"])
	assert.Equal(t, "/v1/health", byID["h2"].Meta["path"])
}

// ── Positive: chi single-level group ─────────────────────────────────────────

func TestEnrichRouteGroups_Chi_SingleGroup(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "g1", Type: graph.NodeTypeHTTPHandler, File: "routes.go", Line: 10,
			Meta: map[string]string{
				"pattern":  "chi_route_group",
				"prefix":   "/admin",
				"end_line": "13",
			},
		},
		{
			ID: "h1", Type: graph.NodeTypeHTTPHandler, File: "routes.go", Line: 11,
			Meta: map[string]string{"pattern": "chi_get", "router": "r", "method": "GET", "path": "/stats"},
		},
		{
			ID: "h2", Type: graph.NodeTypeHTTPHandler, File: "routes.go", Line: 12,
			Meta: map[string]string{"pattern": "chi_get", "router": "r", "method": "POST", "path": "/users"},
		},
		{
			ID: "h3", Type: graph.NodeTypeHTTPHandler, File: "routes.go", Line: 15,
			Meta: map[string]string{"pattern": "chi_get", "router": "r", "method": "GET", "path": "/public"},
		},
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/admin/stats", byID["h1"].Meta["path"], "route inside group must get prefix")
	assert.Equal(t, "/admin/users", byID["h2"].Meta["path"], "route inside group must get prefix")
	assert.Equal(t, "/public", byID["h3"].Meta["path"], "route outside group body must be unchanged")
}

// ── Positive: chi nested groups ───────────────────────────────────────────────

func TestEnrichRouteGroups_Chi_NestedGroups(t *testing.T) {
	// Outer group: lines 10–20, inner group: lines 14–18
	nodes := []graph.Node{
		{
			ID: "g_outer", Type: graph.NodeTypeHTTPHandler, File: "routes.go", Line: 10,
			Meta: map[string]string{"pattern": "chi_route_group", "prefix": "/api", "end_line": "20"},
		},
		{
			ID: "g_inner", Type: graph.NodeTypeHTTPHandler, File: "routes.go", Line: 14,
			Meta: map[string]string{"pattern": "chi_route_group", "prefix": "/v1", "end_line": "18"},
		},
		{
			ID: "h_inner", Type: graph.NodeTypeHTTPHandler, File: "routes.go", Line: 15,
			Meta: map[string]string{"pattern": "chi_get", "method": "GET", "path": "/users"},
		},
		{
			ID: "h_outer", Type: graph.NodeTypeHTTPHandler, File: "routes.go", Line: 11,
			Meta: map[string]string{"pattern": "chi_get", "method": "GET", "path": "/health"},
		},
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/api/v1/users", byID["h_inner"].Meta["path"], "nested prefixes must be chained outermost-first")
	assert.Equal(t, "/api/health", byID["h_outer"].Meta["path"], "outer-only prefix for routes in outer group only")
}

// ── Positive: original node slice is not mutated ──────────────────────────────

func TestEnrichRouteGroups_DoesNotMutateInput(t *testing.T) {
	nodes := []graph.Node{
		ginGroupNode("g1", "routes.go", "api", "/api", "r", 1),
		ginRouteNode("h1", "routes.go", "api", "GET", "/users", 2),
	}
	originalPath := nodes[1].Meta["path"]
	contract.EnrichRouteGroups(nodes)
	assert.Equal(t, originalPath, nodes[1].Meta["path"], "original slice must not be modified")
}

// ── Negative: routes in a different file are not enriched ─────────────────────

func TestEnrichRouteGroups_CrossFile_NoEnrich(t *testing.T) {
	nodes := []graph.Node{
		ginGroupNode("g1", "routes_a.go", "api", "/api", "r", 1),
		ginRouteNode("h1", "routes_b.go", "api", "GET", "/users", 2),
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/users", byID["h1"].Meta["path"], "route in different file must not receive prefix")
}

// ── Negative: non-http_handler nodes are never modified ──────────────────────

func TestEnrichRouteGroups_NonHTTPHandler_Unchanged(t *testing.T) {
	nodes := []graph.Node{
		ginGroupNode("g1", "routes.go", "api", "/api", "r", 1),
		{
			ID: "pub1", Type: graph.NodeTypePublisher, File: "routes.go", Line: 2,
			Meta: map[string]string{"router": "api", "path": "/events"},
		},
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/events", byID["pub1"].Meta["path"], "non-http_handler nodes must not be enriched")
}

// ── Negative: gin route with unrecognised router variable is unchanged ─────────

func TestEnrichRouteGroups_Gin_UnknownRouter_Unchanged(t *testing.T) {
	nodes := []graph.Node{
		ginGroupNode("g1", "routes.go", "api", "/api", "r", 1),
		ginRouteNode("h1", "routes.go", "other", "GET", "/users", 2),
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/users", byID["h1"].Meta["path"], "unrecognised router variable must not receive prefix")
}

// ── Negative: chi route at boundary line (group.line itself) is not inside ────

func TestEnrichRouteGroups_Chi_BoundaryLineNotInside(t *testing.T) {
	// Route at the same line as the group call is not "inside" the func literal.
	nodes := []graph.Node{
		{
			ID: "g1", Type: graph.NodeTypeHTTPHandler, File: "routes.go", Line: 10,
			Meta: map[string]string{"pattern": "chi_route_group", "prefix": "/admin", "end_line": "14"},
		},
		{
			ID: "h_at_boundary", Type: graph.NodeTypeHTTPHandler, File: "routes.go", Line: 10,
			Meta: map[string]string{"pattern": "chi_get", "method": "GET", "path": "/same-line"},
		},
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/same-line", byID["h_at_boundary"].Meta["path"],
		"route at group's own line is not inside the func literal body")
}

// ── Negative: chi group with no end_line does not enrich any routes ───────────

func TestEnrichRouteGroups_Chi_NoEndLine_NoEnrich(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "g1", Type: graph.NodeTypeHTTPHandler, File: "routes.go", Line: 10,
			Meta: map[string]string{"pattern": "chi_route_group", "prefix": "/admin"},
			// end_line absent — body range unknown; no containment possible
		},
		{
			ID: "h1", Type: graph.NodeTypeHTTPHandler, File: "routes.go", Line: 11,
			Meta: map[string]string{"pattern": "chi_get", "method": "GET", "path": "/stats"},
		},
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/stats", byID["h1"].Meta["path"],
		"chi group without end_line must not enrich any routes")
}

// Regression: the pattern matcher captures route-group prefixes as raw source
// text — real graphs carried `"\"/play\""` (with quote characters), which the
// enrichment concatenated into unmatchable paths like `"/play"/:id/draw`.
// Discovered by the chessleap eval corpus (all datastar cases hard-failed).
// The matcher now strips quotes at extraction; this test guards the
// defense-in-depth strip for quoted prefixes still present in node meta.
func TestEnrichRouteGroups_QuotedPrefixStripped(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "svc:routes.go:group:10", Type: graph.NodeTypeHTTPHandler,
			Service: "svc", File: "routes.go", Line: 10,
			Meta: map[string]string{
				"pattern":  "gin_route_group",
				"var_name": "playAuth",
				"receiver": "r",
				"prefix":   `"/play"`, // raw capture with quotes
			},
		},
		{
			ID: "svc:routes.go:route:20", Type: graph.NodeTypeHTTPHandler,
			Service: "svc", File: "routes.go", Line: 20,
			Meta: map[string]string{
				"pattern": "gin_route",
				"router":  "playAuth",
				"method":  "POST",
				"path":    "/:gameID/draw",
			},
		},
	}

	out := contract.EnrichRouteGroups(nodes)

	var route *graph.Node
	for i := range out {
		if out[i].Meta["pattern"] == "gin_route" {
			route = &out[i]
		}
	}
	require.NotNil(t, route)
	assert.Equal(t, "/play/:gameID/draw", route.Meta["path"],
		"quoted prefix must be stripped before concatenation")
}
