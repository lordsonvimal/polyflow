package parser

import (
	"os"

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

func (p *ERBParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, nil, err
	}

	blankedHTML, virtualRuby := railsview.SplitERB(src)

	// HTML pass: nav links and inline event attributes from static markup.
	htmlResults, _ := matcher.Match("html", file, blankedHTML)
	htmlNodes, htmlEdges, htmlUnresolved := patterns.MatchToGraph(service, htmlResults)
	setLanguage(htmlNodes, "html")

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

func init() {
	Register(&ERBParser{})
}
