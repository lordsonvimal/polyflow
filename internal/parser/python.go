package parser

import (
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// PythonParser parses Python source files.
type PythonParser struct{}

func (p *PythonParser) Language() string     { return "python" }
func (p *PythonParser) Extensions() []string { return []string{".py"} }

func (p *PythonParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher, cache SourceCache) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	src, err := readSource(file, cache)
	if err != nil {
		return nil, nil, nil, err
	}
	results, err := matcher.Match("python", file, src)
	results = dropNonHTTPPythonMatches(results, src)
	if err != nil {
		nodes, edges, unresolved := patterns.MatchToGraph(service, results)
		setLanguage(nodes, "python")
		return nodes, edges, unresolved, err
	}
	nodes, edges, unresolved := patterns.MatchToGraph(service, results)
	setLanguage(nodes, "python")

	// Tier PC (docs/python-parity-plan.md): resolve same-file attribute
	// calls (self.foo(), cls.foo(), typed-local x = Foo(); x.bar()) that
	// patterns/python/functions.yaml's identifier-only call pattern cannot
	// see at all.
	edges = append(edges, resolvePythonAttributeCalls(file, src, nodes)...)

	return nodes, edges, unresolved, nil
}

func init() {
	Register(&PythonParser{})
}
