package parser

import (
	"bytes"
	"os"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
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

	blankedHTML, virtualRuby := splitERB(src)

	// HTML pass: nav links and inline event attributes from static markup.
	htmlResults, _ := matcher.Match("html", file, blankedHTML)
	htmlNodes, htmlEdges, htmlUnresolved := patterns.MatchToGraph(service, htmlResults)
	setLanguage(htmlNodes, "html")

	// Ruby pass: link_to / button_to / form_with helpers plus any other
	// Ruby patterns (route captures in partials, etc.).
	rubyResults, _ := matcher.Match("ruby", file, virtualRuby)
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

// splitERB produces:
//   - blankedHTML: src with ERB tags (including <% and %>) replaced by spaces
//     (newlines preserved to keep line numbers intact).
//   - virtualRuby: src with everything OUTSIDE ERB tags replaced by spaces
//     (newlines preserved); the ERB delimiters and modifier chars (=, -, #)
//     are also blanked so the Ruby parser sees clean code.
func splitERB(src []byte) (blankedHTML, virtualRuby []byte) {
	blankedHTML = bytes.Clone(src)
	virtualRuby = bytes.Clone(src)

	i := 0
	for i < len(src) {
		if i+1 < len(src) && src[i] == '<' && src[i+1] == '%' {
			tagStart := i
			// Scan for closing %>
			j := i + 2
			for j+1 < len(src) && !(src[j] == '%' && src[j+1] == '>') {
				j++
			}
			var tagEnd int
			if j+1 < len(src) {
				tagEnd = j + 2
			} else {
				tagEnd = len(src) // unclosed tag: consume to end
			}

			// blankedHTML: blank the entire tag but preserve newlines.
			for k := tagStart; k < tagEnd; k++ {
				if blankedHTML[k] != '\n' {
					blankedHTML[k] = ' '
				}
			}

			// virtualRuby: blank delimiters (<%, %>) and any modifier char
			// (=, -) immediately after <%; keep the inner Ruby content.
			// Comment tags (<%# ... %>) are blanked entirely — their body is
			// dead text, and leaving it live would mint phantom nodes/edges
			// from commented-out helpers.
			if tagStart < len(virtualRuby) {
				virtualRuby[tagStart] = ' ' // <
			}
			if tagStart+1 < len(virtualRuby) {
				virtualRuby[tagStart+1] = ' ' // %
			}
			inner := tagStart + 2
			if inner < len(src) {
				switch src[inner] {
				case '#':
					for k := inner; k < tagEnd; k++ {
						if virtualRuby[k] != '\n' {
							virtualRuby[k] = ' '
						}
					}
				case '=', '-':
					virtualRuby[inner] = ' '
				}
			}
			// Blank %> (and optional leading - before it)
			if j > tagStart+2 && src[j-1] == '-' {
				virtualRuby[j-1] = ' '
			}
			if j < len(virtualRuby) {
				virtualRuby[j] = ' ' // %
			}
			if j+1 < len(virtualRuby) {
				virtualRuby[j+1] = ' ' // >
			}

			i = tagEnd
		} else {
			// Non-ERB byte: blank in virtualRuby but keep newlines.
			if src[i] != '\n' {
				virtualRuby[i] = ' '
			}
			i++
		}
	}
	return blankedHTML, virtualRuby
}

func init() {
	Register(&ERBParser{})
}
