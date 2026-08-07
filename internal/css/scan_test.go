package css

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func specs(imps []Import) []string {
	out := make([]string, 0, len(imps))
	for _, i := range imps {
		out = append(out, i.Spec)
	}
	return out
}

func selectorTexts(sels []Selector) []string {
	out := make([]string, 0, len(sels))
	for _, s := range sels {
		out = append(out, s.Text())
	}
	return out
}

// TestScan_ImportsAndRules is the worked example: orion's
// app/assets/stylesheets/application.scss mixes quoted partial imports, an
// explicit `.css` extension, a relative node_modules path and glob imports.
func TestScan_ImportsAndRules(t *testing.T) {
	res := Scan([]byte(`
@import "vendor/bourbon.scss";
@import "style-components.css";
@import "../../../node_modules/react-tabs/style/react-tabs.css";
@import "settings/colors";
@import "modules/*";
@use "sass:math";

.study-list {
  color: red;
}
`))
	require.Equal(t, []string{
		"vendor/bourbon.scss",
		"style-components.css",
		"../../../node_modules/react-tabs/style/react-tabs.css",
		"settings/colors",
		"modules/*",
		"sass:math",
	}, specs(res.Imports))
	require.Equal(t, "import", res.Imports[0].Rule)
	require.Equal(t, "use", res.Imports[5].Rule)
	require.Equal(t, 2, res.Imports[0].Line)
	require.Equal(t, []string{".study-list"}, selectorTexts(res.Selectors))
}

// TestScan_CommentsAreNotSource guards the single largest false-positive risk
// in this corpus: bourbon's vendored `library/font-face.scss` documents the
// mixin with `///` comments that contain a literal `@font-face { ... url(...) }`
// example, and mixins.scss opens with a `/* Fool-proof @font-face */` banner.
// Reading either as source invents a font node and a stray selector.
func TestScan_CommentsAreNotSource(t *testing.T) {
	res := Scan([]byte(`
/* Fool-proof @font-face */
/// @example scss
///   @font-face {
///     font-family: "source-sans-pro";
///     src: url("fonts/source-sans-pro-regular.woff2") format("woff2");
///   }
///   .documented-example { color: red; }
// @import "not-real";
.real-rule { color: blue; }
`))
	require.Empty(t, res.Imports)
	require.Empty(t, res.FontSources)
	require.Equal(t, []string{".real-rule"}, selectorTexts(res.Selectors))
}

// TestScan_TopLevelSelectorsOnly pins the noise discipline: nested descendant
// rules produce no node, but a rule wrapped in `@media` is still top level —
// an at-rule block is not a style rule.
func TestScan_TopLevelSelectorsOnly(t *testing.T) {
	res := Scan([]byte(`
.card {
  .card-title { font-weight: bold; }
  &:hover { color: red; }
}
#sidebar, .panel { width: 10px; }
@media (max-width: 600px) {
  .mobile-only { display: block; }
}
h1 { margin: 0; }
%placeholder { color: red; }
`))
	require.Equal(t,
		[]string{".card", "#sidebar", ".panel", ".mobile-only"},
		selectorTexts(res.Selectors))
	require.Equal(t, "id", res.Selectors[1].Kind)
	require.Equal(t, "class", res.Selectors[2].Kind)
}

// TestScan_SelectorSubject: only the rightmost simple selector of each
// comma-separated part is the subject — the element a `$(".btn")` lookup
// resolves to. Qualifiers inside `[]`/`()` are not subjects.
func TestScan_SelectorSubject(t *testing.T) {
	res := Scan([]byte(`
.modal .btn { color: red; }
.tabs > .tab.is-active { color: red; }
a[href=".pdf"] .download-link { color: red; }
.list li:not(.excluded) { color: red; }
.icon-#{$name} { color: red; }
.size-1.5x { color: red; }
`))
	require.Equal(t, []string{
		".btn",
		".is-active",
		".download-link",
		".list",
		// `.icon-#{$name}` yields nothing: an interpolated subject can never
		// match a lookup, and its qualifier must not be promoted in its place.
		".size-1", // the `.5x` of a number-like token is not a new selector
	}, selectorTexts(res.Selectors))
}

// TestScan_FontFace covers the @font-face extraction and the interpolation
// ledger: every real `src:` in the orion corpus is built inside a mixin from
// `#{$file-path}`, so it must come back Dynamic rather than as a made-up path.
func TestScan_FontFace(t *testing.T) {
	res := Scan([]byte(`
@font-face {
  font-family: "DejaVu Sans";
  src: url("fonts/dejavu.woff2") format("woff2"),
       url("https://cdn.example.com/dejavu.woff") format("woff");
}
@mixin font-face($font-family, $file-path) {
  @font-face {
    font-family: $font-family;
    src: font-url("#{$file-path}.woff") format("woff");
  }
}
`))
	require.Len(t, res.FontSources, 3)
	require.Equal(t, "DejaVu Sans", res.FontSources[0].Family)
	require.Equal(t, "fonts/dejavu.woff2", res.FontSources[0].URL)
	require.False(t, res.FontSources[0].Dynamic)
	require.Equal(t, "https://cdn.example.com/dejavu.woff", res.FontSources[1].URL)
	require.True(t, res.FontSources[2].Dynamic)
	require.Empty(t, res.Selectors, "a @font-face block declares no selector")
}

// TestScan_InterpolatedBracesDoNotOpenBlocks: `#{...}` contains braces, and
// treating them as block delimiters desynchronises the depth counter for the
// rest of the file.
func TestScan_InterpolatedBracesDoNotOpenBlocks(t *testing.T) {
	res := Scan([]byte(`
.a-#{$x}-b {
  color: red;
  .nested { color: blue; }
}
.after { color: green; }
`))
	// `.a-#{$x}-b` itself is dropped as dynamic; `.after` proves the block
	// depth counter stayed synchronised past the interpolation, and `.nested`
	// proves it did not under-count either.
	require.Equal(t, []string{".after"}, selectorTexts(res.Selectors))
}

// TestScan_StringsShieldDelimiters: a `;`, `{` or `//` inside a quoted value
// must not terminate the declaration or open a block.
func TestScan_StringsShieldDelimiters(t *testing.T) {
	res := Scan([]byte(`
@font-face {
  font-family: Weird;
  src: url("https://cdn.example.com/a;b{c.woff");
}
.after { color: green; }
`))
	require.Len(t, res.FontSources, 1)
	require.Equal(t, "https://cdn.example.com/a;b{c.woff", res.FontSources[0].URL)
	require.Equal(t, []string{".after"}, selectorTexts(res.Selectors))
}

// TestScan_Deterministic: two scans of the same bytes are identical
// (bug-class #2).
func TestScan_Deterministic(t *testing.T) {
	src := []byte(`
@import "a", "b";
.x, .y { color: red; }
@font-face { font-family: F; src: url("f.woff"); }
`)
	require.Equal(t, Scan(src), Scan(src))
	require.Equal(t, []string{"a", "b"}, specs(Scan(src).Imports))
}
