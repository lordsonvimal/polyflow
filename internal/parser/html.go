package parser

import (
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// HTMLParser parses static HTML files: navigation links (href/action) and
// inline DOM event attributes (onclick=…).
type HTMLParser struct{}

func (p *HTMLParser) Language() string     { return "html" }
func (p *HTMLParser) Extensions() []string { return []string{".html", ".htm"} }

func (p *HTMLParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher, cache SourceCache) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	src, err := readSource(file, cache)
	if err != nil {
		return nil, nil, nil, err
	}
	results, matchErr := matcher.Match("html", file, src)
	results = normalizeHTMLElementMatches(results)
	nodes, edges, unresolved := patterns.MatchToGraph(service, results)
	setLanguage(nodes, "html")
	return nodes, edges, unresolved, matchErr
}

func init() {
	Register(&HTMLParser{})
}
