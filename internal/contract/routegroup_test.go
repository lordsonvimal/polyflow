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

// ── Positive: gin empty-prefix (middleware-only) group in the chain ───────────

// X.8: `protected := v1.Group("")` adds middleware but no path segment. It must
// still forward v1's prefix to its nested routes/groups — the real svc-b
// api_routes.go shape, where every /api/v1 route is mounted under an empty-prefix
// group. The old `pfx == ""` skip dropped `protected` from the known set, so its
// children were treated as root-level and silently lost `/api/v1`.
func TestEnrichRouteGroups_Gin_EmptyPrefixGroupInChain(t *testing.T) {
	nodes := []graph.Node{
		ginGroupNode("g1", "routes.go", "v1", "/api/v1", "r", 3),
		ginGroupNode("g2", "routes.go", "protected", "", "v1", 4),   // middleware-only
		ginGroupNode("g3", "routes.go", "apps", "/apps", "protected", 5),
		ginRouteNode("h1", "routes.go", "apps", "GET", "/:id", 6),   // → /api/v1/apps/:id
		ginRouteNode("h2", "routes.go", "protected", "GET", "/me", 7), // → /api/v1/me
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/api/v1/apps/:id", byID["h1"].Meta["path"],
		"empty-prefix group must forward the parent prefix to nested routes")
	assert.Equal(t, "/api/v1/me", byID["h2"].Meta["path"],
		"route directly on the empty-prefix group keeps the parent prefix")
}

// ── Positive: gin cross-function registrar (X.9) ──────────────────────────────

func ginRegistrarFuncNode(id, file, name, param string, line int) graph.Node {
	return graph.Node{
		ID: id, Type: graph.NodeTypeVariable, File: file, Line: line,
		Meta: map[string]string{
			"pattern": "gin_group_registrar_func",
			"name":    name,
			"param":   param,
		},
	}
}

func ginRegistrarCallNode(id, file, callee, arg string, line int) graph.Node {
	return graph.Node{
		ID: id, Type: graph.NodeTypeVariable, File: file, Line: line,
		Meta: map[string]string{
			"pattern": "gin_group_registrar_call",
			"callee":  callee,
			"arg":     arg,
		},
	}
}

// The svc-c-mgr shape: views.go builds `execCfgView` (→ /maple) and passes it to
// registerConfigAssociationViewRoutes, which is defined in another file and
// registers `rg.GET("/config-associations")`. X.9 seeds `rg` from the caller so
// the route composes to /maple/config-associations.
func TestEnrichRouteGroups_Gin_CrossFunctionRegistrar(t *testing.T) {
	nodes := []graph.Node{
		// caller file
		ginGroupNode("g1", "views.go", "mapleManager", "/maple", "r", 3),
		ginGroupNode("g2", "views.go", "execCfgView", "", "mapleManager", 4), // middleware-only
		ginRegistrarCallNode("c1", "views.go", "registerConfigAssociationViewRoutes", "execCfgView", 5),
		// callee file (different file, group arrives as a parameter)
		ginRegistrarFuncNode("f1", "assoc.go", "registerConfigAssociationViewRoutes", "rg", 1),
		ginRouteNode("h1", "assoc.go", "rg", "GET", "/config-associations", 2),
		// a group nested on top of the seeded parameter must still compose
		ginGroupNode("g3", "assoc.go", "sub", "/v2", "rg", 3),
		ginRouteNode("h2", "assoc.go", "sub", "GET", "/deep", 4),
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/maple/config-associations", byID["h1"].Meta["path"],
		"registrar param must be seeded with the caller's resolved prefix")
	assert.Equal(t, "/maple/v2/deep", byID["h2"].Meta["path"],
		"a group nested on the seeded parameter must compose on top of it")
}

// Two callers passing different prefixes to the same registrar is a real
// ambiguity — X.9 seeds the lexicographically-first deterministically, never
// depending on node order.
func TestEnrichRouteGroups_Gin_CrossFunctionRegistrar_Ambiguous(t *testing.T) {
	nodes := []graph.Node{
		ginGroupNode("g1", "b_caller.go", "gb", "/beta", "r", 3),
		ginRegistrarCallNode("c1", "b_caller.go", "registerX", "gb", 4),
		ginGroupNode("g2", "a_caller.go", "ga", "/alpha", "r", 3),
		ginRegistrarCallNode("c2", "a_caller.go", "registerX", "ga", 4),
		ginRegistrarFuncNode("f1", "reg.go", "registerX", "rg", 1),
		ginRouteNode("h1", "reg.go", "rg", "GET", "/thing", 2),
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/alpha/thing", byID["h1"].Meta["path"],
		"ambiguous registrar must resolve to the lexicographically-first prefix")
}

// A registrar call in a test file must not seed the real service's registrar
// parameter — only production wiring counts.
func TestEnrichRouteGroups_Gin_CrossFunctionRegistrar_SkipsTestFile(t *testing.T) {
	nodes := []graph.Node{
		ginGroupNode("g1", "assoc_test.go", "rgTest", "/maple", "r", 3),
		ginRegistrarCallNode("c1", "assoc_test.go", "registerX", "rgTest", 4),
		ginRegistrarFuncNode("f1", "assoc.go", "registerX", "rg", 1),
		ginRouteNode("h1", "assoc.go", "rg", "GET", "/thing", 2),
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/thing", byID["h1"].Meta["path"],
		"a test-file registrar call must not seed the production registrar param")
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

// ── full_path + label: what the persisted graph shows an agent ────────────────

// ginRouteNodeLabelled is ginRouteNode with the `method + " " + path` label the
// pattern matcher actually mints, so the label-rewrite path is exercised.
func ginRouteNodeLabelled(id, file, router, method, path string, line int) graph.Node {
	n := ginRouteNode(id, file, router, method, path, line)
	n.Label = method + " " + path
	return n
}

// The composed route is the only form a caller can use. nodes_fts indexes label,
// not meta, so without the label rewrite a search for the real path finds nothing.
func TestEnrichRouteGroups_StampsFullPathAndLabel(t *testing.T) {
	nodes := []graph.Node{
		ginGroupNode("g1", "routes.go", "v1", "/api/v1", "r", 3),
		ginGroupNode("g2", "routes.go", "admin", "/admin", "v1", 4),
		ginRouteNodeLabelled("h1", "routes.go", "admin", "GET", "/users/:id", 5),
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/api/v1/admin/users/:id", byID["h1"].Meta["path"])
	assert.Equal(t, "/api/v1/admin/users/:id", byID["h1"].Meta["full_path"],
		"the composed route must be recorded for the persisted graph")
	assert.Equal(t, "GET /api/v1/admin/users/:id", byID["h1"].Label,
		"label carries the composed route; nodes_fts indexes label, not meta")
}

// An empty route literal (`camUsers.GET("")`) is the shape that made the
// willow Atlas provisioning routes unfindable: path "" and label "GET ".
func TestEnrichRouteGroups_EmptyRouteLiteralGetsFullPath(t *testing.T) {
	nodes := []graph.Node{
		ginGroupNode("g1", "routes.go", "v1", "/api/v1", "r", 3),
		ginGroupNode("g2", "routes.go", "camUsers", "/users", "v1", 4),
		ginRouteNodeLabelled("h1", "routes.go", "camUsers", "GET", "", 5),
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/api/v1/users", byID["h1"].Meta["full_path"])
	assert.Equal(t, "GET /api/v1/users", byID["h1"].Label)
}

// Idempotency is what lets meta["path"] stay raw in the store: re-running the
// pass over an already-enriched node must not stack the prefix a second time.
func TestEnrichRouteGroups_IsIdempotent(t *testing.T) {
	nodes := []graph.Node{
		ginGroupNode("g1", "routes.go", "v1", "/api/v1", "r", 3),
		ginRouteNodeLabelled("h1", "routes.go", "v1", "GET", "/users", 4),
	}
	once := contract.EnrichRouteGroups(nodes)
	twice := contract.EnrichRouteGroups(once)

	byID := make(map[string]graph.Node)
	for _, n := range twice {
		byID[n.ID] = n
	}
	assert.Equal(t, "/api/v1/users", byID["h1"].Meta["path"],
		"re-enriching an already-composed node must not double-prefix")
	assert.Equal(t, "GET /api/v1/users", byID["h1"].Label)
}

// A route outside any group is not composed, so it must gain no full_path and
// keep its label untouched.
func TestEnrichRouteGroups_UngroupedRouteHasNoFullPath(t *testing.T) {
	nodes := []graph.Node{
		ginGroupNode("g1", "routes.go", "api", "/api/v1", "r", 3),
		ginRouteNodeLabelled("h1", "routes.go", "r", "GET", "/health", 2),
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Empty(t, byID["h1"].Meta["full_path"], "an uncomposed route has no distinct full path")
	assert.Equal(t, "GET /health", byID["h1"].Label)
}

// ── HH.3: groups arrive typed route_group ────────────────────────────────────

// retype returns n with its Type replaced — HH.3 mints gin/chi group nodes as
// route_group rather than http_handler.
func retype(n graph.Node, typ graph.NodeType) graph.Node {
	n.Type = typ
	return n
}

// TestEnrichRouteGroups_AcceptsRouteGroupType is the HH.3 regression guard for
// this pass. Every other fixture in this file hand-builds its group nodes as
// http_handler, so all of them would keep passing even if the harvest loop
// rejected the new type — and the symptom would not be a failing test but a
// fleet-wide silent loss of every gin route's prefix. This asserts the same
// enrichment against groups typed the way the parser now emits them.
func TestEnrichRouteGroups_AcceptsRouteGroupType(t *testing.T) {
	nodes := []graph.Node{
		retype(ginGroupNode("g1", "routes.go", "v1", "/v1", "r", 3), graph.NodeTypeRouteGroup),
		retype(ginGroupNode("g2", "routes.go", "v2", "/v2", "v1", 4), graph.NodeTypeRouteGroup),
		ginRouteNode("h1", "routes.go", "v2", "GET", "/users", 5),
		retype(chiGroupNode("c1", "chi.go", "/admin", 1, 20), graph.NodeTypeRouteGroup),
		chiRouteNode("h2", "chi.go", "r", "GET", "/stats", 5),
	}
	out := contract.EnrichRouteGroups(nodes)
	byID := make(map[string]graph.Node)
	for _, n := range out {
		byID[n.ID] = n
	}
	assert.Equal(t, "/v1/v2/users", byID["h1"].Meta["path"],
		"a route_group-typed gin group must still contribute its prefix")
	assert.Equal(t, "/admin/stats", byID["h2"].Meta["path"],
		"a route_group-typed chi group must still contribute its prefix")

	// The groups themselves stay unstamped: no path is invented for them.
	assert.Empty(t, byID["g1"].Meta["path"])
	assert.Empty(t, byID["c1"].Meta["path"])
}
