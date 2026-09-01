package contract_test

// Tests for the embedded messaging contract rules (amqp, hub, jobs, pusher, websocket).
// Positive fixtures assert expected edges; negative fixtures assert silence or
// ledger surfacing — matching the phases.md requirement.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// rulesOfKind returns only the loaded rules for a given kind.
func rulesOfKind(t *testing.T, kind contract.Kind) []contract.Rule {
	t.Helper()
	all, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	var out []contract.Rule
	for _, r := range all {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func runKind(t *testing.T, kind contract.Kind, nodes []graph.Node) contract.Result {
	t.Helper()
	rules := rulesOfKind(t, kind)
	require.NotEmpty(t, rules, "no rules loaded for kind %s", kind)
	e := &contract.Engine{}
	return e.Link(nodes, rules, nil)
}

// ── AMQP ─────────────────────────────────────────────────────────────────────

// Positive: two channel nodes with the same exchange+routing_key in different
// services produce a cross-service publishes edge.
func TestAMQPRule_CrossServiceMatch(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc-a:channel:user.events/user.created", Type: graph.NodeTypeChannel, Service: "svc-a",
			Meta: map[string]string{"exchange": "user.events", "routing_key": "user.created"}},
		{ID: "svc-b:channel:user.events/user.created", Type: graph.NodeTypeChannel, Service: "svc-b",
			Meta: map[string]string{"exchange": "user.events", "routing_key": "user.created"}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	// Expect at least one cross-service edge (engine may emit both directions).
	crossEdges := 0
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypePublishes {
			crossEdges++
		}
	}
	require.Greater(t, crossEdges, 0, "expected cross-service publishes edge")
	assert.Equal(t, "broker", res.Edges[0].ID[:len("broker")], "edge ID must start with 'broker:'")
	assert.Equal(t, "amqp_channel", res.Edges[0].Meta["via"])
}

// Positive: quoted exchange values are normalised by quote_strip.
func TestAMQPRule_QuotedKeyNormalised(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:channel:orders/placed", Type: graph.NodeTypeChannel, Service: "svc-a",
			Meta: map[string]string{"exchange": `"orders"`, "routing_key": `"placed"`}},
		{ID: "b:channel:orders/placed", Type: graph.NodeTypeChannel, Service: "svc-b",
			Meta: map[string]string{"exchange": "orders", "routing_key": "placed"}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	var found bool
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypePublishes {
			found = true
		}
	}
	assert.True(t, found, "quote_strip must allow quoted channel to match unquoted")
}

// Positive: queue-based flows (bunny channel.queue publisher declare <->
// kicks from_queue consumer) match cross-service on queue_name alone, with no
// exchange/routing_key present.
func TestAMQPRule_QueueName_CrossServiceMatch(t *testing.T) {
	nodes := []graph.Node{
		{ID: "consumer-a:channel:vega_progress_events", Type: graph.NodeTypeChannel, Service: "consumer-a",
			Meta: map[string]string{"queue_name": "vega_progress_events"}},
		{ID: "main-svc:channel:vega_progress_events", Type: graph.NodeTypeChannel, Service: "main-svc",
			Meta: map[string]string{"queue_name": "vega_progress_events"}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	crossEdges := 0
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypePublishes {
			crossEdges++
		}
	}
	require.Greater(t, crossEdges, 0, "expected cross-service queue publishes edge")
	assert.Equal(t, "broker_queue", res.Edges[0].ID[:len("broker_queue")],
		"queue-based edge ID must start with 'broker_queue:'")
}

// Positive: quoted queue names normalise via quote_strip.
func TestAMQPRule_QueueName_QuotedNormalised(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:channel:q", Type: graph.NodeTypeChannel, Service: "svc-a",
			Meta: map[string]string{"queue_name": `"audit_events"`}},
		{ID: "b:channel:q", Type: graph.NodeTypeChannel, Service: "svc-b",
			Meta: map[string]string{"queue_name": "audit_events"}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	var found bool
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypePublishes {
			found = true
		}
	}
	assert.True(t, found, "quote_strip must let a quoted queue_name match an unquoted one")
}

// Negative: an exchange-only channel and a queue-only channel must NOT link —
// both have an empty key under the other's contract (keyIsEmpty guard), so no
// empty-string false match crosses the two AMQP contracts.
func TestAMQPRule_QueueAndExchange_NoEmptyKeyCollision(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:channel:ex", Type: graph.NodeTypeChannel, Service: "svc-a",
			Meta: map[string]string{"exchange": "orders", "routing_key": "placed"}},
		{ID: "b:channel:q", Type: graph.NodeTypeChannel, Service: "svc-b",
			Meta: map[string]string{"queue_name": "audit_events"}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	assert.Empty(t, res.Edges, "exchange-only and queue-only channels must not cross-match")
}

// Negative: different queue names must not match.
func TestAMQPRule_QueueName_Different_NoEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:channel:q", Type: graph.NodeTypeChannel, Service: "svc-a",
			Meta: map[string]string{"queue_name": "vega_progress_events"}},
		{ID: "b:channel:q", Type: graph.NodeTypeChannel, Service: "svc-b",
			Meta: map[string]string{"queue_name": "audit_events"}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	assert.Empty(t, res.Edges, "different queue names must not match")
}

// Negative: same-service channels must not link (skip policy).
func TestAMQPRule_SameService_NoCrossEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:channel:orders/placed", Type: graph.NodeTypeChannel, Service: "svc",
			Meta: map[string]string{"exchange": "orders", "routing_key": "placed"}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	assert.Empty(t, res.Edges, "single-service channel must produce no edge")
}

// Negative: channels with different routing_key must not match.
func TestAMQPRule_DifferentRoutingKey_NoEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:channel:orders/placed", Type: graph.NodeTypeChannel, Service: "svc-a",
			Meta: map[string]string{"exchange": "orders", "routing_key": "placed"}},
		{ID: "b:channel:orders/shipped", Type: graph.NodeTypeChannel, Service: "svc-b",
			Meta: map[string]string{"exchange": "orders", "routing_key": "shipped"}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	assert.Empty(t, res.Edges, "different routing_key must not match")
	assert.Empty(t, res.Unresolved, "unmatched channels are silently dropped")
}

// J.1 positive: a producer whose routing key was reconstructed as "container.*"
// (X.11 Sprintf) meets a binding declared "container.#" once both collapse under
// amqp_topic_wildcard. This is the container_events pair the fleet audit found
// missing entirely.
func TestAMQPRule_TopicWildcard_ProducerMeetsHashBinding(t *testing.T) {
	nodes := []graph.Node{
		{ID: "maple-agent:channel:container_events/container.*", Type: graph.NodeTypeChannel, Service: "maple-agent",
			Meta: map[string]string{"exchange": "container_events", "routing_key": "container.*"}},
		{ID: "maple-manager:channel:container_events/container.#", Type: graph.NodeTypeChannel, Service: "maple-manager",
			Meta: map[string]string{"exchange": "container_events", "routing_key": "container.#",
				"resolved_via": "static_table"}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	require.NotEmpty(t, res.Edges, "container.* must meet container.# after wildcard collapse")
	for _, e := range res.Edges {
		assert.Equal(t, graph.EdgeTypePublishes, e.Type)
		assert.NotEqual(t, graph.ConfidenceStatic, e.Confidence,
			"a wildcard-collapsed match is inferred, never static")
	}
}

// J.1 negative: collapsing wildcards must not join two distinct literal topics on
// one exchange.
func TestAMQPRule_TopicWildcard_DistinctLiteralKeysStillNoEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:channel:build_logs/logs.build.*", Type: graph.NodeTypeChannel, Service: "svc-a",
			Meta: map[string]string{"exchange": "build_logs", "routing_key": "logs.build.*"}},
		{ID: "b:channel:build_logs/logs.runner.*", Type: graph.NodeTypeChannel, Service: "svc-b",
			Meta: map[string]string{"exchange": "build_logs", "routing_key": "logs.runner.*"}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	assert.Empty(t, res.Edges, "logs.build.* and logs.runner.* are different topics")
}

// J.1: the exchange_only tier fires when the producer's routing key is
// unresolvable, and stamps `partial` — a fanout exchange with an unknown key is
// a partial answer and must not be promotable to verified (plan-14).
func TestAMQPExchangeOnlyTier_StampsPartial(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:channel:container_events/", Type: graph.NodeTypeChannel, Service: "svc-a",
			Meta: map[string]string{"exchange": "container_events", "routing_key": ""}},
		{ID: "b:channel:container_events/container.#", Type: graph.NodeTypeChannel, Service: "svc-b",
			Meta: map[string]string{"exchange": "container_events", "routing_key": "container.#"}},
	}
	res := runKind(t, contract.KindAMQP, nodes)

	var edges []graph.Edge
	for _, e := range res.Edges {
		if e.From == "a:channel:container_events/" {
			edges = append(edges, e)
		}
	}
	require.Len(t, edges, 1, "exactly one edge — no earlier tier may also fire")
	assert.Equal(t, "b:channel:container_events/container.#", edges[0].To)
	assert.Equal(t, graph.ConfidencePartial, edges[0].Confidence)
	assert.Equal(t, graph.ConfidencePartial, edges[0].Meta["confidence"])
}

// J.1 negative: exchange_only must not join two concrete, differing routing keys
// on the same exchange — that is two topics, not one rendezvous.
func TestAMQPExchangeOnlyTier_ConcreteKeysBothSides_NoEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:channel:orders/placed", Type: graph.NodeTypeChannel, Service: "svc-a",
			Meta: map[string]string{"exchange": "orders", "routing_key": "placed"}},
		{ID: "b:channel:orders/shipped", Type: graph.NodeTypeChannel, Service: "svc-b",
			Meta: map[string]string{"exchange": "orders", "routing_key": "shipped"}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	assert.Empty(t, res.Edges, "exchange_only must not collapse distinct topics")
}

// ── Hub ───────────────────────────────────────────────────────────────────────

// Positive: hub_broadcast_call producer links to hub_subscribe_call consumer
// within the same service.
func TestHubRule_SameService_Fanout(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:pub", Type: graph.NodeTypePublisher, Service: "svc",
			Meta: map[string]string{"pattern": "hub_broadcast_call"}},
		{ID: "svc:sub", Type: graph.NodeTypeSubscriber, Service: "svc",
			Meta: map[string]string{"pattern": "hub_subscribe_call"}},
	}
	res := runKind(t, contract.KindHub, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "hub:svc:pub->svc:sub", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypeHubBroadcast, res.Edges[0].Type)
	assert.Equal(t, graph.ConfidenceInferred, res.Edges[0].Confidence)
}

// Negative: hub subscribe in a different service must not link (same_service_only).
func TestHubRule_CrossService_NoEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc-a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "hub_broadcast_call"}},
		{ID: "svc-b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "hub_subscribe_call"}},
	}
	res := runKind(t, contract.KindHub, nodes)
	assert.Empty(t, res.Edges, "hub fanout must not cross service boundaries")
}

// Negative: a node with the wrong pattern is not a hub candidate.
func TestHubRule_WrongPattern_NoEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:pub", Type: graph.NodeTypePublisher, Service: "svc",
			Meta: map[string]string{"pattern": "amqp_publish"}}, // not a hub broadcast
		{ID: "svc:sub", Type: graph.NodeTypeSubscriber, Service: "svc",
			Meta: map[string]string{"pattern": "hub_subscribe_call"}},
	}
	res := runKind(t, contract.KindHub, nodes)
	assert.Empty(t, res.Edges, "non-hub producer must not match hub subscriber")
}

// ── Jobs ─────────────────────────────────────────────────────────────────────

// Positive: perform_later enqueue links to the job class's perform method.
func TestJobsRule_PerformLater_LinksToPerform(t *testing.T) {
	nodes := []graph.Node{
		{ID: "app:pub", Type: graph.NodeTypePublisher, Service: "app",
			Meta: map[string]string{"pattern": "aj_perform_later", "job_class": "ReportJob"}},
		{ID: "app:sub", Type: graph.NodeTypeSubscriber, Service: "app",
			Meta: map[string]string{"pattern": "aj_perform_method", "job_class": "ReportJob"}},
	}
	res := runKind(t, contract.KindJob, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "job:app:pub->app:sub", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypeJobEnqueue, res.Edges[0].Type)
}

// Positive: quoted job_class is normalised by quote_strip.
func TestJobsRule_QuotedJobClass_Normalised(t *testing.T) {
	nodes := []graph.Node{
		{ID: "app:pub", Type: graph.NodeTypePublisher, Service: "app",
			Meta: map[string]string{"pattern": "aj_perform_later", "job_class": `"ExportJob"`}},
		{ID: "app:sub", Type: graph.NodeTypeSubscriber, Service: "app",
			Meta: map[string]string{"pattern": "aj_perform_method", "job_class": "ExportJob"}},
	}
	res := runKind(t, contract.KindJob, nodes)
	require.Len(t, res.Edges, 1)
}

// Negative: different job class must not match.
func TestJobsRule_DifferentClass_NoEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "app:pub", Type: graph.NodeTypePublisher, Service: "app",
			Meta: map[string]string{"pattern": "aj_perform_later", "job_class": "ReportJob"}},
		{ID: "app:sub", Type: graph.NodeTypeSubscriber, Service: "app",
			Meta: map[string]string{"pattern": "aj_perform_method", "job_class": "OtherJob"}},
	}
	res := runKind(t, contract.KindJob, nodes)
	assert.Empty(t, res.Edges, "different job class must not match")
	require.Len(t, res.Unresolved, 1, "unmatched enqueue must be surfaced in the ledger")
	assert.Equal(t, "job", res.Unresolved[0].Kind)
}

// Negative: non-job publisher (wrong pattern) must not appear as a job producer.
func TestJobsRule_WrongPattern_NoEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "app:pub", Type: graph.NodeTypePublisher, Service: "app",
			Meta: map[string]string{"pattern": "pusher_trigger", "job_class": "ReportJob"}},
		{ID: "app:sub", Type: graph.NodeTypeSubscriber, Service: "app",
			Meta: map[string]string{"pattern": "aj_perform_method", "job_class": "ReportJob"}},
	}
	res := runKind(t, contract.KindJob, nodes)
	assert.Empty(t, res.Edges, "pusher_trigger publisher must not produce a job edge")
}

// X.2: delayed_job .delay method-wrapping — dj_target join keys a `function`
// consumer by qualified_name (<Type>#<method>), not a job class.

// Positive: user.delay.deliver_email links to User#deliver_email.
func TestJobsRule_DelayedJobDelay_LinksToMethod(t *testing.T) {
	nodes := []graph.Node{
		{ID: "app:pub", Type: graph.NodeTypePublisher, Service: "app",
			Meta: map[string]string{"pattern": "dj_delay", "dj_target": "User#deliver_email"}},
		{ID: "app:sub", Type: graph.NodeTypeFunction, Service: "app",
			Meta: map[string]string{"qualified_name": "User#deliver_email"}},
	}
	res := runKind(t, contract.KindJob, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "job:app:pub->app:sub", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypeJobEnqueue, res.Edges[0].Type)
}

// Positive: handle_asynchronously :rebuild on Group links to Group#rebuild
// as job_perform (declaration, not an enqueue site).
func TestJobsRule_HandleAsynchronously_LinksToMethod(t *testing.T) {
	nodes := []graph.Node{
		{ID: "app:pub", Type: graph.NodeTypePublisher, Service: "app",
			Meta: map[string]string{"pattern": "dj_handle_asynchronously", "dj_target": "Group#rebuild"}},
		{ID: "app:sub", Type: graph.NodeTypeFunction, Service: "app",
			Meta: map[string]string{"qualified_name": "Group#rebuild"}},
	}
	res := runKind(t, contract.KindJob, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, graph.EdgeTypeJobPerform, res.Edges[0].Type)
}

// Negative: a .delay call whose receiver type could not be honestly resolved
// (matcher.go leaves dj_target unset) must not guess a join key — it ledgers
// instead of matching whichever function happens to also lack qualified_name.
func TestJobsRule_DelayedJob_UnresolvedReceiver_Ledgers(t *testing.T) {
	nodes := []graph.Node{
		{ID: "app:pub", Type: graph.NodeTypePublisher, Service: "app",
			Meta: map[string]string{"pattern": "dj_delay"}}, // no dj_target: unwrapped_job
		{ID: "app:unrelated-fn", Type: graph.NodeTypeFunction, Service: "app",
			Meta: map[string]string{}}, // e.g. a JS/Go function with no qualified_name either
	}
	res := runKind(t, contract.KindJob, nodes)
	assert.Empty(t, res.Edges, "an unresolved receiver must never guess-join an unrelated empty-keyed function")
	require.Len(t, res.Unresolved, 1, "unresolved .delay receiver must reach the ledger")
	assert.Equal(t, "job", res.Unresolved[0].Kind)
}

// Fan-out (bug-class #1): Ruby classes can be reopened across files, so two
// distinct method nodes can share one qualified_name. Both must link.
func TestJobsRule_DelayedJob_ReopenedClass_FanOut(t *testing.T) {
	nodes := []graph.Node{
		{ID: "app:pub", Type: graph.NodeTypePublisher, Service: "app",
			Meta: map[string]string{"pattern": "dj_delay", "dj_target": "User#deliver_email"}},
		{ID: "app:user.rb:sub1", Type: graph.NodeTypeFunction, Service: "app",
			Meta: map[string]string{"qualified_name": "User#deliver_email"}},
		{ID: "app:user_ext.rb:sub2", Type: graph.NodeTypeFunction, Service: "app",
			Meta: map[string]string{"qualified_name": "User#deliver_email"}},
	}
	res := runKind(t, contract.KindJob, nodes)
	require.Len(t, res.Edges, 2, "both reopened-class method definitions must be linked, not just the first")
}

// Determinism (bug-class #2): two runs over the same input produce the same
// edge set and order.
func TestJobsRule_DelayedJob_Deterministic(t *testing.T) {
	nodes := []graph.Node{
		{ID: "app:pub", Type: graph.NodeTypePublisher, Service: "app",
			Meta: map[string]string{"pattern": "dj_delay", "dj_target": "User#deliver_email"}},
		{ID: "app:user.rb:sub1", Type: graph.NodeTypeFunction, Service: "app",
			Meta: map[string]string{"qualified_name": "User#deliver_email"}},
		{ID: "app:user_ext.rb:sub2", Type: graph.NodeTypeFunction, Service: "app",
			Meta: map[string]string{"qualified_name": "User#deliver_email"}},
	}
	rules := rulesOfKind(t, contract.KindJob)
	e := &contract.Engine{}
	first := e.Link(nodes, rules, nil)
	require.Len(t, first.Edges, 2)
	for i := 0; i < 20; i++ {
		again := e.Link(nodes, rules, nil)
		require.Equal(t, edgeIDs(first.Edges), edgeIDs(again.Edges),
			"edge set and order must be stable across runs")
	}
}

// ── Pusher ────────────────────────────────────────────────────────────────────

// Positive: server pusher_trigger links to pusher_subscribe_client by channel.
func TestPusherRule_TriggerToSubscribe(t *testing.T) {
	nodes := []graph.Node{
		{ID: "rails:pub", Type: graph.NodeTypePublisher, Service: "rails",
			Meta: map[string]string{"pattern": "pusher_trigger", "channel": "'orders'"}},
		{ID: "web:sub", Type: graph.NodeTypeSubscriber, Service: "web",
			Meta: map[string]string{"pattern": "pusher_subscribe_client", "channel": "'orders'"}},
	}
	res := runKind(t, contract.KindPusher, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "pusher:rails:pub->web:sub", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypePusherTrigger, res.Edges[0].Type)
}

// Positive: pusher_trigger_async variant also matches.
func TestPusherRule_TriggerAsync_LinksToSubscribe(t *testing.T) {
	nodes := []graph.Node{
		{ID: "rails:pub", Type: graph.NodeTypePublisher, Service: "rails",
			Meta: map[string]string{"pattern": "pusher_trigger_async", "channel": "'users'"}},
		{ID: "web:sub", Type: graph.NodeTypeSubscriber, Service: "web",
			Meta: map[string]string{"pattern": "pusher_subscribe_client", "channel": "'users'"}},
	}
	res := runKind(t, contract.KindPusher, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, graph.EdgeTypePusherTrigger, res.Edges[0].Type)
}

// Positive: quote_strip normalises quoted channel names.
func TestPusherRule_QuoteStrip(t *testing.T) {
	nodes := []graph.Node{
		{ID: "rails:pub", Type: graph.NodeTypePublisher, Service: "rails",
			Meta: map[string]string{"pattern": "pusher_trigger", "channel": `"channel-x"`}},
		{ID: "web:sub", Type: graph.NodeTypeSubscriber, Service: "web",
			Meta: map[string]string{"pattern": "pusher_subscribe_client", "channel": "channel-x"}},
	}
	res := runKind(t, contract.KindPusher, nodes)
	require.Len(t, res.Edges, 1)
}

// Negative: variable-held channel (no quotes) finds no consumer → ledger.
func TestPusherRule_VariableChannel_Ledger(t *testing.T) {
	nodes := []graph.Node{
		{ID: "rails:pub", Type: graph.NodeTypePublisher, Service: "rails",
			Meta: map[string]string{"pattern": "pusher_trigger", "channel": "channel_name"}},
	}
	res := runKind(t, contract.KindPusher, nodes)
	assert.Empty(t, res.Edges)
	require.Len(t, res.Unresolved, 1, "variable channel must surface in ledger")
}

// Negative: different channel names must not match.
func TestPusherRule_DifferentChannel_NoEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "rails:pub", Type: graph.NodeTypePublisher, Service: "rails",
			Meta: map[string]string{"pattern": "pusher_trigger", "channel": "'orders'"}},
		{ID: "web:sub", Type: graph.NodeTypeSubscriber, Service: "web",
			Meta: map[string]string{"pattern": "pusher_subscribe_client", "channel": "'users'"}},
	}
	res := runKind(t, contract.KindPusher, nodes)
	assert.Empty(t, res.Edges)
}

// PU.4 positive: a PU.2 forward producer joins a PU.3 ERB consumer on the
// [channel, event] pair, same service.
func TestPusherRule_ForwardToERB_ChannelEvent(t *testing.T) {
	nodes := []graph.Node{
		{ID: "ng:pub", Type: graph.NodeTypePublisher, Service: "orion",
			Meta: map[string]string{"pattern": "pusher_trigger_forward", "channel": "folder-status", "event": "folder_refresh"}},
		{ID: "ng:sub", Type: graph.NodeTypeSubscriber, Service: "orion",
			Meta: map[string]string{"pattern": "pusher_subscribe_erb", "channel": "folder-status", "event": "folder_refresh"}},
	}
	res := runKind(t, contract.KindPusher, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "pusher:ng:pub->ng:sub", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypePusherTrigger, res.Edges[0].Type)
}

// PU.4 negative: same channel, different event → no edge (two views on one
// channel listening for different events are different consumers).
func TestPusherRule_ForwardToERB_EventMismatch(t *testing.T) {
	nodes := []graph.Node{
		{ID: "ng:pub", Type: graph.NodeTypePublisher, Service: "orion",
			Meta: map[string]string{"pattern": "pusher_trigger_forward", "channel": "lyra_progress", "event": "job_complete"}},
		{ID: "ng:sub", Type: graph.NodeTypeSubscriber, Service: "orion",
			Meta: map[string]string{"pattern": "pusher_subscribe_erb", "channel": "lyra_progress", "event": "batch_refreshed"}},
	}
	res := runKind(t, contract.KindPusher, nodes)
	assert.Empty(t, res.Edges)
}

// PU.4 positive: the render-partial path keys on channel alone, so every
// producer on that channel reaches the channel-only ERB subscriber.
func TestPusherRule_ForwardToERB_ChannelOnly(t *testing.T) {
	nodes := []graph.Node{
		{ID: "ng:pub", Type: graph.NodeTypePublisher, Service: "orion",
			Meta: map[string]string{"pattern": "pusher_trigger_forward", "channel": "job-logs", "event": "job_logs"}},
		{ID: "ng:sub", Type: graph.NodeTypeSubscriber, Service: "orion",
			Meta: map[string]string{"pattern": "pusher_subscribe_erb_channel", "channel": "job-logs"}},
	}
	res := runKind(t, contract.KindPusher, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, graph.EdgeTypePusherTrigger, res.Edges[0].Type)
}

// ── WebSocket ─────────────────────────────────────────────────────────────────

// Positive: typed sender links to the matching dispatch case by message_type.
func TestWebSocketRule_TypedDispatch(t *testing.T) {
	nodes := []graph.Node{
		{ID: "client:send", Type: graph.NodeTypePublisher, Service: "tether-client",
			Meta: map[string]string{"pattern": "ws_send_typed", "message_type": "'battery'"}},
		{ID: "server:case", Type: graph.NodeTypeSubscriber, Service: "tether-server",
			Meta: map[string]string{"pattern": "ws_dispatch_case", "message_type": "'battery'"}},
		{ID: "server:other", Type: graph.NodeTypeSubscriber, Service: "tether-server",
			Meta: map[string]string{"pattern": "ws_dispatch_case", "message_type": "'location'"}},
	}
	res := runKind(t, contract.KindWebSocket, nodes)
	require.Len(t, res.Edges, 1, "only the matching message type links")
	assert.Equal(t, "ws:client:send->server:case", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypeWSSend, res.Edges[0].Type)
}

// Positive: Go server sends linked to JS client dispatch (cross-service).
func TestWebSocketRule_GoServerToJSClient(t *testing.T) {
	nodes := []graph.Node{
		{ID: "server:send", Type: graph.NodeTypePublisher, Service: "server",
			Meta: map[string]string{"pattern": "ws_send_typed", "message_type": `"battery_ack"`}},
		{ID: "client:case", Type: graph.NodeTypeSubscriber, Service: "client",
			Meta: map[string]string{"pattern": "ws_dispatch_case", "message_type": "battery_ack"}},
	}
	res := runKind(t, contract.KindWebSocket, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, graph.EdgeTypeWSSend, res.Edges[0].Type)
}

// Negative: different message types must not match.
func TestWebSocketRule_DifferentType_Ledger(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c:send", Type: graph.NodeTypePublisher, Service: "c",
			Meta: map[string]string{"pattern": "ws_send_typed", "message_type": "'battery'"}},
		{ID: "s:case", Type: graph.NodeTypeSubscriber, Service: "s",
			Meta: map[string]string{"pattern": "ws_dispatch_case", "message_type": "'location'"}},
	}
	res := runKind(t, contract.KindWebSocket, nodes)
	assert.Empty(t, res.Edges)
	require.Len(t, res.Unresolved, 1, "unmatched typed send must be surfaced in the ledger")
}

// PW.1: connect-time join for route-style WS servers. Positive — a JS
// client's ws_new_connection matches a Go gorilla ws_upgrade node once it
// carries a path (post-linker.LinkWSUpgradeRoute state).
func TestWebSocketRule_ConnectRouteStyle_Go(t *testing.T) {
	nodes := []graph.Node{
		{ID: "client:connect", Type: graph.NodeTypeHTTPClient, Service: "svc-client",
			Meta: map[string]string{"pattern": "ws_new_connection", "path": "/notifications"}},
		{ID: "server:upgrade", Type: graph.NodeTypeHTTPHandler, Service: "svc-go",
			Meta: map[string]string{"pattern": "ws_upgrade", "path": "/notifications", "method": "GET"}},
	}
	res := runKind(t, contract.KindWebSocket, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, graph.EdgeTypeWSConnect, res.Edges[0].Type)
	assert.Equal(t, "server:upgrade", res.Edges[0].To)
}

// PW.1: same rule, Python FastAPI shape — ws_upgrade_fastapi captures its
// own path directly, no linker stamping needed.
func TestWebSocketRule_ConnectRouteStyle_Python(t *testing.T) {
	nodes := []graph.Node{
		{ID: "client:connect", Type: graph.NodeTypeHTTPClient, Service: "svc-client",
			Meta: map[string]string{"pattern": "ws_new_connection", "path": "/updates"}},
		{ID: "server:ws", Type: graph.NodeTypeHTTPHandler, Service: "svc-py",
			Meta: map[string]string{"pattern": "ws_upgrade_fastapi", "path": "/updates", "method": "GET"}},
	}
	res := runKind(t, contract.KindWebSocket, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, graph.EdgeTypeWSConnect, res.Edges[0].Type)
	assert.Equal(t, "server:ws", res.Edges[0].To)
}

// Negative: a plain HTTP handler at the same path (not ws_upgrade*) must
// never be joined by the connect-time rule (PW.1 gate 4).
func TestWebSocketRule_ConnectRouteStyle_PlainHandlerNotMatched(t *testing.T) {
	nodes := []graph.Node{
		{ID: "client:connect", Type: graph.NodeTypeHTTPClient, Service: "svc-client",
			Meta: map[string]string{"pattern": "ws_new_connection", "path": "/health"}},
		{ID: "server:health", Type: graph.NodeTypeHTTPHandler, Service: "svc-go",
			Meta: map[string]string{"pattern": "gin_route", "path": "/health", "method": "GET"}},
	}
	res := runKind(t, contract.KindWebSocket, nodes)
	assert.Empty(t, res.Edges)
}

// Negative: non-ws_send_typed publisher must not be a websocket producer.
func TestWebSocketRule_WrongProducerPattern_NoEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "c:pub", Type: graph.NodeTypePublisher, Service: "c",
			Meta: map[string]string{"pattern": "hub_broadcast_call", "message_type": "'battery'"}},
		{ID: "s:sub", Type: graph.NodeTypeSubscriber, Service: "s",
			Meta: map[string]string{"pattern": "ws_dispatch_case", "message_type": "'battery'"}},
	}
	res := runKind(t, contract.KindWebSocket, nodes)
	assert.Empty(t, res.Edges)
}

// ── D.4: channel role suppresses backwards publishes edges ──────────────────

// A channel node whose only evidence is a QueueBind is the consuming side, so
// the exchange contract must not treat it as a producer. Both endpoints of that
// rule are `channel`, so before the channel_role gate the join was symmetric
// and emitted an edge in each direction for every shared exchange — half of
// them pointing backwards along the message flow.
func TestAMQPRule_ConsumerOnlyChannelIsNotAProducer(t *testing.T) {
	nodes := []graph.Node{
		// svc-a publishes.
		{ID: "svc-a:channel:runner_heartbeat/heartbeat", Type: graph.NodeTypeChannel, Service: "svc-a",
			Meta: map[string]string{
				"exchange": "runner_heartbeat", "routing_key": "heartbeat",
				graph.MetaChannelRole: graph.ChannelRoleProducer,
			}},
		// svc-b only binds a queue to it.
		{ID: "svc-b:channel:runner_heartbeat/heartbeat", Type: graph.NodeTypeChannel, Service: "svc-b",
			Meta: map[string]string{
				"exchange": "runner_heartbeat", "routing_key": "heartbeat",
				graph.MetaChannelRole: graph.ChannelRoleConsumer,
			}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	var got []string
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypePublishes {
			got = append(got, e.From+"->"+e.To)
		}
	}
	require.Len(t, got, 1, "exactly one direction may be emitted, got %v", got)
	assert.Equal(t,
		"svc-a:channel:runner_heartbeat/heartbeat->svc-b:channel:runner_heartbeat/heartbeat",
		got[0], "the edge must run publisher → binder")
}

// A service that publishes to an exchange AND binds a queue to it is genuinely
// on both sides, and must stay eligible as a producer. The exclusion is an
// exact-value match on "consumer" precisely so that "producer,consumer" passes.
func TestAMQPRule_BothRolesStillProduces(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc-a:channel:events/x", Type: graph.NodeTypeChannel, Service: "svc-a",
			Meta: map[string]string{
				"exchange": "events", "routing_key": "x",
				graph.MetaChannelRole: graph.ChannelRoleBoth,
			}},
		{ID: "svc-b:channel:events/x", Type: graph.NodeTypeChannel, Service: "svc-b",
			Meta: map[string]string{
				"exchange": "events", "routing_key": "x",
				graph.MetaChannelRole: graph.ChannelRoleConsumer,
			}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	var got []string
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypePublishes {
			got = append(got, e.From+"->"+e.To)
		}
	}
	require.Len(t, got, 1)
	assert.Equal(t, "svc-a:channel:events/x->svc-b:channel:events/x", got[0])
}

// An unclassified channel keeps its old behaviour. Absent means "we could not
// tell", not "consumer" — a language whose patterns do not yet record the role
// must not lose its cross-service edges to this gate.
func TestAMQPRule_UnclassifiedChannelStillLinks(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc-a:channel:orders/placed", Type: graph.NodeTypeChannel, Service: "svc-a",
			Meta: map[string]string{"exchange": "orders", "routing_key": "placed"}},
		{ID: "svc-b:channel:orders/placed", Type: graph.NodeTypeChannel, Service: "svc-b",
			Meta: map[string]string{"exchange": "orders", "routing_key": "placed"}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	var n int
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypePublishes {
			n++
		}
	}
	assert.Greater(t, n, 0, "a role-less channel pair must still link")
}

// Two pure consumers of the same exchange are two subscribers to one publisher
// elsewhere, not a link between each other.
func TestAMQPRule_TwoConsumersDoNotLink(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc-a:channel:events/x", Type: graph.NodeTypeChannel, Service: "svc-a",
			Meta: map[string]string{
				"exchange": "events", "routing_key": "x",
				graph.MetaChannelRole: graph.ChannelRoleConsumer,
			}},
		{ID: "svc-b:channel:events/x", Type: graph.NodeTypeChannel, Service: "svc-b",
			Meta: map[string]string{
				"exchange": "events", "routing_key": "x",
				graph.MetaChannelRole: graph.ChannelRoleConsumer,
			}},
	}
	res := runKind(t, contract.KindAMQP, nodes)
	for _, e := range res.Edges {
		assert.NotEqual(t, graph.EdgeTypePublishes, e.Type,
			"two binders of one exchange must not publish to each other")
	}
}
