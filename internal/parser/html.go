package parser

import (
	"os"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// HTMLParser parses static HTML files: navigation links (href/action) and
// inline DOM event attributes (onclick=…).
type HTMLParser struct{}

func (p *HTMLParser) Language() string     { return "html" }
func (p *HTMLParser) Extensions() []string { return []string{".html", ".htm"} }

func (p *HTMLParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	results, matchErr := matcher.Match("html", file, src)
	nodes, edges := patterns.MatchToGraph(service, results)
	setLanguage(nodes, "html")
	return nodes, edges, matchErr
}

func init() {
	Register(&HTMLParser{})
}
