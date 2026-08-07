package contract_test

// Tests for the embedded contracts/http.yaml rule file.
// These are the "fixture" tests required by phases.md: positive cases assert
// expected edges are emitted; negative cases assert silence or unresolved surfacing.
import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// loadHTTPRules loads the embedded contract rules and returns only the HTTP variants.
func loadHTTPRules(t *testing.T) []contract.Rule {
	t.Helper()
	all, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	var httpRules []contract.Rule
	for _, r := range all {
		if r.Kind == contract.KindHTTP {
			httpRules = append(httpRules, r)
		}
	}
	require.Len(t, httpRules, 2, "http.yaml must define exactly 2 variants (api-call + nav-link)")
	return httpRules
}

func runHTTP(t *testing.T, nodes []graph.Node, links []workspace.Link) contract.Result {
	t.Helper()
	rules := loadHTTPRules(t)
	e := &contract.Engine{}
	return e.Link(nodes, rules, links)
}

// ── Positive: API call variant ───────────────────────────────────────────────

func TestHTTPRule_APICall_ExactMatch(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/users"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-b",
			Meta: map[string]string{"method": "GET", "path": "/users"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "link:c1->h1", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypeHTTPCall, res.Edges[0].Type)
	assert.Equal(t, graph.ConfidenceStatic, res.Edges[0].Confidence)
}

func TestHTTPRule_APICall_ParamWildcard(t *testing.T) {
	// Client sends a literal ID; handler declares a route parameter.
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/users/123"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-b",
			Meta: map[string]string{"method": "GET", "path": "/users/:id"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "link:c1->h1", res.Edges[0].ID)
	assert.Equal(t, graph.ConfidenceInferred, res.Edges[0].Confidence)
}

func TestHTTPRule_APICall_URLFallback(t *testing.T) {
	// Client has url meta but no path; key_fallbacks must pick up url via url_to_path.
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "url": "https://api.svc-b.local/users"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-b",
			Meta: map[string]string{"method": "GET", "path": "/users"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "link:c1->h1", res.Edges[0].ID)
}

func TestHTTPRule_APICall_QueryStripAndParamWildcard(t *testing.T) {
	// Client path has query string; handler path has a param segment.
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"method": "POST", "path": "/play/*/history/navigate?direction=1"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-b",
			Meta: map[string]string{"method": "POST", "path": "/play/:gameID/history/navigate"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "link:c1->h1", res.Edges[0].ID)
}

func TestHTTPRule_APICall_MethodFallback_EmptyClientMethod(t *testing.T) {
	// Client has no method; method_fallback tries GET first.
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"path": "/users"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-b",
			Meta: map[string]string{"method": "GET", "path": "/users"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "link:c1->h1", res.Edges[0].ID)
}

func TestHTTPRule_APICall_BaseURLStrip(t *testing.T) {
	// ApplyHints already stripped /api from client path; base_url_strip strips it
	// from the handler path so both resolve to /users for matching.
	links := []workspace.Link{{From: "svc-a", To: "svc-b", BaseURL: "/api"}}
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/users", "target_service": "svc-b"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-b",
			Meta: map[string]string{"method": "GET", "path": "/api/users"}},
	}
	res := runHTTP(t, nodes, links)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "link:c1->h1", res.Edges[0].ID)
	assert.Equal(t, graph.ConfidenceInferred, res.Edges[0].Confidence)
}

func TestHTTPRule_DatastarSameService(t *testing.T) {
	// Datastar actions are the skip_unless_meta:datastar exception: a templ action
	// reaching its own handler should emit an http_call edge.
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "app",
			Meta: map[string]string{"method": "POST", "path": "/play/*/draw", "datastar": "true"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "app",
			Meta: map[string]string{"method": "POST", "path": "/play/:id/draw"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "link:c1->h1", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypeHTTPCall, res.Edges[0].Type)
	assert.Equal(t, "datastar_action", res.Edges[0].Meta["via"])
}

// ── Positive: nav-link variant ───────────────────────────────────────────────

func TestHTTPRule_NavLink_SameService(t *testing.T) {
	// A server-rendered template href pointing at its own route.
	nodes := []graph.Node{
		{ID: "nl1", Type: graph.NodeTypeHTTPClient, Service: "app",
			Meta: map[string]string{"method": "GET", "path": "/reports", "nav_link": "true"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "app",
			Meta: map[string]string{"method": "GET", "path": "/reports"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "nav:nl1->h1", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypeNavigatesTo, res.Edges[0].Type)
	assert.Equal(t, graph.ConfidenceStatic, res.Edges[0].Confidence)
	assert.Equal(t, "nav_link", res.Edges[0].Meta["via"])
}

func TestHTTPRule_NavLink_CrossService(t *testing.T) {
	nodes := []graph.Node{
		{ID: "nl1", Type: graph.NodeTypeHTTPClient, Service: "web",
			Meta: map[string]string{"method": "GET", "path": "/reports", "nav_link": "true"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "app",
			Meta: map[string]string{"method": "GET", "path": "/reports"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "nav:nl1->h1", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypeNavigatesTo, res.Edges[0].Type)
}

func TestHTTPRule_NavLink_FormMethod(t *testing.T) {
	// A POST form points at the POST handler, not the GET one.
	nodes := []graph.Node{
		{ID: "nl1", Type: graph.NodeTypeHTTPClient, Service: "app",
			Meta: map[string]string{"method": "POST", "path": "/users", "nav_link": "true"}},
		{ID: "h_get", Type: graph.NodeTypeHTTPHandler, Service: "app",
			Meta: map[string]string{"method": "GET", "path": "/users"}},
		{ID: "h_post", Type: graph.NodeTypeHTTPHandler, Service: "app",
			Meta: map[string]string{"method": "POST", "path": "/users"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "nav:nl1->h_post", res.Edges[0].ID)
}

// ── Negative fixtures ────────────────────────────────────────────────────────

// Negative: unmatched nav-link is silently dropped (no edge, no unresolved).
func TestHTTPRule_NavLink_Unmatched_Dropped(t *testing.T) {
	nodes := []graph.Node{
		{ID: "nl1", Type: graph.NodeTypeHTTPClient, Service: "app",
			Meta: map[string]string{"method": "GET", "path": "/external/page", "nav_link": "true"}},
	}
	res := runHTTP(t, nodes, nil)
	assert.Empty(t, res.Edges)
	assert.Empty(t, res.Nodes)
	assert.Empty(t, res.Unresolved)
}

// same_service=keep: a same-service non-datastar API call (a UI fetch to the
// service's own handler) IS a real internal edge and resolves. Before the keep
// switch this went to unresolved and mis-scored as a cross miss; keep moves the
// svc-c-mgr UI→own-backend family internal (cross_yield_static 0.193→0.348).
func TestHTTPRule_APICall_SameService_NonDatastar_Resolves(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "app",
			Meta: map[string]string{"method": "GET", "path": "/users"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "app",
			Meta: map[string]string{"method": "GET", "path": "/users"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "c1", res.Edges[0].From)
	assert.Equal(t, "h1", res.Edges[0].To)
	assert.NotEqual(t, graph.ConfidenceUnknown, res.Edges[0].Confidence)
}

// Negative: fully-wildcarded datastar path must not match any handler.
func TestHTTPRule_APICall_AllWildcard_Unresolved(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "app",
			Meta: map[string]string{"method": "GET", "path": "*", "datastar": "true"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "app",
			Meta: map[string]string{"method": "GET", "path": "/:id"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, graph.ConfidenceUnknown, res.Edges[0].Confidence,
		"all-wildcard path must not match a specific handler")
}

// Negative: no shared concrete segment between wildcard client and handler.
func TestHTTPRule_APICall_NoSharedAnchor_Unresolved(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "app",
			Meta: map[string]string{"method": "POST", "path": "/play/*/draw", "datastar": "true"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "app",
			Meta: map[string]string{"method": "POST", "path": "/:id/goto/:nodeID"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, graph.ConfidenceUnknown, res.Edges[0].Confidence,
		"unrelated same-shape routes must not match on wildcards alone")
}

// Negative: no nodes at all — both variants produce no output.
func TestHTTPRule_EmptyNodes(t *testing.T) {
	res := runHTTP(t, nil, nil)
	assert.Empty(t, res.Edges)
	assert.Empty(t, res.Nodes)
	assert.Empty(t, res.Unresolved)
}

// Negative: non-HTTP nodes produce no edges.
func TestHTTPRule_NonHTTPNodes_NoEdges(t *testing.T) {
	nodes := []graph.Node{
		{ID: "pub1", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"exchange": "orders", "routing_key": "placed"}},
		{ID: "sub1", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"exchange": "orders", "routing_key": "placed"}},
	}
	res := runHTTP(t, nodes, nil)
	assert.Empty(t, res.Edges, "non-HTTP node types must produce no edges under http.yaml rules")
}

// ── J.2c: empty-path guard + target_service allowlist ────────────────────────

// A client whose URL could not be resolved to any path must not exact-match
// every service's root handler. It falls to `unmatched` (unknown_edge) instead.
// The producer here has NO path at all — the axios/fetch shape behind all 88
// orion false positives — which is a different fact from an explicit "/".
func TestHTTPRule_EmptyPathProducerDoesNotMatchRootHandler(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "", "url": ""}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-b",
			Meta: map[string]string{"method": "GET", "path": "/"}},
		{ID: "h2", Type: graph.NodeTypeHTTPHandler, Service: "svc-c",
			Meta: map[string]string{"method": "GET", "path": "/"}},
		{ID: "h3", Type: graph.NodeTypeHTTPHandler, Service: "svc-d",
			Meta: map[string]string{"method": "GET", "path": "/"}},
	}
	res := runHTTP(t, nodes, nil)

	for _, e := range res.Edges {
		assert.Equal(t, "unresolved", e.To, "the only edge allowed is the ledger edge")
		assert.Equal(t, graph.ConfidenceUnknown, e.Confidence)
	}
	assert.Len(t, res.Edges, 1, "one visible ledger edge, not three phantom http_calls")
}

// An explicit root link is real, but it is same-origin: it reaches its own
// service's root route and no other service's (J.2c same_origin_relative).
func TestHTTPRule_RootNavLinkStaysSameOrigin(t *testing.T) {
	nodes := []graph.Node{
		{ID: "n1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "nav_link": "true", "path": "/"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/"}},
		{ID: "h2", Type: graph.NodeTypeHTTPHandler, Service: "svc-b",
			Meta: map[string]string{"method": "GET", "path": "/"}},
		{ID: "h3", Type: graph.NodeTypeHTTPHandler, Service: "svc-c",
			Meta: map[string]string{"method": "GET", "path": "/"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1, "the own-service root route only")
	assert.Equal(t, "nav:n1->h1", res.Edges[0].ID)
}

// An absolute href genuinely crosses the origin and is left alone.
func TestHTTPRule_AbsoluteRootNavLinkStillCrossesServices(t *testing.T) {
	nodes := []graph.Node{
		{ID: "n1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "nav_link": "true", "url": "https://svc-b.internal/"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-b",
			Meta: map[string]string{"method": "GET", "path": "/"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "nav:n1->h1", res.Edges[0].ID)
}

// A wildcard-only path (all that survives of a URL whose host was the only
// resolvable part) is voided the same way.
func TestHTTPRule_AllWildcardPathVoided(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "*"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-b",
			Meta: map[string]string{"method": "GET", "path": "/:id"}},
	}
	res := runHTTP(t, nodes, nil)
	for _, e := range res.Edges {
		assert.Equal(t, "unresolved", e.To)
	}
}

// A real path still matches: the guard must not cost recall.
func TestHTTPRule_EmptyPathGuardKeepsRealPaths(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/api/v1/users"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-b",
			Meta: map[string]string{"method": "GET", "path": "/api/v1/users"}},
	}
	res := runHTTP(t, nodes, nil)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "link:c1->h1", res.Edges[0].ID)
}

// J.2c end to end: an env-var hint narrows a generic /health call from four
// candidate services to the one the workspace declares.
func TestHTTPRule_TargetServiceNarrowsHealthFanout(t *testing.T) {
	links := []workspace.Link{
		{From: "migrator", To: "maple-manager", Hint: "MAPLE_MANAGER_API_URL"},
	}
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "migrator",
			Meta: map[string]string{"method": "GET", "path": "*/health", "target_service": "maple-manager"}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "maple-manager",
			Meta: map[string]string{"method": "GET", "path": "/health"}},
		{ID: "h2", Type: graph.NodeTypeHTTPHandler, Service: "maple-agent",
			Meta: map[string]string{"method": "GET", "path": "/health"}},
		{ID: "h3", Type: graph.NodeTypeHTTPHandler, Service: "willow",
			Meta: map[string]string{"method": "GET", "path": "/health"}},
		{ID: "h4", Type: graph.NodeTypeHTTPHandler, Service: "orion",
			Meta: map[string]string{"method": "GET", "path": "/health"}},
	}
	res := runHTTP(t, nodes, links)
	require.Len(t, res.Edges, 1, "the allowlist must leave exactly one candidate")
	assert.Equal(t, "link:c1->h1", res.Edges[0].ID)
}
