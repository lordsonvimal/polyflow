package parser

import (
	"fmt"
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

	// Tier R: compose Rails' namespace/resources/resource nesting + symbol-based
	// member/collection action names into each route's full absolute path, and
	// (Tier K.1) synthesize the REST routes `resources` declares implicitly.
	// See docs/rails-route-path-composition-plan.md.
	nodes = append(nodes, composeRailsRoutePaths(file, service, src, nodes)...)

	// Structural variable tracking: constants, classes, ivar reads/writes.
	varNodes, varEdges, varUnresolved := extractRubyVariables(file, service, src)
	nodes = append(nodes, varNodes...)
	edges = append(edges, varEdges...)
	unresolved = append(unresolved, varUnresolved...)

	// Attribute comm nodes to their enclosing Ruby method. The pattern matcher's
	// Pass 2 caller attribution only sees pattern-matched scope nodes, but Ruby
	// method nodes come from extractRubyVariables (structural), appended above —
	// after MatchToGraph already ran. So a bunny `exchange.publish` inside a
	// controller action dangles with no caller (regression from e556c69, which
	// dropped the duplicate controller_action method node the matcher used to
	// see). Fill the gap here now that both are in `nodes`.
	edges = linkRubyEnclosingCalls(nodes, edges)

	// Hang each method off its class. Without this a Ruby class node's only
	// outgoing edges are `inherits`, so a trace rooted at "FooController" — the
	// name an agent actually types — walks the ancestor chain and never reaches
	// the action, let alone the view the action renders (Tier K.2).
	edges = append(edges, linkRubyClassMembers(nodes)...)

	return nodes, edges, unresolved, nil
}

// linkRubyClassMembers emits `class --contains--> method` for every method the
// structural pass attributed to a class in the same file. Deterministic: it
// walks nodes in slice order and never consults a map for ordering.
func linkRubyClassMembers(nodes []graph.Node) []graph.Edge {
	classID := map[string]string{} // "file\x00ClassName" → node ID
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeClass {
			classID[nodes[i].File+"\x00"+nodes[i].Label] = nodes[i].ID
		}
	}
	if len(classID) == 0 {
		return nil
	}
	var edges []graph.Edge
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeMethod {
			continue
		}
		owner, ok := classID[n.File+"\x00"+n.Meta["class"]]
		if !ok || owner == n.ID {
			continue
		}
		edges = append(edges, graph.Edge{
			ID:   fmt.Sprintf("%s:%s->%s", string(graph.EdgeTypeContains), owner, n.ID),
			From: owner,
			To:   n.ID,
			Type: graph.EdgeTypeContains,
		})
	}
	return edges
}

// rubyCommEdge maps a comm node type to the caller edge kind it should receive
// from its enclosing method (mirrors matcher.go Pass 2's edge-type switch).
var rubyCommEdge = map[graph.NodeType]graph.EdgeType{
	graph.NodeTypePublisher:       graph.EdgeTypeCalls,
	graph.NodeTypeSubscriber:      graph.EdgeTypeCalls,
	graph.NodeTypeHTTPClient:      graph.EdgeTypeCalls,
	graph.NodeTypeWorker:          graph.EdgeTypeSpawns,
	graph.NodeTypeExternalService: graph.EdgeTypeCloudCall,
}

// linkRubyEnclosingCalls emits a caller edge from each comm node's innermost
// enclosing method (by line range within the same file) when that comm node has
// no incoming caller edge yet. Idempotent gap-filler: it never duplicates an edge
// the matcher already produced. Deterministic — iterates nodes in slice order.
func linkRubyEnclosingCalls(nodes []graph.Node, edges []graph.Edge) []graph.Edge {
	type span struct {
		line, end int
		id        string
	}
	funcsByFile := map[string][]span{}
	hasCaller := map[string]bool{}
	for i := range edges {
		switch edges[i].Type {
		case graph.EdgeTypeCalls, graph.EdgeTypeSpawns, graph.EdgeTypeCloudCall, graph.EdgeTypeRenders:
			hasCaller[edges[i].To] = true
		}
	}
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeMethod {
			continue
		}
		end := 0
		if v, ok := n.Meta["end_line"]; ok {
			fmt.Sscanf(v, "%d", &end)
		}
		funcsByFile[n.File] = append(funcsByFile[n.File], span{n.Line, end, n.ID})
	}
	enclosing := func(file string, line int) (string, bool) {
		best := -1
		spans := funcsByFile[file]
		for j := range spans {
			f := &spans[j]
			if f.line > line {
				continue
			}
			if f.end > 0 && line > f.end {
				continue
			}
			if best == -1 || f.line > spans[best].line {
				best = j
			}
		}
		if best == -1 {
			return "", false
		}
		return spans[best].id, true
	}
	for i := range nodes {
		n := &nodes[i]
		et, ok := rubyCommEdge[n.Type]
		if !ok || hasCaller[n.ID] {
			continue
		}
		fromID, found := enclosing(n.File, n.Line)
		if !found || fromID == n.ID {
			continue
		}
		edges = append(edges, graph.Edge{
			ID:   fmt.Sprintf("%s:%s->%s", string(et), fromID, n.ID),
			From: fromID,
			To:   n.ID,
			Type: et,
		})
		hasCaller[n.ID] = true
	}
	return edges
}

func init() {
	Register(&RubyParser{})
}
