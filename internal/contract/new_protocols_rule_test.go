package contract_test

// Tests for the G.4 protocol contract rules: kafka, nats, redis_pubsub, grpc, graphql.
// Each kind gets a cross-service positive, a same-service negative, a
// different-key negative, and (where the unmatched policy is ledger/unknown_edge)
// an unmatched-producer test.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ── Kafka ─────────────────────────────────────────────────────────────────────

// Positive: publisher and subscriber on the same topic in different services
// produce a cross-service kafka_publish edge.
func TestKafkaRule_CrossServiceMatch(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc-a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "kafka_publish", "topic": "orders"}},
		{ID: "svc-b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "kafka_subscribe", "topic": "orders"}},
	}
	res := runKind(t, contract.KindKafka, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "kafka:svc-a:pub->svc-b:sub", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypeKafkaPublish, res.Edges[0].Type)
	assert.Equal(t, graph.ConfidenceStatic, res.Edges[0].Confidence)
}

// Positive: quoted topic is normalised by quote_strip.
func TestKafkaRule_QuotedTopic_Normalised(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "kafka_publish", "topic": `"orders"`}},
		{ID: "b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "kafka_subscribe", "topic": "orders"}},
	}
	res := runKind(t, contract.KindKafka, nodes)
	require.Len(t, res.Edges, 1, "quote_strip must allow quoted topic to match unquoted")
	assert.Equal(t, graph.EdgeTypeKafkaPublish, res.Edges[0].Type)
}

// Negative: same-service kafka nodes must not link (skip policy).
func TestKafkaRule_SameService_NoCrossEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:pub", Type: graph.NodeTypePublisher, Service: "svc",
			Meta: map[string]string{"pattern": "kafka_publish", "topic": "orders"}},
		{ID: "svc:sub", Type: graph.NodeTypeSubscriber, Service: "svc",
			Meta: map[string]string{"pattern": "kafka_subscribe", "topic": "orders"}},
	}
	res := runKind(t, contract.KindKafka, nodes)
	assert.Empty(t, res.Edges, "same-service kafka nodes must not link")
}

// Negative: different topics must not match; unmatched publisher → ledger.
func TestKafkaRule_DifferentTopic_Ledger(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "kafka_publish", "topic": "orders"}},
		{ID: "b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "kafka_subscribe", "topic": "payments"}},
	}
	res := runKind(t, contract.KindKafka, nodes)
	assert.Empty(t, res.Edges, "different topics must not produce an edge")
	require.Len(t, res.Unresolved, 1, "unmatched kafka producer must surface in the ledger")
	assert.Equal(t, "kafka", res.Unresolved[0].Kind)
}

// Negative: AMQP publisher must not match the kafka consumer (wrong pattern gate).
func TestKafkaRule_WrongPattern_NoEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "amqp_publish", "topic": "orders"}},
		{ID: "b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "kafka_subscribe", "topic": "orders"}},
	}
	res := runKind(t, contract.KindKafka, nodes)
	assert.Empty(t, res.Edges, "amqp_publish must not match kafka contract producer gate")
}

// ── NATS ──────────────────────────────────────────────────────────────────────

// Positive: publisher and subscriber on the same subject in different services.
func TestNATSRule_CrossServiceMatch(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc-a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "nats_publish", "subject": "orders.created"}},
		{ID: "svc-b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "nats_subscribe", "subject": "orders.created"}},
	}
	res := runKind(t, contract.KindNATS, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "nats:svc-a:pub->svc-b:sub", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypeNATSPublish, res.Edges[0].Type)
}

// Positive: quoted subject normalised by quote_strip.
func TestNATSRule_QuotedSubject_Normalised(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "nats_publish", "subject": `"orders.created"`}},
		{ID: "b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "nats_subscribe", "subject": "orders.created"}},
	}
	res := runKind(t, contract.KindNATS, nodes)
	require.Len(t, res.Edges, 1)
}

// Negative: same-service NATS nodes must not link.
func TestNATSRule_SameService_NoCrossEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:pub", Type: graph.NodeTypePublisher, Service: "svc",
			Meta: map[string]string{"pattern": "nats_publish", "subject": "orders.created"}},
		{ID: "svc:sub", Type: graph.NodeTypeSubscriber, Service: "svc",
			Meta: map[string]string{"pattern": "nats_subscribe", "subject": "orders.created"}},
	}
	res := runKind(t, contract.KindNATS, nodes)
	assert.Empty(t, res.Edges)
}

// Negative: different subjects → ledger.
func TestNATSRule_DifferentSubject_Ledger(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "nats_publish", "subject": "orders.created"}},
		{ID: "b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "nats_subscribe", "subject": "payments.processed"}},
	}
	res := runKind(t, contract.KindNATS, nodes)
	assert.Empty(t, res.Edges)
	require.Len(t, res.Unresolved, 1)
	assert.Equal(t, "nats", res.Unresolved[0].Kind)
}

// ── Redis pub/sub ─────────────────────────────────────────────────────────────

// Positive: publisher and subscriber on the same channel in different services.
func TestRedisRule_CrossServiceMatch(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc-a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "redis_publish", "channel": "notifications"}},
		{ID: "svc-b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "redis_subscribe", "channel": "notifications"}},
	}
	res := runKind(t, contract.KindRedisPubSub, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "redis:svc-a:pub->svc-b:sub", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypeRedisPublish, res.Edges[0].Type)
}

// Positive: quoted channel normalised.
func TestRedisRule_QuotedChannel_Normalised(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "redis_publish", "channel": `"notifications"`}},
		{ID: "b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "redis_subscribe", "channel": "notifications"}},
	}
	res := runKind(t, contract.KindRedisPubSub, nodes)
	require.Len(t, res.Edges, 1)
}

// Negative: same-service redis nodes must not link.
func TestRedisRule_SameService_NoCrossEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:pub", Type: graph.NodeTypePublisher, Service: "svc",
			Meta: map[string]string{"pattern": "redis_publish", "channel": "notifications"}},
		{ID: "svc:sub", Type: graph.NodeTypeSubscriber, Service: "svc",
			Meta: map[string]string{"pattern": "redis_subscribe", "channel": "notifications"}},
	}
	res := runKind(t, contract.KindRedisPubSub, nodes)
	assert.Empty(t, res.Edges)
}

// Negative: different channel → ledger.
func TestRedisRule_DifferentChannel_Ledger(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "redis_publish", "channel": "alerts"}},
		{ID: "b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "redis_subscribe", "channel": "notifications"}},
	}
	res := runKind(t, contract.KindRedisPubSub, nodes)
	assert.Empty(t, res.Edges)
	require.Len(t, res.Unresolved, 1)
	assert.Equal(t, "redis_pubsub", res.Unresolved[0].Kind)
}

// ── gRPC ──────────────────────────────────────────────────────────────────────

// Positive: grpc_client node in svc-a links to grpc_handler in svc-b by
// service_method key.
func TestGRPCRule_CrossServiceMatch(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc-a:client", Type: graph.NodeTypeGRPCClient, Service: "svc-a",
			Meta: map[string]string{"service_method": "/UserService/GetUser"}},
		{ID: "svc-b:handler", Type: graph.NodeTypeGRPCHandler, Service: "svc-b",
			Meta: map[string]string{"service_method": "/UserService/GetUser"}},
	}
	res := runKind(t, contract.KindGRPC, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "grpc:svc-a:client->svc-b:handler", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypeGRPCCall, res.Edges[0].Type)
	assert.Equal(t, graph.ConfidenceStatic, res.Edges[0].Confidence)
}

// Positive: quoted service_method normalised by quote_strip.
func TestGRPCRule_QuotedServiceMethod_Normalised(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:client", Type: graph.NodeTypeGRPCClient, Service: "svc-a",
			Meta: map[string]string{"service_method": `"/UserService/GetUser"`}},
		{ID: "b:handler", Type: graph.NodeTypeGRPCHandler, Service: "svc-b",
			Meta: map[string]string{"service_method": "/UserService/GetUser"}},
	}
	res := runKind(t, contract.KindGRPC, nodes)
	require.Len(t, res.Edges, 1)
}

// Negative: same-service gRPC handler must never be the edge target.
// The same_service:skip policy filters out the same-service handler;
// the unknown_edge policy then fires (as with HTTP calls), emitting an
// unknown-confidence edge to the synthetic "unresolved" node — but NOT
// an edge directly to the same-service handler.
func TestGRPCRule_SameService_NoDirectEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:client", Type: graph.NodeTypeGRPCClient, Service: "svc",
			Meta: map[string]string{"service_method": "/UserService/GetUser"}},
		{ID: "svc:handler", Type: graph.NodeTypeGRPCHandler, Service: "svc",
			Meta: map[string]string{"service_method": "/UserService/GetUser"}},
	}
	res := runKind(t, contract.KindGRPC, nodes)
	for _, e := range res.Edges {
		assert.NotEqual(t, "svc:handler", e.To,
			"same-service grpc handler must never be an edge target; got %+v", e)
	}
}

// Negative: different service_method → unknown_edge to synthetic unresolved node
// (grpc uses unknown_edge policy so dangling calls appear in impact traversal).
func TestGRPCRule_DifferentMethod_UnknownEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:client", Type: graph.NodeTypeGRPCClient, Service: "svc-a",
			Meta: map[string]string{"service_method": "/UserService/GetUser"}},
		{ID: "b:handler", Type: graph.NodeTypeGRPCHandler, Service: "svc-b",
			Meta: map[string]string{"service_method": "/UserService/ListUsers"}},
	}
	res := runKind(t, contract.KindGRPC, nodes)
	// Unmatched client → unknown_edge to synthetic unresolved node.
	unknownEdges := 0
	for _, e := range res.Edges {
		if e.Confidence == graph.ConfidenceUnknown {
			unknownEdges++
		}
	}
	require.Greater(t, unknownEdges, 0, "unmatched grpc client must emit an unknown_edge")
	assert.NotEmpty(t, res.Nodes, "synthetic unresolved node must be created")
	assert.Empty(t, res.Unresolved, "grpc uses unknown_edge policy, not ledger")
}

// ── GraphQL ───────────────────────────────────────────────────────────────────

// Positive: graphql_client node links to graphql_resolver by operation name.
func TestGraphQLRule_CrossServiceMatch(t *testing.T) {
	nodes := []graph.Node{
		{ID: "web:query", Type: graph.NodeTypeGraphQLClient, Service: "web",
			Meta: map[string]string{"operation": "books"}},
		{ID: "api:resolver", Type: graph.NodeTypeGraphQLResolver, Service: "api",
			Meta: map[string]string{"operation": "books"}},
	}
	res := runKind(t, contract.KindGraphQL, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "graphql:web:query->api:resolver", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypeGraphQLCall, res.Edges[0].Type)
}

// Positive: quoted operation normalised.
func TestGraphQLRule_QuotedOperation_Normalised(t *testing.T) {
	nodes := []graph.Node{
		{ID: "web:query", Type: graph.NodeTypeGraphQLClient, Service: "web",
			Meta: map[string]string{"operation": `"books"`}},
		{ID: "api:resolver", Type: graph.NodeTypeGraphQLResolver, Service: "api",
			Meta: map[string]string{"operation": "books"}},
	}
	res := runKind(t, contract.KindGraphQL, nodes)
	require.Len(t, res.Edges, 1)
}

// Negative: same-service graphql nodes must not link.
func TestGraphQLRule_SameService_NoCrossEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:query", Type: graph.NodeTypeGraphQLClient, Service: "svc",
			Meta: map[string]string{"operation": "books"}},
		{ID: "svc:resolver", Type: graph.NodeTypeGraphQLResolver, Service: "svc",
			Meta: map[string]string{"operation": "books"}},
	}
	res := runKind(t, contract.KindGraphQL, nodes)
	assert.Empty(t, res.Edges)
}

// Negative: different operation names → ledger.
func TestGraphQLRule_DifferentOperation_Ledger(t *testing.T) {
	nodes := []graph.Node{
		{ID: "web:query", Type: graph.NodeTypeGraphQLClient, Service: "web",
			Meta: map[string]string{"operation": "books"}},
		{ID: "api:resolver", Type: graph.NodeTypeGraphQLResolver, Service: "api",
			Meta: map[string]string{"operation": "authors"}},
	}
	res := runKind(t, contract.KindGraphQL, nodes)
	assert.Empty(t, res.Edges)
	require.Len(t, res.Unresolved, 1, "unmatched graphql client must surface in the ledger")
	assert.Equal(t, "graphql", res.Unresolved[0].Kind)
}

// Negative: wrong node type (http_client instead of graphql_client) must not match.
func TestGraphQLRule_WrongNodeType_NoEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "web:client", Type: graph.NodeTypeHTTPClient, Service: "web",
			Meta: map[string]string{"operation": "books"}},
		{ID: "api:resolver", Type: graph.NodeTypeGraphQLResolver, Service: "api",
			Meta: map[string]string{"operation": "books"}},
	}
	res := runKind(t, contract.KindGraphQL, nodes)
	assert.Empty(t, res.Edges, "http_client node must not match graphql contract producer gate")
}
