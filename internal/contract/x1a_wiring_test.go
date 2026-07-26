package contract_test

// X.1 end-to-end regression tests: real parser → matcher → MatchToGraph →
// EnrichAliases → contract.Engine.Link, proving the live WalkKey wiring
// (X.1a) and template reconstruction (X.1b) both fire on actual source
// (bug-class #6 — hand-built nodes alone are insufficient evidence). Covers
// the two worked examples from docs/systemic-gaps-plan.md Phase X.1:
//   - a JSX ternary href still fans out via key_candidates after
//     nav_links.yaml's rewrite from the JSX-only @branch_N convention to the
//     general WalkKey routing (no regression);
//   - a Go resty.New().R().Get(fmt.Sprintf(...)) call — previously silently
//     producing zero nodes (resty_get's literal-quote gate) — now resolves
//     to a real http_call edge via template reconstruction +
//     dynamic_host_strip, with a precision-negative proving target-service
//     scoping still applies to the reconstructed key.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// matchGo/matchJS/matchRuby run the real tree-sitter matcher over a source
// snippet using the embedded pattern registry, mirroring what the indexer
// does for a single file.
func matchWithGrammar(t *testing.T, lang, grammar, file, src string) []patterns.MatchResult {
	t.Helper()
	reg, err := patterns.EmbeddedRegistry()
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	results, err := m.MatchWithGrammar(lang, grammar, file, []byte(src))
	require.NoError(t, err)
	return results
}

func TestX1a_JSXTernaryHref_StillFansOutThroughWalker(t *testing.T) {
	src := `
const Nav = ({ isAdmin }) => (
  <a href={isAdmin ? "/admin" : "/dashboard"}>Go</a>
);
`
	results := matchWithGrammar(t, "jsx", "tsx", "nav.tsx", src)
	nodes, _, _ := patterns.MatchToGraph("svc-a", results)

	var navNodes []graph.Node
	for _, n := range nodes {
		if n.Meta["nav_link"] == "true" {
			navNodes = append(navNodes, n)
		}
	}
	require.Len(t, navNodes, 1, "one nav_link node from the ternary href")
	nav := navNodes[0]
	assert.Equal(t, "true", nav.Meta["nav_link"])
	require.NotEmpty(t, nav.Meta["key_candidates"], "ternary of literals must produce key_candidates, not key_dynamic")
	assert.Empty(t, nav.Meta["key_dynamic"])

	handlers := []graph.Node{
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/admin"}},
		{ID: "h2", Type: graph.NodeTypeHTTPHandler, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/dashboard"}},
	}
	all := append([]graph.Node{nav}, handlers...)

	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	enriched, ledger := contract.EnrichAliases(all)
	require.Empty(t, ledger)

	eng := &contract.Engine{}
	result := eng.Link(enriched, rules, nil)

	require.Len(t, result.Edges, 2, "fan-out: one navigates_to edge per ternary branch")
	targets := map[string]bool{}
	for _, e := range result.Edges {
		assert.Equal(t, graph.EdgeTypeNavigatesTo, e.Type)
		assert.Equal(t, graph.ConfidenceInferred, e.Confidence)
		targets[e.To] = true
	}
	assert.True(t, targets["h1"])
	assert.True(t, targets["h2"])
}

func TestX1a_JSXDynamicHref_ReachesLedger(t *testing.T) {
	src := `
const Nav = ({ target }) => (
  <a href={target}>Go</a>
);
`
	results := matchWithGrammar(t, "jsx", "tsx", "nav.tsx", src)
	nodes, _, _ := patterns.MatchToGraph("svc-a", results)

	var nav *graph.Node
	for i := range nodes {
		if nodes[i].Meta["nav_link"] == "true" {
			nav = &nodes[i]
		}
	}
	require.NotNil(t, nav, "dynamic href still produces a nav_link node (never silently dropped)")
	assert.Equal(t, "true", nav.Meta["key_dynamic"])
	assert.Equal(t, "target", nav.Meta["key_dynamic_raw"])
	assert.Empty(t, nav.Meta["path"], "dynamic field is cleared, not left as raw garbage text")

	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	enriched, ledger := contract.EnrichAliases([]graph.Node{*nav})
	require.Empty(t, ledger)

	eng := &contract.Engine{}
	result := eng.Link(enriched, rules, nil)

	assert.Empty(t, result.Edges, "dynamic nav link never fabricates an edge")
	require.Len(t, result.Unresolved, 1, "dynamic nav link reaches the ledger — never silently dropped")
	assert.Equal(t, "dynamic_url", result.Unresolved[0].Kind)
}

// TestX1a_RestySprintfURL_DirectChain_ResolvesToRealEdge is the plan's
// svc-c worked example end to end: resty.New().R().Get(fmt.Sprintf(
// "%s/api/v1/builds/%s", cfg.AgentURL, buildID)) previously produced zero
// nodes at all (resty_get's literal-quote gate); X.1a made it a
// key_dynamic-ledgered node; X.1b's template reconstruction plus
// dynamic_host_strip now resolves it to a real http_call edge.
func TestX1a_RestySprintfURL_DirectChain_ResolvesToRealEdge(t *testing.T) {
	src := `
package client

func fetchBuild(cfg Config, buildID string) {
	resty.New().R().Get(fmt.Sprintf("%s/api/v1/builds/%s", cfg.AgentURL, buildID))
}
`
	results := matchWithGrammar(t, "go", "go", "client.go", src)
	nodes, _, _ := patterns.MatchToGraph("svc-c-mgr", results)

	var client *graph.Node
	for i := range nodes {
		if nodes[i].Meta["pattern"] == "resty_get_dynamic" {
			client = &nodes[i]
		}
	}
	require.NotNil(t, client, "resty.New().R().Get(fmt.Sprintf(...)) must produce a node — "+
		"resty_get's literal-quote gate silently dropped this before X.1a")
	assert.Equal(t, graph.NodeTypeHTTPClient, client.Type)
	assert.Empty(t, client.Meta["key_dynamic"], "X.1b: reconstructed to a static-shaped template, not dynamic")
	assert.Equal(t, "*/api/v1/builds/*", client.Meta["url"])

	handler := graph.Node{
		ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-c-agent",
		Meta: map[string]string{"method": "GET", "path": "/api/v1/builds/*"},
	}
	all := append(append([]graph.Node{}, nodes...), handler)

	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	enriched, ledger := contract.EnrichAliases(all)
	require.Empty(t, ledger)

	eng := &contract.Engine{}
	result := eng.Link(enriched, rules, nil)

	var edge *graph.Edge
	for i := range result.Edges {
		if result.Edges[i].From == client.ID {
			edge = &result.Edges[i]
		}
	}
	require.NotNil(t, edge, "template reconstruction + dynamic_host_strip must resolve to a real edge")
	assert.Equal(t, "h1", edge.To)
	assert.Equal(t, graph.EdgeTypeHTTPCall, edge.Type)
	assert.Equal(t, graph.ConfidenceInferred, edge.Confidence)
}

// TestX1a_RestySprintfURL_WrongService_NoCrossEdge is the precision-negative
// half of the same worked example: a reconstructed template must still
// respect target-service scoping — it does not blindly match every handler
// with the same shape in an unrelated service.
func TestX1a_RestySprintfURL_WrongService_NoCrossEdge(t *testing.T) {
	src := `
package client

func fetchBuild(cfg Config, buildID string) {
	resty.New().R().Get(fmt.Sprintf("%s/api/v1/builds/%s", cfg.AgentURL, buildID))
}
`
	results := matchWithGrammar(t, "go", "go", "client.go", src)
	nodes, _, _ := patterns.MatchToGraph("svc-c-mgr", results)
	for i := range nodes {
		if nodes[i].Meta["pattern"] == "resty_get_dynamic" {
			nodes[i].Meta["target_service"] = "svc-c-agent"
		}
	}

	wrongHandler := graph.Node{
		ID: "h-wrong", Type: graph.NodeTypeHTTPHandler, Service: "unrelated-svc",
		Meta: map[string]string{"method": "GET", "path": "/api/v1/builds/*"},
	}
	all := append(append([]graph.Node{}, nodes...), wrongHandler)

	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	enriched, ledger := contract.EnrichAliases(all)
	require.Empty(t, ledger)

	eng := &contract.Engine{}
	result := eng.Link(enriched, rules, nil)

	for _, e := range result.Edges {
		assert.NotEqual(t, "h-wrong", e.To, "target_service scoping must block a same-shape handler in the wrong service")
	}
}

// TestX1b_PusherRoomChannel_CrossLanguageFanOut is the plan's MainSvc worked
// example: a Ruby pusher.trigger("room-#{room.id}", ...) producer must fan
// out to every JS pusher.subscribe("room-"+id) consumer sharing the same
// reconstructed "room-*" template (bug-class #1: fan-out, never first-match).
func TestX1b_PusherRoomChannel_CrossLanguageFanOut(t *testing.T) {
	rubySrc := `
def broadcast_move(room)
  pusher.trigger("room-#{room.id}", "message", payload)
end
`
	jsSrc := `
p.subscribe("room-" + idA);
p.subscribe("room-" + idB);
`
	rubyResults := matchWithGrammar(t, "ruby", "ruby", "broadcast.rb", rubySrc)
	rubyNodes, _, _ := patterns.MatchToGraph("main-svc", rubyResults)

	jsResults := matchWithGrammar(t, "javascript", "javascript", "subscribe.js", jsSrc)
	jsNodes, _, _ := patterns.MatchToGraph("main-svc", jsResults)

	var producer *graph.Node
	for i := range rubyNodes {
		if rubyNodes[i].Meta["pattern"] == "pusher_trigger" {
			producer = &rubyNodes[i]
		}
	}
	require.NotNil(t, producer)
	assert.Equal(t, "room-*", producer.Meta["channel"])
	assert.Empty(t, producer.Meta["key_dynamic"])

	var subscribers []graph.Node
	for _, n := range jsNodes {
		if n.Meta["pattern"] == "pusher_subscribe_client" {
			assert.Equal(t, "room-*", n.Meta["channel"])
			subscribers = append(subscribers, n)
		}
	}
	require.Len(t, subscribers, 2)

	all := append([]graph.Node{*producer}, subscribers...)
	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	enriched, ledger := contract.EnrichAliases(all)
	require.Empty(t, ledger)

	eng := &contract.Engine{}
	result := eng.Link(enriched, rules, nil)

	require.Len(t, result.Edges, 2, "one pusher_trigger edge per subscriber sharing the reconstructed channel")
	targets := map[string]bool{}
	for _, e := range result.Edges {
		assert.Equal(t, graph.EdgeTypePusherTrigger, e.Type)
		targets[e.To] = true
	}
	assert.True(t, targets[subscribers[0].ID])
	assert.True(t, targets[subscribers[1].ID])
}

// TestX1b_FullyWildcardKey_ReachesLedger_NotSilentlyMatched proves the
// engine-level precision guard end to end: a producer whose reconstruction
// has no concrete segment at all (two concatenated identifiers, no literal
// anchor) must reach the dynamic ledger rather than becoming a wildcard-only
// key that could spuriously match an unrelated route.
func TestX1b_FullyWildcardKey_ReachesLedger_NotSilentlyMatched(t *testing.T) {
	src := `
package client

func fetchSomething(a, b string) {
	http.Get(a + b)
}
`
	results := matchWithGrammar(t, "go", "go", "client.go", src)
	nodes, _, _ := patterns.MatchToGraph("svc-a", results)

	var client *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeHTTPClient {
			client = &nodes[i]
		}
	}
	require.NotNil(t, client)
	assert.Equal(t, "true", client.Meta["key_dynamic"], "no concrete anchor in a+b — must not become a bare wildcard key")

	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	enriched, ledger := contract.EnrichAliases(nodes)
	require.Empty(t, ledger)

	// A handler whose own path also normalizes to "*" would otherwise be a
	// spurious match target if the producer's key were wildcard-only.
	handler := graph.Node{
		ID: "h-anything", Type: graph.NodeTypeHTTPHandler, Service: "svc-a",
		Meta: map[string]string{"method": "GET", "path": "/*"},
	}
	eng := &contract.Engine{}
	result := eng.Link(append(enriched, handler), rules, nil)

	assert.Empty(t, result.Edges, "a fully-wildcard reconstruction must never fabricate an edge")
	var found bool
	for _, u := range result.Unresolved {
		if u.Kind == "dynamic_url" {
			found = true
		}
	}
	assert.True(t, found, "must reach the dynamic_url ledger")
}

func TestX1a_RestyLiteralURL_StillMatchesNormally(t *testing.T) {
	// Regression: the literal fast path (resty_get) must be unaffected by
	// the new dynamic-gated variants added alongside it.
	src := `
package client

func fetchUsers(c *resty.Client) {
	c.Get("/api/v1/users")
}
`
	results := matchWithGrammar(t, "go", "go", "client.go", src)
	nodes, _, _ := patterns.MatchToGraph("svc-c-mgr", results)

	var client *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeHTTPClient {
			client = &nodes[i]
		}
	}
	require.NotNil(t, client)
	// http_get and resty_get both structurally match a bare `.Get("literal")`
	// call regardless of receiver type — a pre-existing, unrelated dedup
	// ambiguity (file-glob order decides the winner: http_get uses meta
	// "url", resty_get uses "path"). X.1a doesn't change which pattern
	// wins; it only asserts the literal fast path still resolves to a
	// clean, non-dynamic value either way.
	key := client.Meta["path"]
	if key == "" {
		key = client.Meta["url"]
	}
	assert.Equal(t, "/api/v1/users", key)
	assert.Empty(t, client.Meta["key_dynamic"])
}

// TestX1a_Determinism_TwoRuns (bug-class #2) runs the JSX ternary and Go
// resty-sprintf pipelines twice each and requires byte-identical edge/ledger
// output — the WalkKey routing must never depend on map iteration order.
func TestX1a_Determinism_TwoRuns(t *testing.T) {
	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)

	runNav := func() []graph.Edge {
		src := `
const Nav = ({ isAdmin }) => (
  <a href={isAdmin ? "/admin" : "/dashboard"}>Go</a>
);
`
		results := matchWithGrammar(t, "jsx", "tsx", "nav.tsx", src)
		nodes, _, _ := patterns.MatchToGraph("svc-a", results)
		handlers := []graph.Node{
			{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-a",
				Meta: map[string]string{"method": "GET", "path": "/admin"}},
			{ID: "h2", Type: graph.NodeTypeHTTPHandler, Service: "svc-a",
				Meta: map[string]string{"method": "GET", "path": "/dashboard"}},
		}
		enriched, _ := contract.EnrichAliases(append(nodes, handlers...))
		eng := &contract.Engine{}
		return eng.Link(enriched, rules, nil).Edges
	}

	runResty := func() []graph.UnresolvedRef {
		src := `
package client

func fetchBuild(cfg Config, buildID string) {
	resty.New().R().Get(fmt.Sprintf("%s/api/v1/builds/%s", cfg.AgentURL, buildID))
}
`
		results := matchWithGrammar(t, "go", "go", "client.go", src)
		nodes, _, _ := patterns.MatchToGraph("svc-c-mgr", results)
		enriched, _ := contract.EnrichAliases(nodes)
		eng := &contract.Engine{}
		return eng.Link(enriched, rules, nil).Unresolved
	}

	navA, navB := runNav(), runNav()
	require.Equal(t, len(navA), len(navB))
	for i := range navA {
		assert.Equal(t, navA[i], navB[i], "edge %d diverged across runs", i)
	}

	restyA, restyB := runResty(), runResty()
	require.Equal(t, len(restyA), len(restyB))
	for i := range restyA {
		assert.Equal(t, restyA[i], restyB[i], "unresolved entry %d diverged across runs", i)
	}
}
