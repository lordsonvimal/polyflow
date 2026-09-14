package contract_test

// FX.8.14: AMQP message-type dispatch, migrated off
// internal/linker/amqp_message_type.go onto a contracts/amqp.yaml rule. The
// claim under test:
//
//	producer's `message_type: MT_CREATE_USER` payload field
//	  --publishes--> consumer's `when MT_CREATE_USER` dispatch branch
//
// joined on the shared constant NAME across repos, through REAL parses (not
// hand-built nodes) — same fixtures the retired Go pass used.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

func messageTypeFixtureNodes(t *testing.T) []graph.Node {
	t.Helper()
	reg, err := patterns.EmbeddedRegistry()
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	files := map[string]string{
		"server": "testdata/amqp_message_type/server/data_server_communicator_amqp.rb",
		"agent":  "testdata/amqp_message_type/agent/message_handler.rb",
	}
	var nodes []graph.Node
	for _, svc := range []string{"agent", "server"} {
		f := files[svc]
		p := parser.ForFile(f)
		require.NotNil(t, p, "no parser for %s", f)
		ns, _, _, err := p.Parse(f, svc, m, nil)
		require.NoError(t, err)
		nodes = append(nodes, ns...)
	}
	return nodes
}

func TestAMQPMessageTypeRule_JoinsOnSharedConstantName(t *testing.T) {
	nodes := messageTypeFixtureNodes(t)
	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	e := &contract.Engine{}
	res := e.Link(nodes, rules, nil)

	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}

	var found []graph.Edge
	for _, edge := range res.Edges {
		from, to := byID[edge.From], byID[edge.To]
		if from == nil || to == nil {
			continue
		}
		if from.Meta["pattern"] != "amqp_message_type_pair" || to.Meta["pattern"] != "amqp_message_type_dispatch" {
			continue
		}
		if from.Service == "server" && to.Service == "agent" {
			found = append(found, edge)
		}
	}
	require.NotEmpty(t, found, "no cross-repo message_type edge; dispatch join did not fire")

	var reachedUserHandler bool
	for _, edge := range found {
		assert.Equal(t, graph.ConfidencePartial, edge.Confidence,
			"a name-only message_type join produced a %s edge", edge.Confidence)
		assert.Equal(t, graph.EdgeTypePublishes, edge.Type)
		to := byID[edge.To]
		if to.Meta["handler"] == "UserMessageHandler" {
			reachedUserHandler = true
			assert.Equal(t, "MT_CREATE_USER", edge.Label)
		}
	}
	assert.True(t, reachedUserHandler,
		"producer's MT_CREATE_USER did not reach the consumer's UserMessageHandler branch")
}

// Pins the two asymmetric misses: a producer type no consumer dispatches on
// (MT_DELETE_STUDY) and a consumer branch no producer ever sets
// (MT_ORPHAN_TYPE) must both stay edge-less rather than borrow a neighbour.
func TestAMQPMessageTypeRule_UndeclaredTypeProducesNoEdge(t *testing.T) {
	nodes := messageTypeFixtureNodes(t)
	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	e := &contract.Engine{}
	res := e.Link(nodes, rules, nil)

	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}
	for _, edge := range res.Edges {
		from, to := byID[edge.From], byID[edge.To]
		if from == nil || to == nil || from.Meta["pattern"] != "amqp_message_type_pair" {
			continue
		}
		assert.NotEqual(t, "MT_DELETE_STUDY", edge.Label, "orphan producer type must not be edged")
		assert.NotEqual(t, "OrphanMessageHandler", to.Meta["handler"], "orphan consumer branch must not be edged")
	}
}

// Pins that a same-service match (already resolvable in-repo) is not this
// rule's concern, mirroring the exchange/queue amqp contracts' same_service
// policy.
func TestAMQPMessageTypeRule_SameServiceIsNotADispatchJoin(t *testing.T) {
	nodes := messageTypeFixtureNodes(t)
	for i := range nodes {
		nodes[i].Service = "server" // collapse both fixture files into one service
	}
	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	e := &contract.Engine{}
	res := e.Link(nodes, rules, nil)

	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}
	for _, edge := range res.Edges {
		from, to := byID[edge.From], byID[edge.To]
		if from == nil || to == nil || from.Meta["pattern"] != "amqp_message_type_pair" {
			continue
		}
		assert.NotEqual(t, from.Service, to.Service, "same-service pair must not be joined by this rule")
	}
}
