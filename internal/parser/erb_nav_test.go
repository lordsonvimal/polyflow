package parser_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseERBNav parses an ERB template and returns its nav producer nodes.
func parseERBNav(t *testing.T, src string) []graph.Node {
	t.Helper()
	m := mustMatcher(t)
	file := filepath.Join(t.TempDir(), "index.html.erb")
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))

	p := parser.ForFile(file)
	require.NotNil(t, p)
	nodes, _, _, err := p.Parse(file, "svc", m, nil)
	require.NoError(t, err)

	var out []graph.Node
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			out = append(out, n)
		}
	}
	return out
}

// TestERBNav_ParenthesisedLinkToReadsTheSecondArgument is Tier RT.2.
//
// `link_to("Create Org Admin", widget_path(id: 1))` produced a node with
// method="Create" and path="Org Admin": the query's leading `_` wildcard bound
// the `(` token, which shifted every argument one place left and made the link
// *text* the destination — then the http_path capture split it on whitespace
// into a verb and a path. The node was not merely mislabelled; it described a
// request to a URL that does not exist.
//
// The unparenthesised form is asserted alongside it in every case here. It is
// the form that always worked and carries the traffic, so it is the one a fix
// to the parenthesised form can quietly break.
func TestERBNav_ParenthesisedLinkToReadsTheSecondArgument(t *testing.T) {
	t.Parallel()
	got := parseERBNav(t, `
<span><%= link_to("Create Org Admin", widget_path(organization_id: 1)) %></span>
<span><%= link_to "Create Org Admin", widget_path(organization_id: 1) %></span>
`)

	require.Len(t, got, 2, "one nav producer per link_to; got %v", navSummaries(got))
	for _, n := range got {
		assert.Equal(t, "widget_path", n.Meta["helper"], "destination is the second argument")
		assert.Equal(t, "widget_path", n.Label)
		assert.Equal(t, "nav_link_rails_helper", n.Meta["pattern"])
		assert.NotEqual(t, "Create", n.Meta["method"],
			"link text read as an HTTP method")
		assert.NotEqual(t, "Org Admin", n.Meta["path"],
			"link text read as a path")
	}
}

// TestERBNav_ParenthesisedLiteralPath — the same shift, with a literal
// destination rather than a helper.
func TestERBNav_ParenthesisedLiteralPath(t *testing.T) {
	t.Parallel()
	// Distinct paths: two literal nav producers for the same path in one file
	// share a node ID and collapse into one, which would hide a miss.
	got := parseERBNav(t, `
<%= link_to("View widget", "/widgets/1") %>
<%= link_to "Edit widget", "/widgets/2" %>
`)

	require.Len(t, got, 2, "got %v", navSummaries(got))
	paths := map[string]bool{}
	for _, n := range got {
		paths[n.Meta["path"]] = true
		assert.Equal(t, "nav_link_rails_literal", n.Meta["pattern"])
	}
	assert.Equal(t, map[string]bool{"/widgets/1": true, "/widgets/2": true}, paths)
}

// TestERBNav_SinglePositionalArgumentIsTheDestination. Rails lets the URL stand
// in for the link text, and the block form (`link_to(path, opts) do … end`)
// makes that the *common* shape in a template rather than a curiosity. These
// resolved before RT.2 only by the accident the tier fixes, so an anchored
// first-argument query has to take them over deliberately.
func TestERBNav_SinglePositionalArgumentIsTheDestination(t *testing.T) {
	t.Parallel()
	got := parseERBNav(t, `
<%= link_to(help_index_path, id: "help") do %>Help<% end %>
<%= link_to(widget_path(id: 1), method: :delete) do %>Delete<% end %>
<%= link_to widget_index_path do %>All<% end %>
`)

	helpers := map[string]bool{}
	for _, n := range got {
		helpers[n.Meta["helper"]] = true
	}
	for _, want := range []string{"help_index_path", "widget_path", "widget_index_path"} {
		assert.True(t, helpers[want], "no nav producer for %s; got %v", want, navSummaries(got))
	}
}

// TestERBNav_AnchorLinkIsNotANavProducer. `link_to("#", data: { role: … }) do`
// is a button wired up by JavaScript, not a navigation: the `#` names no route
// and no request, so a nav producer for it is an orphan http_client describing
// a page transition that never happens — the artifact the C.2 helper gate
// removed 28 of on the fleet.
//
// The two-argument `link_to text, "#"` still produces one. That behaviour
// predates RT.2 and moving it is a separate decision; what RT.2 declines to do
// is mint eleven more of them on the way past.
func TestERBNav_AnchorLinkIsNotANavProducer(t *testing.T) {
	t.Parallel()
	got := parseERBNav(t, `
<%= link_to("#", class: "btn") do %>Audit<% end %>
<%= link_to "#" do %>Audit<% end %>
`)

	assert.Empty(t, got, "an in-page anchor became a navigation: %v", navSummaries(got))
}

// TestERBNav_LinkTextAloneIsNotADestination is the gate on the anchored
// first-argument query: `link_to "Home"` names no URL at all, and a query that
// reads the first argument unconditionally would mint an http_client for a
// request to "Home".
func TestERBNav_LinkTextAloneIsNotADestination(t *testing.T) {
	t.Parallel()
	got := parseERBNav(t, `
<%= link_to "Home" %>
<%= link_to("Home") %>
`)

	assert.Empty(t, got, "link text with no destination produced %v", navSummaries(got))
}

func navSummaries(nodes []graph.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Meta["pattern"]+"{helper="+n.Meta["helper"]+
			",path="+n.Meta["path"]+",method="+n.Meta["method"]+"}")
	}
	return out
}
