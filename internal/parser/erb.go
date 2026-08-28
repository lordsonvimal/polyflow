package parser

import (
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/railsview"
)

// ERBParser parses Rails ERB template files (.erb / .html.erb).
//
// Strategy (hand-rolled splitter — no tree-sitter ERB grammar exists in the
// smacker/go-tree-sitter module; the delimiters are trivially scannable):
//
//  1. blankedHTML: copy of source where every ERB tag (including delimiters)
//     is replaced byte-for-byte with spaces, preserving newlines so all line
//     numbers are unchanged. HTML patterns run on this view.
//
//  2. virtualRuby: copy of source where everything OUTSIDE ERB tags is
//     replaced with spaces (newlines kept), leaving Ruby code at its original
//     line positions. Ruby patterns and extractRubyVariables run on this view.
//
// Both passes use the original file path so node IDs and line numbers refer
// to the actual ERB file, not a virtual buffer.
type ERBParser struct{}

func (p *ERBParser) Language() string     { return "erb" }
func (p *ERBParser) Extensions() []string { return []string{".erb"} }

func (p *ERBParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher, cache SourceCache) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	src, err := readSource(file, cache)
	if err != nil {
		return nil, nil, nil, err
	}

	blankedHTML, virtualRuby := railsview.SplitERB(src)
	// `file` arrives absolute (needed for readSource/os.ReadFile); every node
	// this parser mints must carry the cwd-relative form instead, matching
	// the Go semantic pass's convention — extractRubyVariables builds nodes
	// directly (bypassing the matcher's own relativization).
	file = patterns.RelativizeToCwd(file)

	var htmlNodes []graph.Node
	var htmlEdges []graph.Edge
	var htmlUnresolved []graph.UnresolvedRef

	if isJSERB(file) {
		// DC.16: a `.js.erb` (Rails `format.js` AJAX-response template) is
		// almost pure JavaScript with occasional `<%= %>` interpolations —
		// blankedHTML already isolates that static content (every ERB tag
		// blanked, everything else untouched at its original offset), so run
		// the JS pattern matcher and structural pass over it instead of the
		// HTML matcher; there is no markup here for HTML patterns to find.
		jsResults, _ := matcher.Match("javascript", file, blankedHTML)
		jsNodes, jsEdges, jsUnresolved := patterns.MatchToGraph(service, jsResults)
		setLanguage(jsNodes, "javascript")

		varNodes, varEdges, varUnresolved, jqListeners := extractJSVariables(file, service, "javascript", "javascript", blankedHTML)
		stampJQueryHandlers(jsNodes, jqListeners)
		jsEdges = reattributeJQueryHandlers(jsNodes, jsEdges, jqListeners)
		jsNodes = mergeJSNodes(jsNodes, varNodes)

		htmlNodes = jsNodes
		htmlEdges = append(jsEdges, varEdges...)
		htmlUnresolved = append(jsUnresolved, varUnresolved...)
	} else {
		// HTML pass: nav links and inline event attributes from static markup.
		htmlResults, _ := matcher.Match("html", file, blankedHTML)
		// C.5: blanking an ERB tag leaves whitespace where an interpolated id= or
		// class= value used to be, and that value is what names the element node.
		htmlResults = normalizeHTMLElementMatches(htmlResults)
		htmlNodes, htmlEdges, htmlUnresolved = patterns.MatchToGraph(service, htmlResults)
		setLanguage(htmlNodes, "html")
	}

	// Ruby pass: link_to / button_to / form_with helpers plus any other
	// Ruby patterns (route captures in partials, etc.).
	rubyResults, _ := matcher.Match("ruby", file, virtualRuby)
	// Same gate as RubyParser: views are where the nav patterns' `_` wildcard
	// binds @helper to `t` or `image_tag` most often, so this is the pass that
	// needs it most.
	rubyResults = dropNonRouteHelperNavMatches(rubyResults)
	rubyNodes, rubyEdges, rubyUnresolved := patterns.MatchToGraph(service, rubyResults)
	setLanguage(rubyNodes, "ruby")

	// Structural variable pass (constants, class hierarchy, ivar reads/writes).
	varNodes, varEdges, varUnresolved := extractRubyVariables(file, service, virtualRuby)

	nodes := append(htmlNodes, rubyNodes...)
	nodes = append(nodes, varNodes...)
	edges := append(htmlEdges, rubyEdges...)
	edges = append(edges, varEdges...)
	unresolved := append(htmlUnresolved, rubyUnresolved...)
	unresolved = append(unresolved, varUnresolved...)

	return nodes, edges, unresolved, nil
}

// isJSERB reports whether file is a Rails `format.js` response template
// (`create.js.erb`, `update.js.coffee.erb`, …) rather than an HTML view.
func isJSERB(file string) bool {
	lower := strings.ToLower(file)
	return strings.HasSuffix(lower, ".js.erb") || strings.HasSuffix(lower, ".js.coffee.erb")
}

func init() {
	Register(&ERBParser{})
}
