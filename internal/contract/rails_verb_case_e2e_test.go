package contract_test

// Tier HH.2 end-to-end guard: real routes.rb through parser -> matcher ->
// contract engine. Hand-built nodes cannot prove this one, because the defect
// was in what the *parser* stamped, and its damage only became visible at the
// engine's exact tier.
//
// findMatches tries tiers in order and the first tier that hits wins, and the
// exact tier indexes on the *raw* key — before case folding. A Rails route that
// kept Ruby's lowercase verb was keyed "post /login", so it missed the exact
// tier entirely while a Gin route elsewhere in the fleet keyed "POST /login"
// hit it. The wrong service won at the highest confidence tier and the
// normalized tier, where the correct route would have matched, never ran.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

func parseRailsVerbCaseRoutes(t *testing.T) []graph.Node {
	t.Helper()
	reg, err := patterns.EmbeddedRegistry()
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	rp := &parser.RubyParser{}
	nodes, _, _, err := rp.Parse("testdata/rails_verb_case/routes.rb", "cam", m)
	require.NoError(t, err)
	return nodes
}

// TestRailsRouteKeysAreUpperCased pins the join key the engine actually indexes
// on: a bare-string Rails route must reach the graph as "POST /login", not
// "post login".
func TestRailsRouteKeysAreUpperCased(t *testing.T) {
	keys := map[string]bool{}
	for _, n := range parseRailsVerbCaseRoutes(t) {
		if n.Type == graph.NodeTypeHTTPHandler && n.Meta["pattern"] == "http_verb_route" {
			keys[n.Meta["method"]+" "+n.Meta["path"]] = true
		}
	}
	assert.True(t, keys["POST /login"], "got keys %v", keys)
	assert.True(t, keys["GET /client_api/v1/user_category_rules/:id"], "got keys %v", keys)
}

// TestExactTierNotShadowedByVerbCase is the regression guard for the shadowing
// bug: a same-service client calling POST /login must reach its own service's
// Rails route. Before HH.2 the CAM route was keyed "post /login", so the *only*
// reachable candidate was the cross-service Gin handler — one edge, wrong
// service, at `static`, the tier evidence fusion trusts most.
//
// The engine emits an edge per eligible hit by design (recall first), so the
// cross-service candidate does not disappear; what changes is that it is no
// longer alone and no longer authoritative. Two services now answer the key, so
// the fan-out guard downgrades both edges to `partial`, which evidence fusion
// will not promote to `verified` on spec alone.
func TestExactTierNotShadowedByVerbCase(t *testing.T) {
	nodes := parseRailsVerbCaseRoutes(t)

	var camLogin string
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPHandler && n.Meta["path"] == "/login" {
			camLogin = n.ID
		}
	}
	require.NotEmpty(t, camLogin, "fixture must yield a CAM /login handler")

	nodes = append(nodes,
		// The competing handler: a Gin route in another service whose verb was
		// already upper-case, so it always reached the exact tier.
		graph.Node{ID: "gin-login", Type: graph.NodeTypeHTTPHandler, Service: "mysycamore",
			Label: "POST /login", File: "main.go",
			Meta: map[string]string{"method": "POST", "path": "/login"}},
		graph.Node{ID: "cam-client", Type: graph.NodeTypeHTTPClient, Service: "cam",
			Label: "POST /login", File: "src/services/login.service.tsx",
			Meta: map[string]string{"method": "POST", "path": "/login"}},
	)

	res := runHTTP(t, nodes, nil)

	targets := map[string]string{}
	for _, e := range res.Edges {
		if e.From == "cam-client" {
			targets[e.To] = e.Confidence
		}
	}
	require.Contains(t, targets, camLogin,
		"client must reach its own service's route; got %v", targets)
	assert.Equal(t, graph.ConfidencePartial, targets[camLogin],
		"two services answer this key, so neither edge may claim static")
	if conf, ok := targets["gin-login"]; ok {
		assert.Equal(t, graph.ConfidencePartial, conf,
			"the cross-service candidate must lose its unchallenged static edge")
	}
}
