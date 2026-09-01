package linker_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
)

func jsClient(meta map[string]string) graph.Node {
	return graph.Node{
		Type:     graph.NodeTypeHTTPClient,
		Language: "javascript",
		Meta:     meta,
	}
}

func TestGradeJSHTTPProducers(t *testing.T) {
	t.Parallel()

	nodes := []graph.Node{
		// 0: opaque host, one literal segment → weak + partial ceiling
		jsClient(map[string]string{"url": "*/*/unlock"}),
		// 1: opaque host, two literal (non-namespace) segments → strong, no marker
		jsClient(map[string]string{"url": "*/studies/*/roles"}),
		// 2: opaque host, only namespace + wildcards → none, no marker
		jsClient(map[string]string{"url": "*/api/v1/*"}),
		// 3: root-relative literal path → strong via no-wildcard branch, no marker
		jsClient(map[string]string{"url": "/app/folders/123"}),
		// 4: host already resolved via env var → skip
		jsClient(map[string]string{"url": "*/health", "env_var": "API_URL"}),
		// 5: host resolved by react_prop_urls → skip
		jsClient(map[string]string{"url": "/x/unlock", "path_resolved_via": "react_prop_url"}),
		// 6: nav link → skip
		jsClient(map[string]string{"url": "*/logout", "nav_link": "true"}),
		// 7: already graded → left untouched
		jsClient(map[string]string{"url": "*/health", "path_evidence": "strong"}),
		// 8: weak but another pass set a ceiling → keep that ceiling
		jsClient(map[string]string{"url": "*/emails", "confidence_ceiling": graph.ConfidenceInferred}),
		// 9: Ruby http_client — out of scope
		{Type: graph.NodeTypeHTTPClient, Language: "ruby", Meta: map[string]string{"url": "*/unlock"}},
	}

	changed := linker.GradeJSHTTPProducers(nodes)

	assert.Equal(t, graph.PathEvidenceWeak, nodes[0].Meta["path_evidence"])
	assert.Equal(t, graph.ConfidencePartial, nodes[0].Meta["confidence_ceiling"])

	assert.Empty(t, nodes[1].Meta["path_evidence"])
	assert.Empty(t, nodes[2].Meta["path_evidence"])
	assert.Empty(t, nodes[3].Meta["path_evidence"])
	assert.Empty(t, nodes[4].Meta["path_evidence"])
	assert.Empty(t, nodes[5].Meta["path_evidence"])
	assert.Empty(t, nodes[6].Meta["path_evidence"])
	assert.Equal(t, "strong", nodes[7].Meta["path_evidence"])

	assert.Equal(t, graph.PathEvidenceWeak, nodes[8].Meta["path_evidence"])
	assert.Equal(t, graph.ConfidenceInferred, nodes[8].Meta["confidence_ceiling"], "existing ceiling must not be clobbered")

	assert.Empty(t, nodes[9].Meta["path_evidence"])

	// changed set = nodes 0 and 8 only
	assert.Len(t, changed, 2)

	// idempotent
	assert.Empty(t, linker.GradeJSHTTPProducers(nodes))
}
