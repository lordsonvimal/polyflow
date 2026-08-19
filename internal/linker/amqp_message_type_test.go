package linker_test

// AH follow-up: AMQP message-type dispatch, the payload-shape analogue of
// amqp_handshake.go's queue-name join. The claim under test:
//
//	producer's `message_type: MT_CREATE_USER` payload field
//	  --publishes--> consumer's `when MT_CREATE_USER` dispatch branch
//
// joined on the shared constant NAME across repos, same as amqp_handshake's
// broker_field bridge — through REAL parses, not hand-built nodes.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

func messageTypeFixture(t *testing.T) []graph.Node {
	t.Helper()
	reg, err := patterns.DefaultRegistry("../../patterns")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	files := map[string][]string{
		"server": {"testdata/amqp_message_type/server/data_server_communicator_amqp.rb"},
		"agent":  {"testdata/amqp_message_type/agent/message_handler.rb"},
	}
	var nodes []graph.Node
	for _, svc := range []string{"agent", "server"} {
		for _, f := range files[svc] {
			p := parser.ForFile(f)
			require.NotNil(t, p, "no parser for %s", f)
			ns, _, _, err := p.Parse(f, svc, m, nil)
			require.NoError(t, err)
			nodes = append(nodes, ns...)
		}
	}
	return nodes
}

func TestAMQPMessageTypeDispatch_JoinsOnSharedConstantName(t *testing.T) {
	t.Parallel()
	nodes := messageTypeFixture(t)

	edges := linker.LinkAMQPMessageTypeDispatch(nodes)

	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}

	var found []graph.Edge
	for _, e := range edges {
		from, to := byID[e.From], byID[e.To]
		require.NotNil(t, from)
		require.NotNil(t, to)
		if from.Service == "server" && to.Service == "agent" {
			found = append(found, e)
		}
	}
	require.NotEmpty(t, found, "no cross-repo message_type edge; dispatch join did not fire")

	var reachedUserHandler bool
	for _, e := range found {
		assert.Equal(t, graph.ConfidencePartial, e.Confidence,
			"a name-only message_type join produced a %s edge", e.Confidence)
		assert.Equal(t, graph.EdgeTypePublishes, e.Type)
		to := byID[e.To]
		if to.Meta["handler"] == "UserMessageHandler" {
			reachedUserHandler = true
			assert.Equal(t, "MT_CREATE_USER", e.Meta["message_type"])
			assert.Equal(t, "amqp_message_type", e.Meta["resolved_via"])
		}
	}
	assert.True(t, reachedUserHandler,
		"producer's MT_CREATE_USER did not reach the consumer's UserMessageHandler branch")
}

// TestAMQPMessageTypeDispatch_UndeclaredTypeProducesNoEdge pins the two
// asymmetric misses: a producer type no consumer dispatches on
// (MT_DELETE_STUDY) and a consumer branch no producer ever sets
// (MT_ORPHAN_TYPE) must both stay edge-less rather than borrow a neighbour.
func TestAMQPMessageTypeDispatch_UndeclaredTypeProducesNoEdge(t *testing.T) {
	t.Parallel()
	nodes := messageTypeFixture(t)
	edges := linker.LinkAMQPMessageTypeDispatch(nodes)

	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}
	for _, e := range edges {
		mt := e.Meta["message_type"]
		assert.NotEqual(t, "MT_DELETE_STUDY", mt, "orphan producer type must not be edged")
		to := byID[e.To]
		assert.NotEqual(t, "OrphanMessageHandler", to.Meta["handler"], "orphan consumer branch must not be edged")
	}
}

// TestAMQPMessageTypeDispatch_SameServiceIsNotADispatchJoin pins that a
// same-service match (already resolvable in-repo) is not this pass's
// concern, mirroring amqp_handshake's same-service exclusion.
func TestAMQPMessageTypeDispatch_SameServiceIsNotADispatchJoin(t *testing.T) {
	t.Parallel()
	nodes := messageTypeFixture(t)
	edges := linker.LinkAMQPMessageTypeDispatch(nodes)

	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}
	for _, e := range edges {
		from, to := byID[e.From], byID[e.To]
		assert.NotEqual(t, from.Service, to.Service, "same-service pair must not be joined by this pass")
	}
}
