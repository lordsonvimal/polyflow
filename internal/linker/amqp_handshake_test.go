package linker_test

// Tier K.6 step 3 acceptance, through REAL parses and the REAL contract engine
// (bug-class #6 — hand-built nodes would skip the pattern matcher and Tier 2,
// which is precisely the seam this pass sits on). The claim under test:
//
//	agent publish site (queue name known only as a handshake field symbol)
//	  --publishes--> server worker consuming the queue that field carries
//
// so "where does a CDR progress event go" is answerable across the repo
// boundary, without either repo ever sharing a queue literal.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// handshakeFixture parses the two-repo fixture and runs the handshake pass.
// Nodes are returned post-enrichment, matching what the engine sees.
func handshakeFixture(t *testing.T) ([]graph.Node, []graph.UnresolvedRef, map[string]bool) {
	t.Helper()
	reg, err := patterns.DefaultRegistry("../../patterns")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	files := map[string][]string{
		"server": {
			"testdata/amqp_handshake/server/queue_names.rb",
			"testdata/amqp_handshake/server/agents_controller.rb",
			"testdata/amqp_handshake/server/progress_worker.rb",
		},
		"agent": {"testdata/amqp_handshake/agent/publisher.rb"},
	}
	var nodes []graph.Node
	// Service order is fixed rather than ranged over, so the fixture cannot
	// depend on map iteration order (bug-class #2).
	for _, svc := range []string{"agent", "server"} {
		for _, f := range files[svc] {
			p := parser.ForFile(f)
			require.NotNil(t, p, "no parser for %s", f)
			ns, _, _, err := p.Parse(f, svc, m, nil)
			require.NoError(t, err)
			nodes = append(nodes, ns...)
		}
	}
	unresolved, resolved := linker.LinkAMQPHandshake(nodes)
	return nodes, unresolved, resolved
}

// findNode returns the single node matching a predicate, failing otherwise.
func findNode(t *testing.T, nodes []graph.Node, pred func(*graph.Node) bool) *graph.Node {
	t.Helper()
	var hits []*graph.Node
	for i := range nodes {
		if pred(&nodes[i]) {
			hits = append(hits, &nodes[i])
		}
	}
	require.Len(t, hits, 1, "expected exactly one matching node")
	return hits[0]
}

// TestAMQPHandshake_ResolvesQueueAcrossRepos is the phase's central claim: the
// publish site's queue name is knowable only by following the field symbol into
// the other repo, and after this pass it carries the resolved name.
func TestAMQPHandshake_ResolvesQueueAcrossRepos(t *testing.T) {
	t.Parallel()
	nodes, _, _ := handshakeFixture(t)

	pub := findNode(t, nodes, func(n *graph.Node) bool {
		return n.Service == "agent" && n.Type == graph.NodeTypeChannel &&
			n.Meta["broker_field"] == "amqp_progress_events_queue_name" &&
			n.Meta["pattern"] == "bunny_queue_declare"
	})

	assert.Equal(t, "cdr_progress_events", pub.Meta["queue_name"],
		"publish site did not inherit the queue the server declares for this field")
	assert.Equal(t, "amqp_handshake", pub.Meta["key_resolved_via"])
	assert.Empty(t, pub.Meta["key_dynamic"], "resolved key still marked dynamic")

	// A handshake match is agreement on a name, not proof of reachability, so
	// it must never be able to produce a `static` edge (plan-14 trust
	// soundness). The ceiling is what enforces that in the engine.
	assert.Equal(t, graph.ConfidencePartial, pub.Meta["confidence_ceiling"])
}

// TestAMQPHandshake_ResolvesZeroMiddleSegmentField is the Tier AH regression:
// "amqp_queue_name" has no middle segment between "amqp_" and "queue_name",
// which used to fall one character short of the match regex's minimum length
// (prefix + suffix could not share the single separating underscore) and
// silently produced no amqp_field_pair/amqp_field_symbol node at all — not a
// resolution failure, an absence. This is the busiest handshake field in the
// real fleet (nextGen's generic task queue).
//
// Isolated in its own two-file fixture rather than added as a third field to
// testdata/amqp_handshake's shared publisher class: doing that tripped an
// unrelated pre-existing contract-engine ambiguity (any third publish site
// added to that file, even one reusing an already-resolved queue, made
// TestAMQPHandshake_ContractClosesPublisherToConsumer's cross-repo edge
// disappear) — a separate bug, out of Tier AH's scope.
func TestAMQPHandshake_ResolvesZeroMiddleSegmentField(t *testing.T) {
	t.Parallel()
	reg, err := patterns.DefaultRegistry("../../patterns")
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)

	files := map[string][]string{
		"server": {
			"testdata/amqp_handshake_zero_middle/server/queue_names.rb",
			"testdata/amqp_handshake_zero_middle/server/agents_controller.rb",
			"testdata/amqp_handshake_zero_middle/server/task_worker.rb",
		},
		"agent": {"testdata/amqp_handshake_zero_middle/agent/publisher.rb"},
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
	_, _ = linker.LinkAMQPHandshake(nodes)

	pub := findNode(t, nodes, func(n *graph.Node) bool {
		return n.Service == "agent" && n.Type == graph.NodeTypeChannel &&
			n.Meta["broker_field"] == "amqp_queue_name" &&
			n.Meta["pattern"] == "bunny_queue_declare"
	})

	assert.Equal(t, "generic_task", pub.Meta["queue_name"],
		"zero-middle-segment field did not inherit the queue the server declares for it")
	assert.Equal(t, "amqp_handshake", pub.Meta["key_resolved_via"])
	assert.Empty(t, pub.Meta["key_dynamic"], "resolved key still marked dynamic")
	assert.Equal(t, graph.ConfidencePartial, pub.Meta["confidence_ceiling"])
}

// TestAMQPHandshake_ContractClosesPublisherToConsumer pins that the resolution
// is expressed through the EXISTING queue_name contract rather than a second
// queue matcher — and that the edge lands at partial, not static.
func TestAMQPHandshake_ContractClosesPublisherToConsumer(t *testing.T) {
	t.Parallel()
	nodes, _, _ := handshakeFixture(t)

	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	eng := &contract.Engine{}
	result := eng.Link(nodes, rules, nil)

	byID := map[string]*graph.Node{}
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}

	var found []graph.Edge
	for _, e := range result.Edges {
		from, to := byID[e.From], byID[e.To]
		if from == nil || to == nil || from.Service == to.Service {
			continue
		}
		if from.Service == "agent" && to.Service == "server" && e.Type == graph.EdgeTypePublishes {
			found = append(found, e)
		}
	}
	require.NotEmpty(t, found, "no cross-repo publishes edge; handshake did not reach the contract engine")

	var reachedWorker bool
	for _, e := range found {
		assert.Equal(t, graph.ConfidencePartial, e.Confidence,
			"a handshake-resolved key produced a %s edge", e.Confidence)
		if to := byID[e.To]; to != nil && to.Meta["queue_name"] == "cdr_progress_events" {
			reachedWorker = true
			// The publish site's own line says `dynamic`. Unless the edge names
			// the field the two repos agreed on, nothing in the graph explains
			// why these two nodes are connected.
			assert.Equal(t, "amqp_handshake", e.Meta["resolved_via"])
			assert.Equal(t, "amqp_progress_events_queue_name", e.Meta["handshake_field"])
		}
	}
	assert.True(t, reachedWorker, "publish site does not reach the worker consuming that queue")

	// The queue contract is symmetric, so once both sides carry the same
	// queue_name it will happily emit the reverse edge too — claiming the
	// Sneakers worker publishes to the agent that feeds it. Direction is the
	// whole point of a message-flow edge, so pin it.
	for _, e := range result.Edges {
		from, to := byID[e.From], byID[e.To]
		if from == nil || to == nil {
			continue
		}
		if from.Meta["pattern"] == "kicks_from_queue" && e.Type == graph.EdgeTypePublishes {
			t.Fatalf("a from_queue consumer was made the producer of %s -> %s", from.File, to.File)
		}
	}
}

// TestAMQPHandshake_UndeclaredFieldIsLedgered: a field no service declares is a
// clue this pass tried and failed to resolve. It must reach the ledger, and it
// must not borrow the queue of the field next to it (bug-class #12).
func TestAMQPHandshake_UndeclaredFieldIsLedgered(t *testing.T) {
	t.Parallel()
	nodes, unresolved, _ := handshakeFixture(t)

	var kinds []string
	for _, u := range unresolved {
		if u.Name == "amqp_orphan_events_queue_name" {
			kinds = append(kinds, u.Kind)
		}
	}
	assert.Equal(t, []string{"amqp_handshake_unresolved"}, kinds)

	for i := range nodes {
		n := &nodes[i]
		if n.Meta["broker_field"] == "amqp_orphan_events_queue_name" {
			assert.Empty(t, n.Meta["queue_name"], "undeclared field was given a queue name")
		}
	}
}

// TestAMQPHandshake_RetractsStaleLedgerEntries: earlier passes ledger the
// publish site as unresolvable, which was true until this pass ran. Leaving the
// entry would have polyflow assert the edge and deny it in the same index, and
// send a reader off to hand-verify a link it already has.
func TestAMQPHandshake_RetractsStaleLedgerEntries(t *testing.T) {
	t.Parallel()
	nodes, _, resolved := handshakeFixture(t)

	pub := findNode(t, nodes, func(n *graph.Node) bool {
		return n.Service == "agent" && n.Meta["broker_field"] == "amqp_progress_events_queue_name" &&
			n.Meta["pattern"] == "bunny_queue_declare"
	})

	before := []graph.UnresolvedRef{
		{Service: pub.Service, File: pub.File, Line: pub.Line,
			Name: "Messaging::Publisher.progress_events_queue_name", Kind: "config_not_found"},
		// A different clue on the same line: this pass says nothing about it.
		{Service: pub.Service, File: pub.File, Line: pub.Line, Name: "Retryable", Kind: "inherits_unresolved"},
		// The orphan field stays: nothing resolved it.
		{Service: pub.Service, File: pub.File, Line: pub.Line + 7,
			Name: "orphan", Kind: "config_not_found"},
	}
	after := linker.DropResolvedRefs(append([]graph.UnresolvedRef(nil), before...), resolved)

	var kinds []string
	for _, r := range after {
		kinds = append(kinds, r.Name+"/"+r.Kind)
	}
	assert.NotContains(t, kinds, "Messaging::Publisher.progress_events_queue_name/config_not_found",
		"resolved publish site is still ledgered as unresolvable")
	assert.Contains(t, kinds, "Retryable/inherits_unresolved",
		"an unrelated clue on the same line was retracted")
	assert.Contains(t, kinds, "orphan/config_not_found",
		"an unresolved line was retracted")
}

// TestAMQPHandshake_SameServiceIsNotAHandshake: the declaring side must be a
// different repo. A same-service match would mean the name was resolvable
// in-file, which is Tier 2's job — accepting it here would hide a real
// cross-repo link behind a local shortcut.
func TestAMQPHandshake_SameServiceIsNotAHandshake(t *testing.T) {
	t.Parallel()
	nodes, _, _ := handshakeFixture(t)

	// Re-run with everything in one service: the same evidence must now resolve
	// nothing at all.
	single := make([]graph.Node, len(nodes))
	copy(single, nodes)
	for i := range single {
		single[i].Service = "one"
		meta := map[string]string{}
		for k, v := range nodes[i].Meta {
			meta[k] = v
		}
		delete(meta, "queue_name")
		delete(meta, "key_resolved_via")
		delete(meta, "confidence_ceiling")
		if meta["broker_field"] != "" && meta["pattern"] == "bunny_queue_declare" {
			meta["key_dynamic"] = "true"
		}
		single[i].Meta = meta
	}
	_, _ = linker.LinkAMQPHandshake(single)

	for i := range single {
		n := &single[i]
		if n.Meta["key_resolved_via"] == "amqp_handshake" {
			t.Fatalf("same-service handshake resolved %s at %s:%d", n.Meta["broker_field"], n.File, n.Line)
		}
	}
}

func TestAMQPHandshake_Deterministic(t *testing.T) {
	t.Parallel()
	firstNodes, firstUnresolved, _ := handshakeFixture(t)
	for i := 0; i < 4; i++ {
		gotNodes, gotUnresolved, _ := handshakeFixture(t)
		require.Equal(t, firstNodes, gotNodes)
		require.Equal(t, firstUnresolved, gotUnresolved)
	}
}
