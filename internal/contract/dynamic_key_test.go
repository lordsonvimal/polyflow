package contract_test

// Tests for G.6: dynamic producer key fan-out (key_candidates) and
// dynamic surfacing (key_dynamic). All node inputs are created manually
// to isolate engine behaviour from pattern extraction.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// navRule returns a minimal HTTP nav-link rule (drop unmatched, keep same-service).
func navRule() contract.Rule {
	return contract.Rule{
		Kind: contract.KindHTTP,
		Producer: contract.EndpointSpec{
			Node:  graph.NodeTypeHTTPClient,
			Where: map[string]string{"nav_link": "true"},
			Key:   []string{"method", "path"},
		},
		Consumer: contract.EndpointSpec{
			Node: graph.NodeTypeHTTPHandler,
			Key:  []string{"method", "path"},
		},
		Normalizers: []string{"case_fold", "url_to_path", "query_strip", "param_wildcard", "trim_slash"},
		Match:       []contract.MatchTier{contract.TierExact, contract.TierNormalized},
		Edge: contract.EdgeSpec{
			Type:        graph.EdgeTypeNavigatesTo,
			IDPrefix:    "nav",
			SameService: "keep",
		},
		Unmatched: contract.UnmatchedDrop,
	}
}

// kafkaRule returns a minimal Kafka producer/consumer rule.
func kafkaRule() contract.Rule {
	return contract.Rule{
		Kind: contract.KindKafka,
		Producer: contract.EndpointSpec{
			Node: graph.NodeTypePublisher,
			Where: map[string]string{"kind": "kafka"},
			Key:   []string{"topic"},
		},
		Consumer: contract.EndpointSpec{
			Node: graph.NodeTypeSubscriber,
			Where: map[string]string{"kind": "kafka"},
			Key:   []string{"topic"},
		},
		Normalizers: []string{},
		Match:       []contract.MatchTier{contract.TierExact},
		Edge: contract.EdgeSpec{
			Type:        graph.EdgeTypeKafkaPublish,
			IDPrefix:    "kafka",
			SameService: "skip",
		},
		Unmatched: contract.UnmatchedLedger,
	}
}

func navClient(id, service, candidates string) graph.Node {
	return graph.Node{
		ID:      id,
		Type:    graph.NodeTypeHTTPClient,
		Service: service,
		Meta: map[string]string{
			"nav_link":       "true",
			"method":         "GET",
			"key_candidates": candidates,
		},
	}
}

func navClientDynamic(id, service string) graph.Node {
	return graph.Node{
		ID:      id,
		Type:    graph.NodeTypeHTTPClient,
		Service: service,
		Meta: map[string]string{
			"nav_link":         "true",
			"method":           "GET",
			"key_dynamic":      "true",
			"key_dynamic_raw":  "someVar",
		},
	}
}

func kafkaPublisher(id, service, candidates string) graph.Node {
	return graph.Node{
		ID:      id,
		Type:    graph.NodeTypePublisher,
		Service: service,
		Meta: map[string]string{
			"kind":           "kafka",
			"key_candidates": candidates,
		},
	}
}

func kafkaDynamic(id, service string) graph.Node {
	return graph.Node{
		ID:      id,
		Type:    graph.NodeTypePublisher,
		Service: service,
		Meta: map[string]string{
			"kind":            "kafka",
			"key_dynamic":     "true",
			"key_dynamic_raw": "computedTopic",
		},
	}
}

// ── key_candidates fan-out ───────────────────────────────────────────────────

func TestEngine_KeyCandidates_TwoEdges(t *testing.T) {
	// <a href={isAdmin ? "/admin" : "/dashboard"}> → two navigates_to edges
	nodes := []graph.Node{
		navClient("c1", "svc-a", `["/admin","/dashboard"]`),
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/admin"}},
		{ID: "h2", Type: graph.NodeTypeHTTPHandler, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/dashboard"}},
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{navRule()}, nil)

	require.Len(t, result.Edges, 2, "one edge per candidate")
	for _, edge := range result.Edges {
		assert.Equal(t, graph.ConfidenceInferred, edge.Confidence)
		assert.Equal(t, "branch_enum", edge.Meta["via"])
		assert.Equal(t, graph.EdgeTypeNavigatesTo, edge.Type)
	}
	// Each edge targets one of the two handlers
	targets := map[string]bool{result.Edges[0].To: true, result.Edges[1].To: true}
	assert.True(t, targets["h1"])
	assert.True(t, targets["h2"])
}

func TestEngine_KeyCandidates_OneMatch(t *testing.T) {
	// Only one of two candidates matches; one edge emitted, no unmatched
	nodes := []graph.Node{
		navClient("c1", "svc-a", `["/admin","/missing"]`),
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/admin"}},
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{navRule()}, nil)

	require.Len(t, result.Edges, 1)
	assert.Equal(t, "h1", result.Edges[0].To)
	assert.Equal(t, "branch_enum", result.Edges[0].Meta["via"])
	assert.Len(t, result.Unresolved, 0, "partial match does not fire unmatched")
}

func TestEngine_KeyCandidates_NoMatch_UnmatchedFires(t *testing.T) {
	// No candidate matches → unmatched policy (ledger for kafka)
	nodes := []graph.Node{
		kafkaPublisher("p1", "svc-a", `["orders.created","orders.updated"]`),
		{ID: "s1", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"kind": "kafka", "topic": "payments.created"}},
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{kafkaRule()}, nil)

	assert.Len(t, result.Edges, 0)
	require.Len(t, result.Unresolved, 1)
	assert.Equal(t, "kafka", result.Unresolved[0].Kind)
}

func TestEngine_KeyCandidates_ThreeTopics(t *testing.T) {
	// Go switch: three topic candidates → three kafka_publish edges
	cands := `["orders.created","orders.updated","orders.deleted"]`
	nodes := []graph.Node{
		kafkaPublisher("p1", "svc-a", cands),
		{ID: "s1", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"kind": "kafka", "topic": "orders.created"}},
		{ID: "s2", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"kind": "kafka", "topic": "orders.updated"}},
		{ID: "s3", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"kind": "kafka", "topic": "orders.deleted"}},
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{kafkaRule()}, nil)

	require.Len(t, result.Edges, 3)
	for _, edge := range result.Edges {
		assert.Equal(t, "branch_enum", edge.Meta["via"])
		assert.Equal(t, graph.ConfidenceInferred, edge.Confidence)
	}
}

// ── key_dynamic surfacing ────────────────────────────────────────────────────

func TestEngine_KeyDynamic_NavDropRefined(t *testing.T) {
	// Nav-link drop policy refined: dynamic nav links reach the ledger (not silently dropped)
	nodes := []graph.Node{
		navClientDynamic("c1", "svc-a"),
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/admin"}},
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{navRule()}, nil)

	assert.Len(t, result.Edges, 0, "dynamic nav link emits no edge")
	require.Len(t, result.Unresolved, 1, "dynamic nav link must reach ledger (not dropped)")
	assert.Equal(t, "dynamic_url", result.Unresolved[0].Kind)
	assert.Equal(t, "someVar", result.Unresolved[0].Name)
}

func TestEngine_KeyDynamic_KafkaLedgerKind(t *testing.T) {
	// Kafka dynamic topic → dynamic_topic ledger entry
	nodes := []graph.Node{
		kafkaDynamic("p1", "svc-a"),
		{ID: "s1", Type: graph.NodeTypeSubscriber, Service: "svc-b",
			Meta: map[string]string{"kind": "kafka", "topic": "orders.created"}},
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{kafkaRule()}, nil)

	assert.Len(t, result.Edges, 0)
	require.Len(t, result.Unresolved, 1)
	assert.Equal(t, "dynamic_topic", result.Unresolved[0].Kind)
	assert.Equal(t, "computedTopic", result.Unresolved[0].Name)
}

func TestEngine_KeyDynamic_LiteralNavStillDropped(t *testing.T) {
	// Regression: literal-unmatched nav links still drop (policy preserved)
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{
				"nav_link": "true",
				"method":   "GET",
				"path":     "/external-page", // no matching handler
			}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/admin"}},
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{navRule()}, nil)

	assert.Len(t, result.Edges, 0, "unmatched literal nav link is dropped")
	assert.Len(t, result.Unresolved, 0, "unmatched literal nav link is silently dropped")
}

// ── negative fixtures ────────────────────────────────────────────────────────

func TestEngine_KeyCandidates_EmptyArray(t *testing.T) {
	// Empty key_candidates → treated as no candidates (normal single-key path)
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{
				"nav_link":       "true",
				"method":         "GET",
				"path":           "/admin",
				"key_candidates": "[]",
			}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/admin"}},
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{navRule()}, nil)

	// ParseKeyCandidates("[]") returns empty slice → falls through to normal matching
	// The node has path=/admin so normal match fires.
	require.Len(t, result.Edges, 1)
}

func TestEngine_KeyCandidates_InvalidJSON(t *testing.T) {
	// Invalid key_candidates JSON → treated as no candidates
	nodes := []graph.Node{
		{ID: "c1", Type: graph.NodeTypeHTTPClient, Service: "svc-a",
			Meta: map[string]string{
				"nav_link":       "true",
				"method":         "GET",
				"path":           "/admin",
				"key_candidates": "not-json",
			}},
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc-a",
			Meta: map[string]string{"method": "GET", "path": "/admin"}},
	}
	e := &contract.Engine{}
	result := e.Link(nodes, []contract.Rule{navRule()}, nil)

	require.Len(t, result.Edges, 1, "falls back to normal matching with path=/admin")
}
