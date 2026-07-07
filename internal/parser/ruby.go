package parser

import (
	"os"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// RubyParser parses Ruby source files.
type RubyParser struct{}

func (p *RubyParser) Language() string     { return "ruby" }
func (p *RubyParser) Extensions() []string { return []string{".rb", ".rake"} }

func (p *RubyParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	results, err := matcher.Match("ruby", file, src)
	if err != nil {
		nodes, edges := patterns.MatchToGraph(service, results)
		setLanguage(nodes, "ruby")
		return nodes, edges, err
	}
	nodes, edges := patterns.MatchToGraph(service, results)
	setLanguage(nodes, "ruby")
	return nodes, edges, nil
}

func init() {
	Register(&RubyParser{})
}
