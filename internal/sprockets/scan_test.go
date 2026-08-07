package sprockets

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestScanDirectives_HeaderBlock is nextGen's application.js shape: a grouped
// header with blank lines and prose comments between the directives.
func TestScanDirectives_HeaderBlock(t *testing.T) {
	got := ScanDirectives([]byte(`// This is a manifest file.
//
// VENDOR
//= require node_modules
//= require vendor/twitter-bootstrap-all

// APP
//= require utility/common
//= require_tree ./cdr
//= require_self

jQuery(function () {
  //= require studies
});
`))

	require.Equal(t, []Directive{
		{Verb: "require", Path: "node_modules", Line: 4},
		{Verb: "require", Path: "vendor/twitter-bootstrap-all", Line: 5},
		{Verb: "require", Path: "utility/common", Line: 8},
		{Verb: "require_tree", Path: "./cdr", Line: 9},
	}, got)
}

// TestScanDirectives_BodyIsNotHeader is the false-positive that matters: a
// minified bundle or a template string can contain `//=` anywhere, and Sprockets
// itself only reads the leading comment block.
func TestScanDirectives_BodyIsNotHeader(t *testing.T) {
	require.Empty(t, ScanDirectives([]byte(`var a = 1;
//= require phantom
`)))

	// require_self does not add a dependency and stub removes one; neither is
	// an edge.
	require.Empty(t, ScanDirectives([]byte("//= require_self\n//= stub jquery\n")))
}

func TestScanDirectives_CSSSpellings(t *testing.T) {
	got := ScanDirectives([]byte(`/*
 *= require reset
 *= require_tree .
 */
.btn { color: red; }
`))
	require.Equal(t, []Directive{
		{Verb: "require", Path: "reset", Line: 2},
		{Verb: "require_tree", Path: ".", Line: 3},
	}, got)

	single := ScanDirectives([]byte("/*= require reset */\n"))
	require.Equal(t, []Directive{{Verb: "require", Path: "reset", Line: 1}}, single)
}

func TestScanDirectives_ExtensionFilter(t *testing.T) {
	got := ScanDirectives([]byte(`//= link_tree ../images
//= link_directory ../builds .css
//= link mission_control/jobs/brand.css
`))
	require.Equal(t, []Directive{
		{Verb: "link_tree", Path: "../images", Line: 1},
		{Verb: "link_directory", Path: "../builds", Ext: ".css", Line: 2},
		{Verb: "link", Path: "mission_control/jobs/brand.css", Line: 3},
	}, got)
}

// TestScanIncludeTags_Literals covers nextGen's four real spellings, including
// the multi-line `<%= if ... end %>` block whose two branches name the same
// asset.
func TestScanIncludeTags_Literals(t *testing.T) {
	got := ScanIncludeTags([]byte(`<head>
  <%= stylesheet_link_tag    'application', media: 'all' %>
  <%= javascript_include_tag "home" %>
  <%= if Rails.env.production?
        javascript_include_tag 'home', async: :true, defer: 'defer'
      else
        javascript_include_tag 'home'
      end
  %>
</head>
`))

	require.Equal(t, []IncludeTag{
		{Helper: "stylesheet_link_tag", Name: "application", Line: 2},
		{Helper: "javascript_include_tag", Name: "home", Line: 3},
		{Helper: "javascript_include_tag", Name: "home", Line: 5},
		{Helper: "javascript_include_tag", Name: "home", Line: 7},
	}, got)
}

// TestScanIncludeTags_CommentedOutIsNotALoad: nextGen has three commented-out
// include tags naming assets the page no longer loads. Reading one asserts a
// page→asset binding production does not have.
func TestScanIncludeTags_CommentedOutIsNotALoad(t *testing.T) {
	require.Empty(t, ScanIncludeTags([]byte(`
<%# COMMENTED OUT javascript_include_tag... %>
<%#= javascript_include_tag 'application', 'data-turbolinks-track' => true %>
`)))
}

// TestScanIncludeTags_OptionsAreNotSources: every value in the options hash is
// a string too, and counting them would double the asset graph with names that
// resolve to nothing.
func TestScanIncludeTags_OptionsAreNotSources(t *testing.T) {
	got := ScanIncludeTags([]byte(
		`<%= javascript_include_tag 'application', 'data-turbolinks-track' => true %>`))
	require.Equal(t, []IncludeTag{
		{Helper: "javascript_include_tag", Name: "application", Line: 1},
	}, got)

	multi := ScanIncludeTags([]byte(`<%= javascript_include_tag("a", "b", media: "all") %>`))
	require.Equal(t, []IncludeTag{
		{Helper: "javascript_include_tag", Name: "a", Line: 1},
		{Helper: "javascript_include_tag", Name: "b", Line: 1},
	}, multi)
}

// TestScanIncludeTags_DynamicIsFlagged: an interpolated or variable source is
// ledgered, never guessed (phases.md #12).
func TestScanIncludeTags_DynamicIsFlagged(t *testing.T) {
	got := ScanIncludeTags([]byte(
		"<%= javascript_include_tag \"http://localhost:5200/#{component_name}.jsx\", type: \"module\" %>"))
	require.Len(t, got, 1)
	require.True(t, got[0].Dynamic)

	fromVar := ScanIncludeTags([]byte(`<%= javascript_include_tag @bundle %>`))
	require.Len(t, fromVar, 1)
	require.True(t, fromVar[0].Dynamic)
}

func TestScanIncludeTags_Deterministic(t *testing.T) {
	src := []byte(`<%= javascript_include_tag "home" %>
<%= stylesheet_link_tag "application" %>`)
	first := ScanIncludeTags(src)
	for i := 0; i < 5; i++ {
		require.Equal(t, first, ScanIncludeTags(src))
	}
}
