package contract_test

// X.1a end-to-end regression tests: real parser → matcher → MatchToGraph →
// EnrichAliases → contract.Engine.Link, proving the live WalkKey wiring
// fires on actual source (bug-class #6 — hand-built nodes alone are
// insufficient evidence). Covers the two worked examples from
// docs/systemic-gaps-plan.md Phase X.1a:
//   - a JSX ternary href still fans out via key_candidates after
//     nav_links.yaml's rewrite from the JSX-only @branch_N convention to the
//     general WalkKey routing (no regression);
//   - a Go resty.New().R().Get(fmt.Sprintf(...)) call — previously silently
//     producing zero nodes (resty_get's literal-quote gate) — now reaches
//     the dynamic_url ledger instead of vanishing. Template reconstruction
//     (turning this into a resolved edge) is X.1b; this test pins the X.1a
//     baseline: recognized-and-ledgered, not yet resolved.

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

func TestX1a_RestySprintfURL_DirectChain_ReachesDynamicLedger(t *testing.T) {
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
	assert.Equal(t, "true", client.Meta["key_dynamic"], "pre-X.1b: recognized as dynamic, not yet template-reconstructed")
	assert.Empty(t, client.Meta["url"])

	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	enriched, ledger := contract.EnrichAliases(nodes)
	require.Empty(t, ledger)

	eng := &contract.Engine{}
	result := eng.Link(enriched, rules, nil)

	for _, e := range result.Edges {
		assert.NotEqual(t, client.ID, e.From, "a dynamic key must never fabricate an edge")
	}
	var found bool
	for _, u := range result.Unresolved {
		if u.Kind == "dynamic_url" {
			found = true
		}
	}
	assert.True(t, found, "must reach the dynamic_url ledger, not vanish silently")
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
