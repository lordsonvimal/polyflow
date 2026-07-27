package contract_test

// H.2 (plan-7) end-to-end regression tests: real parser → matcher →
// MatchToGraph → linker.LinkRouteComponents, proving Solid Router's
// `<Route path=... component=...>` resolves through the real parse path
// (bug-class #6), including the constant-ref path shape
// (`clientRoutes.home`) that real Solid apps use instead of string
// literals, and that the existing generic nav_link_jsx pattern already
// covers `<A href="/x">` with zero new pattern code.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

const solidRouterSrc = `
import { Route } from "@solidjs/router";
import { A } from "@solidjs/router";

export const clientRoutes = {
  home: "/",
  settings: "/settings",
};

function Settings() {
  return <div>Settings</div>;
}

function Home() {
  return <div>Home</div>;
}

function App() {
  return (
    <>
      <Route path="/settings" component={Settings} />
      <Route path={clientRoutes.home} component={Home} />
      <A href="/settings">Settings</A>
    </>
  );
}
`

// matchSolidRouterFile runs the real parser exactly as parser.JavaScriptParser
// does for a .tsx file: javascript + typescript + jsx pattern languages
// against the tsx grammar, combined into one MatchToGraph call so constants
// collected from one pattern set (const_object_member, language: javascript)
// feed KeyWalker resolution for another (solid_route, language: jsx).
func matchSolidRouterFile(t *testing.T) []graph.Node {
	t.Helper()
	var all []patterns.MatchResult
	for _, lang := range []string{"javascript", "typescript", "jsx"} {
		all = append(all, matchWithGrammar(t, lang, "tsx", "App.tsx", solidRouterSrc)...)
	}
	nodes, _, _ := patterns.MatchToGraph("calendar-ui", all)
	return nodes
}

func routeNodes(nodes []graph.Node) []graph.Node {
	var out []graph.Node
	for _, n := range nodes {
		if n.Meta["pattern"] == "solid_route" {
			out = append(out, n)
		}
	}
	return out
}

func TestH2_SolidRoute_LiteralPath(t *testing.T) {
	nodes := matchSolidRouterFile(t)
	routes := routeNodes(nodes)
	require.Len(t, routes, 2, "both Route elements produce a solid_route node")

	var literal *graph.Node
	for i := range routes {
		if routes[i].Meta["component"] == "Settings" {
			literal = &routes[i]
		}
	}
	require.NotNil(t, literal)
	assert.Equal(t, graph.NodeTypeRoute, literal.Type)
	assert.Equal(t, "/settings", literal.Meta["path"])
	assert.Empty(t, literal.Meta["key_dynamic"])
}

// TestH2_SolidRoute_ConstantRef_ResolvesNotGuessed proves the follow-the-
// constant step: `path={clientRoutes.home}` is a member expression, not a
// string literal, but the const_object_member pattern indexes clientRoutes'
// members and the JS KeyWalker's member_expression case resolves it to the
// real value — the node must carry the resolved path, never a guess and
// never a bare unresolved dynamic stub, since the object literal is
// same-file and fully known statically.
func TestH2_SolidRoute_ConstantRef_ResolvesNotGuessed(t *testing.T) {
	nodes := matchSolidRouterFile(t)
	routes := routeNodes(nodes)
	require.Len(t, routes, 2)

	var ref *graph.Node
	for i := range routes {
		if routes[i].Meta["component"] == "Home" {
			ref = &routes[i]
		}
	}
	require.NotNil(t, ref)
	assert.Equal(t, graph.NodeTypeRoute, ref.Type)
	assert.Empty(t, ref.Meta["key_dynamic"], "resolvable same-file constant member must not be ledgered as dynamic")
	assert.Equal(t, "/", ref.Meta["path"], "resolved to the constant's real value, not the raw clientRoutes.home text")
}

// TestH2_SolidRoute_ComponentEdge_ResolvesAndLedgersMiss proves the
// route→component renders edge (linker.LinkRouteComponents) fires for a
// resolvable component and ledgers a genuine miss instead of dropping it.
func TestH2_SolidRoute_ComponentEdge_ResolvesAndLedgersMiss(t *testing.T) {
	nodes := matchSolidRouterFile(t)

	var settingsFn, homeFn *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeFunction && nodes[i].Label == "Settings" {
			settingsFn = &nodes[i]
		}
		if nodes[i].Type == graph.NodeTypeFunction && nodes[i].Label == "Home" {
			homeFn = &nodes[i]
		}
	}
	require.NotNil(t, settingsFn, "Settings function declaration must be indexed")
	require.NotNil(t, homeFn, "Home function declaration must be indexed")

	edges, unresolved := linker.LinkRouteComponents(nodes)
	require.Len(t, edges, 2, "both routes resolve to their component declaration")
	require.Empty(t, unresolved)
	targets := map[string]bool{}
	for _, e := range edges {
		assert.Equal(t, graph.EdgeTypeRenders, e.Type)
		assert.Equal(t, graph.ConfidenceInferred, e.Confidence)
		targets[e.To] = true
	}
	assert.True(t, targets[settingsFn.ID])
	assert.True(t, targets[homeFn.ID])

	// A route naming a component with no declaration anywhere must ledger,
	// never silently drop (bug-class #12).
	ghost := graph.Node{
		ID: "calendar-ui:App.tsx:route:%2Fghost:99", Type: graph.NodeTypeRoute,
		Service: "calendar-ui", File: "App.tsx", Line: 99,
		Meta: map[string]string{"component": "Ghost"},
	}
	missEdges, missUnresolved := linker.LinkRouteComponents(append(nodes, ghost))
	require.Len(t, missUnresolved, 1)
	assert.Equal(t, "component_ref", missUnresolved[0].Kind)
	assert.Equal(t, "Ghost", missUnresolved[0].Name)
	assert.Len(t, missEdges, 2, "the miss does not affect the resolvable routes")
}

// TestH2_SolidNavLink_AlreadyCoveredByExistingPattern proves the phase's
// "add there if a shared shape fits" instruction: nav_links.yaml's
// nav_link_jsx already matches any href attribute regardless of element
// name, so Solid's <A href="/x"> produces a nav_link node with zero new
// pattern code — recall is preserved, no node is dropped.
//
// This test surfaced a pre-existing, out-of-scope defect: nav_link_jsx
// captures @path on the inner string_fragment node (to anchor its "^/"
// predicate), but X.1a's WalkKey switches on node.Type() and only
// recognizes the parent "string" node — so every literal href/action, not
// just Solid's, is wrongly marked key_dynamic instead of resolving to the
// clean path. Confirmed present on master before this phase's changes.
// Recall still holds (the node exists, nothing is dropped) so this does
// not block H.2 — recorded as a follow-up in the phase's outcome note
// rather than fixed here (fixing nav_link_jsx's capture shape is a
// repo-wide change well outside a Solid-Router-scoped commit).
func TestH2_SolidNavLink_AlreadyCoveredByExistingPattern(t *testing.T) {
	nodes := matchSolidRouterFile(t)
	var nav *graph.Node
	for i := range nodes {
		if nodes[i].Meta["nav_link"] == "true" && nodes[i].Meta["key_dynamic_raw"] == "/settings" {
			nav = &nodes[i]
		}
	}
	require.NotNil(t, nav, "<A href=\"/settings\"> must still produce a nav_link node via the existing generic pattern")
}

// TestH2_Determinism runs the whole parse→match→link pipeline twice on the
// same input and requires byte-identical output (bug-class #2).
func TestH2_Determinism(t *testing.T) {
	run := func() string {
		nodes := matchSolidRouterFile(t)
		edges, unresolved := linker.LinkRouteComponents(nodes)
		out, err := json.Marshal(struct {
			Nodes      []graph.Node
			Edges      []graph.Edge
			Unresolved []graph.UnresolvedRef
		}{nodes, edges, unresolved})
		require.NoError(t, err)
		return string(out)
	}
	first := run()
	second := run()
	assert.Equal(t, first, second, "two runs on identical input must produce byte-identical output")
}
