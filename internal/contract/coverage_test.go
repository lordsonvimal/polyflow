package contract_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestComputeCoverage_MatchedAndUnresolved(t *testing.T) {
	rules := []contract.Rule{
		{
			Kind: contract.KindHTTP,
			Edge: contract.EdgeSpec{Type: graph.EdgeTypeHTTPCall},
		},
		{
			Kind: "kafka",
			Edge: contract.EdgeSpec{Type: graph.EdgeTypeKafkaPublish},
		},
	}

	result := contract.Result{
		Edges: []graph.Edge{
			{Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceStatic},
			{Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceInferred},
			{Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceUnknown}, // unmatched unknown_edge
			{Type: graph.EdgeTypeKafkaPublish, Confidence: graph.ConfidenceStatic},
		},
		Unresolved: []graph.UnresolvedRef{
			{Kind: "kafka"},
			{Kind: "kafka"},
		},
	}

	cov := contract.ComputeCoverage(rules, result)

	assert.Len(t, cov, 2)
	httpCov := findCoverage(cov, "http")
	assert.Equal(t, 2, httpCov.Matched)    // static + inferred
	assert.Equal(t, 1, httpCov.Unresolved) // unknown_edge

	kafkaCov := findCoverage(cov, "kafka")
	assert.Equal(t, 1, kafkaCov.Matched)
	assert.Equal(t, 2, kafkaCov.Unresolved) // ledger entries
}

func TestComputeCoverage_AllMatched(t *testing.T) {
	rules := []contract.Rule{
		{Kind: "grpc", Edge: contract.EdgeSpec{Type: graph.EdgeTypeGRPCCall}},
	}
	result := contract.Result{
		Edges: []graph.Edge{
			{Type: graph.EdgeTypeGRPCCall, Confidence: graph.ConfidenceInferred},
		},
	}

	cov := contract.ComputeCoverage(rules, result)
	assert.Len(t, cov, 1)
	assert.Equal(t, 1, cov[0].Matched)
	assert.Equal(t, 0, cov[0].Unresolved)
}

func TestComputeCoverage_AllUnresolved(t *testing.T) {
	rules := []contract.Rule{
		{Kind: "amqp", Edge: contract.EdgeSpec{Type: graph.EdgeTypePublishes}},
	}
	result := contract.Result{
		Unresolved: []graph.UnresolvedRef{{Kind: "amqp"}, {Kind: "amqp"}},
	}

	cov := contract.ComputeCoverage(rules, result)
	assert.Len(t, cov, 1)
	assert.Equal(t, 0, cov[0].Matched)
	assert.Equal(t, 2, cov[0].Unresolved)
}

func TestComputeCoverage_EmptyResult(t *testing.T) {
	rules := []contract.Rule{
		{Kind: contract.KindHTTP, Edge: contract.EdgeSpec{Type: graph.EdgeTypeHTTPCall}},
	}
	cov := contract.ComputeCoverage(rules, contract.Result{})
	assert.Len(t, cov, 1)
	assert.Equal(t, 0, cov[0].Matched)
	assert.Equal(t, 0, cov[0].Unresolved)
}

func TestComputeCoverage_SortedByKind(t *testing.T) {
	rules := []contract.Rule{
		{Kind: "websocket", Edge: contract.EdgeSpec{Type: graph.EdgeTypeWSSend}},
		{Kind: "amqp", Edge: contract.EdgeSpec{Type: graph.EdgeTypePublishes}},
		{Kind: "http", Edge: contract.EdgeSpec{Type: graph.EdgeTypeHTTPCall}},
	}
	cov := contract.ComputeCoverage(rules, contract.Result{})
	assert.Len(t, cov, 3)
	assert.Equal(t, "amqp", cov[0].Kind)
	assert.Equal(t, "http", cov[1].Kind)
	assert.Equal(t, "websocket", cov[2].Kind)
}

func TestComputeCoverage_DuplicateRuleKindsMerged(t *testing.T) {
	// http has two rule variants (API call + nav-link) but one kind
	rules := []contract.Rule{
		{Kind: contract.KindHTTP, Edge: contract.EdgeSpec{Type: graph.EdgeTypeHTTPCall}},
		{Kind: contract.KindHTTP, Edge: contract.EdgeSpec{Type: graph.EdgeTypeNavigatesTo}},
	}
	result := contract.Result{
		Edges: []graph.Edge{
			{Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceStatic},
			{Type: graph.EdgeTypeNavigatesTo, Confidence: graph.ConfidenceInferred},
		},
	}
	cov := contract.ComputeCoverage(rules, result)
	assert.Len(t, cov, 1, "two variants of the same kind collapse to one row")
	assert.Equal(t, "http", cov[0].Kind)
	assert.Equal(t, 2, cov[0].Matched)
}

func TestComputeCoverage_UnknownEdgeTypeIgnored(t *testing.T) {
	// Edges whose type is not mapped to any rule kind are ignored
	rules := []contract.Rule{
		{Kind: "http", Edge: contract.EdgeSpec{Type: graph.EdgeTypeHTTPCall}},
	}
	result := contract.Result{
		Edges: []graph.Edge{
			{Type: graph.EdgeTypeCalls, Confidence: graph.ConfidenceStatic}, // non-contract edge
			{Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceStatic},
		},
	}
	cov := contract.ComputeCoverage(rules, result)
	assert.Len(t, cov, 1)
	assert.Equal(t, 1, cov[0].Matched, "non-contract edges must not inflate matched count")
}

// TestComputeCoverage_Indirect verifies that edges resolved via alias/wrapper
// are counted in the Indirect column for their kind.
func TestComputeCoverage_Indirect(t *testing.T) {
	rules := []contract.Rule{
		{Kind: contract.KindHTTP, Edge: contract.EdgeSpec{Type: graph.EdgeTypeHTTPCall}},
	}
	result := contract.Result{
		Edges: []graph.Edge{
			// Static match with no indirection
			{Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceStatic,
				Meta: map[string]string{"confidence": graph.ConfidenceStatic}},
			// Match via alias indirection
			{Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceInferred,
				Meta: map[string]string{"confidence": graph.ConfidenceInferred, "via": "alias"}},
			// Match via wrapper indirection
			{Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceInferred,
				Meta: map[string]string{"confidence": graph.ConfidenceInferred, "via": "wrapper"}},
			// Match via branch_enum (G.6) — not counted as indirect
			{Type: graph.EdgeTypeHTTPCall, Confidence: graph.ConfidenceInferred,
				Meta: map[string]string{"confidence": graph.ConfidenceInferred, "via": "branch_enum"}},
		},
	}

	cov := contract.ComputeCoverage(rules, result)
	httpCov := findCoverage(cov, "http")
	assert.Equal(t, 4, httpCov.Matched)
	assert.Equal(t, 2, httpCov.Indirect, "only alias and wrapper edges count as indirect")
}

func findCoverage(cov []contract.KindCoverage, kind string) contract.KindCoverage {
	for _, c := range cov {
		if c.Kind == kind {
			return c
		}
	}
	return contract.KindCoverage{Kind: kind}
}
