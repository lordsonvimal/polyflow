package parser

import (
	"os"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// PythonParser parses Python source files.
type PythonParser struct{}

func (p *PythonParser) Language() string     { return "python" }
func (p *PythonParser) Extensions() []string { return []string{".py"} }

func (p *PythonParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, nil, err
	}
	results, err := matcher.Match("python", file, src)
	if err != nil {
		nodes, edges, unresolved := patterns.MatchToGraph(service, results)
		setLanguage(nodes, "python")
		return nodes, edges, unresolved, err
	}
	nodes, edges, unresolved := patterns.MatchToGraph(service, results)
	setLanguage(nodes, "python")
	return nodes, edges, unresolved, nil
}

func init() {
	Register(&PythonParser{})
}
