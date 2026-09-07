package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/stretchr/testify/require"
)

// parseJSFull runs the JavaScriptParser over inline source and returns every
// output channel (nodes, edges, unresolved refs).
func parseJSFull(t *testing.T, name, src string) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))
	reg, err := patterns.DefaultRegistry(rbPatternsDir)
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	p := &JavaScriptParser{}
	nodes, edges, unresolved, err := p.Parse(file, "svc", m, nil)
	require.NoError(t, err)
	return nodes, edges, unresolved
}

// TestJSJQueryDollarNotACallTarget (JCM.5): `component_fn_call`'s `^[a-z_$]`
// query captures every `$(...)` site. A same-file `const $ = require("jquery")`
// binding then gives the callee a `variable:$` node to (wrongly) resolve to,
// flooding files like ActionCreators.jsx with `calls` edges into test-file `$`
// vars. The jQuery/Zepto global must never be treated as a call target unless
// `$` resolves to a real same-file function.
func TestJSJQueryDollarNotACallTarget(t *testing.T) {
	t.Parallel()
	nodes, edges, unresolved := parseJSFull(t, "ActionCreators.js", `
const $ = require("jquery");
function setSelection(id) {
  $(".row-" + id).addClass("selected");
  $$(".other").hide();
}
`)

	for _, u := range unresolved {
		if u.Kind == "call_ref" && (u.Name == "$" || u.Name == "$$") {
			t.Errorf("jQuery %q must not be ledgered as an unresolved call_ref: %+v", u.Name, u)
		}
	}

	dollarIDs := map[string]bool{}
	for _, n := range nodes {
		if n.Label == "$" || n.Label == "$$" {
			dollarIDs[n.ID] = true
		}
	}
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && dollarIDs[e.To] {
			t.Errorf("no calls edge may target the jQuery global: %+v", e)
		}
	}
}
