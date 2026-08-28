package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const shellPatternsDir = "../../patterns"

// parseShellSrc runs the ShellParser on inline source written to a temp file
// — the real parser→matcher path (rule 6), not hand-built nodes.
func parseShellSrc(t *testing.T, filename, src string) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, filename)
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))
	reg, err := patterns.DefaultRegistry(shellPatternsDir)
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	p := &ShellParser{}
	nodes, edges, unresolved, err := p.Parse(file, "svc", m, nil)
	require.NoError(t, err)
	return nodes, edges, unresolved
}

func shNode(nodes []graph.Node, typ graph.NodeType, label string) *graph.Node {
	for i := range nodes {
		if nodes[i].Type == typ && nodes[i].Label == label {
			return &nodes[i]
		}
	}
	return nil
}

// TestShell_FunctionsAndCall verifies the SH0 acceptance criterion: a shell
// script with two functions and a top-level call indexes to function nodes
// plus one calls edge, with an honest unresolved count for anything
// unresolved (the top-level `build` call and the bare `echo`/`greet`/
// `hello` invocations that don't resolve to a declared function).
func TestShell_FunctionsAndCall(t *testing.T) {
	t.Parallel()
	src := `function greet {
  echo "hello"
}

build() {
  greet
}

build
`
	nodes, edges, unresolved := parseShellSrc(t, "deploy.sh", src)

	greet := shNode(nodes, graph.NodeTypeFunction, "greet")
	build := shNode(nodes, graph.NodeTypeFunction, "build")
	require.NotNil(t, greet, "greet function missing")
	require.NotNil(t, build, "build function missing")
	assert.Equal(t, "bash", greet.Language)

	// build() calls greet() — a same-file resolved call.
	var found bool
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.From == build.ID && e.To == greet.ID {
			found = true
		}
	}
	assert.True(t, found, "expected build -> greet calls edge")

	// echo (inside greet) and the top-level `build` call are unresolved
	// call_ref entries (echo is an external program; the top-level `build`
	// bare-word call resolves via the SAME mechanism actually — but is
	// asserted separately below since MatchToGraph resolves it too).
	var callRefKinds int
	for _, u := range unresolved {
		if u.Kind == "call_ref" {
			callRefKinds++
		}
	}
	assert.GreaterOrEqual(t, callRefKinds, 1, "expected at least one unresolved call_ref (echo)")
}

// TestShell_TopLevelCallAttributesToScriptScope verifies SH0's synthetic
// (script) fallback: a bare top-level call to a declared function (outside
// any function body) attributes to the per-file (script) node, mirroring the
// JS/Python (module) precedent.
func TestShell_TopLevelCallAttributesToScriptScope(t *testing.T) {
	t.Parallel()
	src := `setup() {
  echo "setting up"
}

setup
`
	nodes, edges, _ := parseShellSrc(t, "run.sh", src)

	script := shNode(nodes, graph.NodeTypeFunction, "(script)")
	require.NotNil(t, script, "(script) scope node missing")
	assert.Equal(t, "script", script.Meta["scope"])

	setup := shNode(nodes, graph.NodeTypeFunction, "setup")
	require.NotNil(t, setup)

	var found bool
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.From == script.ID && e.To == setup.ID {
			found = true
		}
	}
	assert.True(t, found, "expected (script) -> setup calls edge for the top-level call")
}

// TestShell_ScriptScopeAlwaysPresent verifies the (script) node is minted
// even for a function-only file with no top-level statement to lazily
// trigger it — SH1's cross-file `exec` edges need a stable landing node
// regardless of the target script's content.
func TestShell_ScriptScopeAlwaysPresent(t *testing.T) {
	t.Parallel()
	src := `migrate_up() {
  echo "migrating"
}
`
	nodes, _, _ := parseShellSrc(t, "migrate.sh", src)
	script := shNode(nodes, graph.NodeTypeFunction, "(script)")
	require.NotNil(t, script, "(script) scope node must always be minted for a shell file")
}

// TestShell_BatsFixtureIndexesWithNoSpecialCasing proves a .bats file
// (Bats-core test framework) parses as ordinary bash-dialect content: the
// `@test "..." { ... }` blocks are NOT recognized as function definitions
// (real bash — and therefore this grammar — does not treat them as such;
// Bats itself preprocesses them before execution), but an ordinary function
// declared in the same file still indexes normally, and no exclude/panic
// occurs.
func TestShell_BatsFixtureIndexesWithNoSpecialCasing(t *testing.T) {
	t.Parallel()
	src := `#!/usr/bin/env bats

helper_func() {
  echo "helper"
}

@test "calls helper" {
  run helper_func
  [ "$status" -eq 0 ]
}
`
	nodes, _, unresolved := parseShellSrc(t, "deploy.bats", src)
	helper := shNode(nodes, graph.NodeTypeFunction, "helper_func")
	require.NotNil(t, helper, "helper_func must index from a .bats file like any .sh file")
	// The unresolved ledger should be non-nil (bare words like @test/run/[
	// don't resolve to a declared function) — proves exhaustive accounting,
	// not a silent drop or a crash.
	assert.NotEmpty(t, unresolved)
}

// TestShell_HeredocAndQuoteStrip runs a real fixture with a single-quoted
// string, a double-quoted string, an ANSI-C string, and a heredoc through
// the real parser→matcher path (rule 6/11): parsing must not error, and the
// function declared after the heredoc must still be recognized (proving the
// heredoc body doesn't corrupt subsequent parsing).
func TestShell_HeredocAndQuoteStrip(t *testing.T) {
	t.Parallel()
	src := `cat <<EOF
some heredoc body with $VAR
EOF

cat <<-'NOSUB'
literal $VAR body
NOSUB

echo 'single quoted'
echo "double quoted"
echo $'ansi c quoted'

after_heredoc() {
  echo "still parses"
}
`
	nodes, _, _ := parseShellSrc(t, "with_heredoc.sh", src)
	fn := shNode(nodes, graph.NodeTypeFunction, "after_heredoc")
	require.NotNil(t, fn, "function after a heredoc block must still be recognized")
}

// TestStripStringLiteral_BashForms unit-tests the SH0 additions to
// stripStringLiteral directly: ANSI-C quoting and the three heredoc forms.
func TestStripStringLiteral_BashForms(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"ansi_c", `$'hello world'`, "hello world"},
		{"heredoc_plain", "<<EOF\nline one\nline two\nEOF", "line one\nline two"},
		{"heredoc_dash", "<<-EOF\nindented\nEOF", "indented"},
		{"heredoc_nosub_single", "<<'EOF'\nliteral $VAR\nEOF", "literal $VAR"},
		{"heredoc_nosub_double", `<<"EOF"` + "\nliteral $VAR\nEOF", "literal $VAR"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, patterns.StripStringLiteral(tc.in))
		})
	}
}
