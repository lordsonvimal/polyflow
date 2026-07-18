package linker_test

// L.W2 acceptance-chain seam test through REAL parses (bug-class rule 6 —
// hand-built nodes are insufficient): an ERB view's element (id="save-btn")
// must enter the shared element-definition index via the ERB parser's HTML
// pass, and a jQuery selector in a separate JS file must link to it with a
// defined_in edge. This closes the "ERB element source omitted (L.W0
// pending)" deviation recorded in the L.W2 outcome note.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

func TestERBElementLinksToJQuerySelector_RealParse(t *testing.T) {
	reg, err := patterns.DefaultRegistry("../../patterns")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	var nodes []graph.Node
	for _, f := range []string{
		"testdata/erb_jquery/form.html.erb",
		"testdata/erb_jquery/app.js",
	} {
		p := parser.ForFile(f)
		require.NotNil(t, p, "no parser for %s", f)
		ns, _, _, err := p.Parse(f, "app", m)
		require.NoError(t, err)
		nodes = append(nodes, ns...)
	}

	// The ERB HTML pass must have minted an element node for #save-btn.
	var erbElement *graph.Node
	var jqSelector *graph.Node
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeElement && n.Meta["id"] == "save-btn" {
			erbElement = n
		}
		if n.Type == graph.NodeTypeDOMTarget && n.File == "testdata/erb_jquery/app.js" {
			jqSelector = n
		}
	}
	require.NotNil(t, erbElement, "ERB view element #save-btn missing from element index; nodes: %+v", nodes)
	require.NotNil(t, jqSelector, "jQuery selector dom_target missing")

	_, edges, _ := linker.LinkDOMDefinitions(nodes)
	var found bool
	for _, e := range edges {
		if e.To == erbElement.ID && e.From == jqSelector.ID {
			found = true
			assert.Equal(t, graph.EdgeType("defined_in"), e.Type)
		}
	}
	assert.True(t, found, "expected defined_in edge %s -> %s; got %+v", jqSelector.ID, erbElement.ID, edges)
}
