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
