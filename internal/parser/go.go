package parser

import (
	"os"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// GoParser parses Go source files.
type GoParser struct{}

func (p *GoParser) Language() string     { return "go" }
func (p *GoParser) Extensions() []string { return []string{".go"} }

func (p *GoParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}
	results, err := matcher.Match("go", file, src)
	if err != nil {
		// Return partial results on parse error rather than nothing.
		nodes, edges := patterns.MatchToGraph(service, results)
		setLanguage(nodes, "go")
		return nodes, edges, err
	}
	nodes, edges := patterns.MatchToGraph(service, results)
	setLanguage(nodes, "go")
	return nodes, edges, nil
}

func init() {
	Register(&GoParser{})
}
