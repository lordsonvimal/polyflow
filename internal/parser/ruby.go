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

func (p *RubyParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, nil, err
	}
	results, err := matcher.Match("ruby", file, src)
	if err != nil {
		nodes, edges, unresolved := patterns.MatchToGraph(service, results)
		setLanguage(nodes, "ruby")
		return nodes, edges, unresolved, err
	}
	nodes, edges, unresolved := patterns.MatchToGraph(service, results)
	setLanguage(nodes, "ruby")

	// Tier-2 AMQP queue-key resolution: rewrite channel/subscriber nodes whose
	// queue name is a same-file method reference (from_queue resolved_queue_name)
	// to the concrete queue key that method returns. Runs before variable
	// tracking so it only sees the pattern-matched comm nodes.
	resolveRubyQueueKeys(file, src, nodes)

	// Structural variable tracking: constants, classes, ivar reads/writes.
	varNodes, varEdges, varUnresolved := extractRubyVariables(file, service, src)
	nodes = append(nodes, varNodes...)
	edges = append(edges, varEdges...)
	unresolved = append(unresolved, varUnresolved...)
	return nodes, edges, unresolved, nil
}

func init() {
	Register(&RubyParser{})
}
