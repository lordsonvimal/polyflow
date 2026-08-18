package parser_test

// Ruby channel-node attribution: a queue/exchange declaration must hang off the
// scope that declares it, so a trace rooted at the publishing method (or at the
// worker class) reaches the queue.
//
// Before this, `rubyCommEdge` listed every comm node type except
// graph.NodeTypeChannel, so 22 of 29 non-spec Ruby channel nodes in the AMQP
// fleet had no inbound edge at all: the K.6 cross-repo publishes edge existed
// but was unreachable from either endpoint.
//
// Real parses throughout (bug-class #6) — hand-built nodes would skip the
// pattern matcher, which is where the channel node and its type come from.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
)

// parseRuby runs the full Ruby parse pipeline on a fixture.
func parseRuby(t *testing.T, file string) ([]graph.Node, []graph.Edge) {
	t.Helper()
	p := parser.ForFile(file)
	require.NotNil(t, p, "no parser for %s", file)
	nodes, edges, _, err := p.Parse(file, service, mustMatcher(t), nil)
	require.NoError(t, err)
	return nodes, edges
}

// nodesOfType collects every node of a type.
func nodesOfType(nodes []graph.Node, typ graph.NodeType) []*graph.Node {
	var out []*graph.Node
	for i := range nodes {
		if nodes[i].Type == typ {
			out = append(out, &nodes[i])
		}
	}
	return out
}

// inboundOf returns the edges pointing at a node.
func inboundOf(edges []graph.Edge, id string) []graph.Edge {
	var out []graph.Edge
	for _, e := range edges {
		if e.To == id {
			out = append(out, e)
		}
	}
	return out
}

// TestRubyChannel_AttributedToEnclosingMethod is the central claim: a
// `channel.queue(...)` inside a method is reachable from that method.
func TestRubyChannel_AttributedToEnclosingMethod(t *testing.T) {
	t.Parallel()
	nodes, edges := parseRuby(t, "testdata/amqp_attribution/publisher.rb")

	channels := nodesOfType(nodes, graph.NodeTypeChannel)
	require.NotEmpty(t, channels, "fixture produced no channel node")

	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}

	for _, ch := range channels {
		in := inboundOf(edges, ch.ID)
		require.NotEmpty(t, in, "channel at line %d has no inbound edge", ch.Line)

		var fromPublish bool
		for _, e := range in {
			from := byID[e.From]
			require.NotNil(t, from, "edge from a node not in this file (bug-class #10)")
			if from.Label == "publish_audit_event" {
				fromPublish = true
				assert.Equal(t, graph.EdgeTypeCalls, e.Type,
					"a queue declaration inside a method is a call, matching matcher.go Pass 2")
			}
		}
		assert.True(t, fromPublish,
			"queue declaration not attributed to publish_audit_event; trace from the publisher stops short of the queue")
	}
}

// TestRubyChannel_ClassBodyDeclarationAttributedToClass: a Sneakers `from_queue`
// has no enclosing method, so the class is the only honest anchor. Without it
// the one node naming the worker's queue hangs off nothing.
func TestRubyChannel_ClassBodyDeclarationAttributedToClass(t *testing.T) {
	t.Parallel()
	nodes, edges := parseRuby(t, "testdata/amqp_attribution/event_worker.rb")

	channels := nodesOfType(nodes, graph.NodeTypeChannel)
	require.Len(t, channels, 1, "expected exactly one from_queue channel node")
	ch := channels[0]

	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}

	in := inboundOf(edges, ch.ID)
	require.NotEmpty(t, in, "class-body from_queue has no inbound edge")

	var fromClass bool
	for _, e := range in {
		from := byID[e.From]
		require.NotNil(t, from)
		if from.Type == graph.NodeTypeClass && from.Label == "WorkspaceEventWorker" {
			fromClass = true
			// A class body declares; it does not call. `contains` is what the
			// K.2 class→method edges already use, so a trace rooted at the class
			// name walks into the queue the same way it walks into an action.
			assert.Equal(t, graph.EdgeTypeContains, e.Type)
		}
	}
	assert.True(t, fromClass, "from_queue not attributed to its worker class")
}

// TestRubyChannel_NoFabricatedAnchorOutsideClassBody is the guard on the
// fallback. tasks.rake closes `module Kicks` at line 10 and declares a queue at
// line 15 inside a rake task block. Nearest-preceding attribution would claim
// Kicks contains that queue, which is false — and a wrong edge is worse than a
// missing one (bug-class #12). The node stays unattributed.
func TestRubyChannel_NoFabricatedAnchorOutsideClassBody(t *testing.T) {
	t.Parallel()
	nodes, edges := parseRuby(t, "testdata/amqp_attribution/tasks.rake")

	channels := nodesOfType(nodes, graph.NodeTypeChannel)
	require.NotEmpty(t, channels, "fixture produced no channel node")

	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}

	for _, ch := range channels {
		for _, e := range inboundOf(edges, ch.ID) {
			from := byID[e.From]
			require.NotNil(t, from)
			assert.NotEqual(t, "Kicks", from.Label,
				"queue at line %d attributed to a module that closes at line %d",
				ch.Line, from.Line)
		}
	}
}

// TestRubyChannel_MethodBeatsClass: when both a method and a class enclose the
// node, the method wins — it is the tighter scope and the one an agent traces.
func TestRubyChannel_MethodBeatsClass(t *testing.T) {
	t.Parallel()
	nodes, edges := parseRuby(t, "testdata/amqp_attribution/publisher.rb")

	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}
	for _, ch := range nodesOfType(nodes, graph.NodeTypeChannel) {
		for _, e := range inboundOf(edges, ch.ID) {
			if from := byID[e.From]; from != nil && from.Type == graph.NodeTypeClass {
				t.Fatalf("channel at line %d anchored on class %s despite an enclosing method",
					ch.Line, from.Label)
			}
		}
	}
}

// TestRubyChannel_CallSiteIsNotAScope: `before_action :ensure_valid_token` is a
// class-body callback registration. The pattern matcher mints it as a function
// node, and in a Rails controller it is the ONLY scope candidate Pass 2 can see
// (real Ruby methods are structural, added after MatchToGraph), so it used to
// collect every comm node in the file — here, queue declarations inside a
// private method 8 lines below it.
//
// A call site has no body. The signal is end_line: a declaration pattern
// captures @_def and records its span, a call pattern cannot.
func TestRubyChannel_CallSiteIsNotAScope(t *testing.T) {
	t.Parallel()
	nodes, edges := parseRuby(t, "testdata/amqp_attribution/agents_controller.rb")

	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}

	channels := nodesOfType(nodes, graph.NodeTypeChannel)
	require.NotEmpty(t, channels, "fixture produced no queue-name declarations")

	for _, ch := range channels {
		in := inboundOf(edges, ch.ID)
		require.NotEmpty(t, in, "queue declaration at line %d has no inbound edge", ch.Line)
		for _, e := range in {
			from := byID[e.From]
			require.NotNil(t, from)
			assert.NotEqual(t, "before_action", from.Label,
				"queue at line %d attributed to a callback registration at line %d",
				ch.Line, from.Line)
			assert.Equal(t, "registration_json", from.Label,
				"queue at line %d belongs to the method that declares it", ch.Line)
		}
	}
}

// TestRubyChannel_AttributionDeterministic: the pass consults maps keyed by
// file, so pin that repeated parses agree (bug-class #2).
func TestRubyChannel_AttributionDeterministic(t *testing.T) {
	t.Parallel()
	files := []string{
		"testdata/amqp_attribution/publisher.rb",
		"testdata/amqp_attribution/event_worker.rb",
		"testdata/amqp_attribution/tasks.rake",
		"testdata/amqp_attribution/agents_controller.rb",
	}
	for _, f := range files {
		firstNodes, firstEdges := parseRuby(t, f)
		for i := 0; i < 3; i++ {
			gotNodes, gotEdges := parseRuby(t, f)
			require.Equal(t, firstNodes, gotNodes, "nodes differ across parses of %s", f)
			require.Equal(t, firstEdges, gotEdges, "edges differ across parses of %s", f)
		}
	}
}
