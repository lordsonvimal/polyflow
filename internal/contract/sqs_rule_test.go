package contract_test

// Tests for the PW.3 SQS contract rule: sqs_send_message_python and
// sqs_receive_message_python joined by queue_url, mirroring kafka's
// topic-keyed join in new_protocols_rule_test.go.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Positive: publisher and subscriber on the same queue_url in different
// services produce a cross-service publishes edge.
func TestSQSRule_CrossServiceMatch(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc-a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "sqs_send_message_python", "queue_url": "https://sqs.us-east-1.amazonaws.com/123/my-queue"}},
		{ID: "svc-b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "sqs_receive_message_python", "queue_url": "https://sqs.us-east-1.amazonaws.com/123/my-queue"}},
	}
	res := runKind(t, contract.KindSQS, nodes)
	require.Len(t, res.Edges, 1)
	assert.Equal(t, "sqs:svc-a:pub->svc-b:sub", res.Edges[0].ID)
	assert.Equal(t, graph.EdgeTypePublishes, res.Edges[0].Type)
	assert.Equal(t, graph.ConfidenceStatic, res.Edges[0].Confidence)
}

// Positive: quoted queue_url is normalised by quote_strip.
func TestSQSRule_QuotedQueueURL_Normalised(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "sqs_send_message_python", "queue_url": `"https://sqs.../my-queue"`}},
		{ID: "b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "sqs_receive_message_python", "queue_url": "https://sqs.../my-queue"}},
	}
	res := runKind(t, contract.KindSQS, nodes)
	require.Len(t, res.Edges, 1, "quote_strip must allow quoted queue_url to match unquoted")
	assert.Equal(t, graph.EdgeTypePublishes, res.Edges[0].Type)
}

// Positive: same-service producer/consumer still link (same_service: keep —
// a worker consuming its own service's queue is a real, common shape, unlike
// kafka/amqp's same-service skip).
func TestSQSRule_SameService_StillLinks(t *testing.T) {
	nodes := []graph.Node{
		{ID: "svc:pub", Type: graph.NodeTypePublisher, Service: "svc",
			Meta: map[string]string{"pattern": "sqs_send_message_python", "queue_url": "https://sqs.../my-queue"}},
		{ID: "svc:sub", Type: graph.NodeTypeSubscriber, Service: "svc",
			Meta: map[string]string{"pattern": "sqs_receive_message_python", "queue_url": "https://sqs.../my-queue"}},
	}
	res := runKind(t, contract.KindSQS, nodes)
	require.Len(t, res.Edges, 1, "same-service SQS producer/consumer must still link")
}

// Negative: different queue_url values must not match; unmatched producer → ledger.
func TestSQSRule_DifferentQueueURL_Ledger(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "sqs_send_message_python", "queue_url": "https://sqs.../orders"}},
		{ID: "b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "sqs_receive_message_python", "queue_url": "https://sqs.../payments"}},
	}
	res := runKind(t, contract.KindSQS, nodes)
	assert.Empty(t, res.Edges, "different queue_url values must not produce an edge")
	require.Len(t, res.Unresolved, 1, "unmatched sqs producer must surface in the ledger")
	assert.Equal(t, "sqs", res.Unresolved[0].Kind)
}

// Negative: an S3 external_service node must not match the SQS contract's
// producer gate — confirms the s3_/sqs_ classification split didn't blur the
// contract's pattern-name gate.
func TestSQSRule_WrongPattern_NoEdge(t *testing.T) {
	nodes := []graph.Node{
		{ID: "a:pub", Type: graph.NodeTypePublisher, Service: "svc-a",
			Meta: map[string]string{"pattern": "s3_operation_python", "queue_url": "https://sqs.../my-queue"}},
		{ID: "b:sub", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"pattern": "sqs_receive_message_python", "queue_url": "https://sqs.../my-queue"}},
	}
	res := runKind(t, contract.KindSQS, nodes)
	assert.Empty(t, res.Edges, "s3_operation_python must not match the sqs contract producer gate")
}
