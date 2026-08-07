package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/require"
)

func parseStylesheet(t *testing.T, name, src string) ([]graph.Node, []graph.UnresolvedRef) {
	t.Helper()
	file := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))
	p := &StylesheetParser{}
	nodes, edges, unresolved, err := p.Parse(file, "nextGen", nil)
	require.NoError(t, err)
	require.Empty(t, edges, "stylesheet edges are the linker's job (cross-file)")
	return nodes, unresolved
}

// TestStylesheetParser_SelectorNodes: each top-level class/id selector becomes
// one element node — the join target Tier K.4 needs for `$(\".btn\")`.
func TestStylesheetParser_SelectorNodes(t *testing.T) {
	nodes, unresolved := parseStylesheet(t, "issues.scss", `
.resolve-issue-btn {
  content: "\f046";
  font-family: "FontAwesome";
}
.resolve-issue-btn:after { color: red; }
#issue-panel .inner { color: red; }
`)
	require.Empty(t, unresolved)

	// `.resolve-issue-btn` and its `:after` variant are one join target, and
	// `.inner` is the subject of the third rule, not `#issue-panel`.
	require.Len(t, nodes, 2)
	require.Equal(t, ".resolve-issue-btn", nodes[0].Label)
	require.Equal(t, graph.NodeTypeElement, nodes[0].Type)
	require.Equal(t, "class", nodes[0].Meta["selector_kind"])
	require.Equal(t, "stylesheet_selector", nodes[0].Meta["pattern"])
	require.Equal(t, "scss", nodes[0].Language)
	require.Equal(t, "nextGen", nodes[0].Service)
	require.Equal(t, 2, nodes[0].Line)
	require.Equal(t, ".inner", nodes[1].Label)
}

// TestStylesheetParser_NoNoiseNodes pins the noise discipline that the templ
// `variable` regression (commit d07e911) established: SCSS variables, mixins,
// functions, declarations and nested rules mint nothing. 145 stylesheets can
// otherwise bury real search results.
func TestStylesheetParser_NoNoiseNodes(t *testing.T) {
	nodes, _ := parseStylesheet(t, "settings.scss", `
$brand-color: #0055aa;
$spacing-unit: 4px;
@mixin clearfix { &:after { content: ""; } }
@function rem($px) { @return $px / 16 * 1rem; }
.only-node {
  color: $brand-color;
  .nested-noise { color: red; }
}
`)
	require.Len(t, nodes, 1)
	require.Equal(t, ".only-node", nodes[0].Label)
}

// TestStylesheetParser_FontFaceSources: a remote src becomes an external
// service, a local one an asset file node, and an interpolated one is ledgered
// rather than guessed (phases.md #12) — the last is the only shape that
// actually occurs in nextGen, via utilities/mixins.scss.
func TestStylesheetParser_FontFaceSources(t *testing.T) {
	nodes, unresolved := parseStylesheet(t, "fonts.scss", `
@font-face {
  font-family: "DejaVu Sans";
  src: url("../fonts/dejavu.woff2") format("woff2"),
       url("https://cdn.example.com/dejavu.woff") format("woff");
}
@mixin font-face($font-family, $file-path) {
  @font-face {
    font-family: $font-family;
    src: font-url("#{$file-path}.eot");
  }
}
`)
	require.Len(t, nodes, 2)

	require.Equal(t, graph.NodeTypeFile, nodes[0].Type)
	require.Equal(t, "local", nodes[0].Meta["scope"])
	require.Equal(t, "DejaVu Sans", nodes[0].Meta["font_family"])
	require.True(t, filepath.IsAbs(nodes[0].File))
	require.Equal(t, "fonts/dejavu.woff2", filepath.Base(filepath.Dir(nodes[0].File))+"/"+filepath.Base(nodes[0].File))

	require.Equal(t, graph.NodeTypeExternalService, nodes[1].Type)
	require.Equal(t, "remote", nodes[1].Meta["scope"])
	require.Equal(t, "https://cdn.example.com/dejavu.woff", nodes[1].Meta["url"])

	require.Len(t, unresolved, 1)
	require.Equal(t, "stylesheet_font_dynamic", unresolved[0].Kind)
	require.Equal(t, "#{$file-path}.eot", unresolved[0].Name)
}

// TestStylesheetParser_PlainCSS: `.css` is parsed by the same reader and tagged
// distinctly, so a search can scope to one or the other.
func TestStylesheetParser_PlainCSS(t *testing.T) {
	nodes, _ := parseStylesheet(t, "brand.css", `.brand-header { color: red; }`)
	require.Len(t, nodes, 1)
	require.Equal(t, "css", nodes[0].Language)
}

// TestStylesheetParser_Deterministic: node IDs and order are stable across runs
// (bug-class #2).
func TestStylesheetParser_Deterministic(t *testing.T) {
	src := `.a { color: red; } #b, .c { color: blue; }`
	dir := t.TempDir()
	file := filepath.Join(dir, "d.scss")
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))
	p := &StylesheetParser{}
	first, _, _, err := p.Parse(file, "svc", nil)
	require.NoError(t, err)
	second, _, _, err := p.Parse(file, "svc", nil)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Len(t, first, 3)
}

// TestStylesheetParser_Registered: `.scss`/`.css` must reach this parser through
// the registry, which is also what removes them from the indexer's unparsed
// blind-spot count.
func TestStylesheetParser_Registered(t *testing.T) {
	for _, ext := range []string{"a.scss", "a.css"} {
		require.NotNil(t, ForFile(ext), "no parser registered for %s", ext)
	}
	require.NotNil(t, ForFile("a.es6"), "no parser registered for .es6")
	require.Equal(t, "javascript", ForFile("a.es6").Language())
}
