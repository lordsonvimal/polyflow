package pipeline_test

// FX.8.16 (2026-09-16): js_http_grade — Tier FX migration of
// internal/linker/js_http_grade.go's GradeJSHTTPProducers (JG.1), replaced
// by patterns/generic/js_http_grade.yaml + rules/generic/js_http_grade.dl,
// driven by the "js_http_grade" hub provider
// (internal/factpipe/hub_js_http_grade.go). Hub-only, no file I/O — every
// case is a graph.Node handed straight to the hub, porting the retired
// Go test's fixtures verbatim.

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func jhgActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("js_http_grade")
	if fw == nil {
		t.Fatal("js_http_grade framework not embedded")
	}
	return []*pipeline.Framework{fw}
}

func jhgRun(t *testing.T, nodes []graph.Node) pipeline.Result {
	t.Helper()
	res, err := pipeline.Run(jhgActive(t), nil, graph.Snapshot{Nodes: nodes})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func jhgClient(id string, meta map[string]string) graph.Node {
	return graph.Node{ID: id, Type: graph.NodeTypeHTTPClient, Language: "javascript", Meta: meta}
}

// jhgPatchByID merges every Patches row for id, in encounter order —
// js_http_grade emits two relations (evidence, ceiling) that both patch the
// same node id, exactly how internal/indexer/link_passes.go's dedicated
// call site applies them sequentially onto the real node's Meta.
func jhgPatchByID(res pipeline.Result, id string) (map[string]string, bool) {
	var merged map[string]string
	found := false
	for _, p := range res.Patches {
		if p.ID != id {
			continue
		}
		found = true
		if merged == nil {
			merged = map[string]string{}
		}
		for k, v := range p.Meta {
			merged[k] = v
		}
	}
	return merged, found
}

func TestJSHTTPGradeRule(t *testing.T) {
	nodes := []graph.Node{
		// 0: opaque host, one literal segment -> weak + partial ceiling
		jhgClient("n0", map[string]string{"url": "*/*/unlock"}),
		// 1: opaque host, two literal (non-namespace) segments -> strong, no marker
		jhgClient("n1", map[string]string{"url": "*/studies/*/roles"}),
		// 2: opaque host, only namespace + wildcards -> none, no marker
		jhgClient("n2", map[string]string{"url": "*/api/v1/*"}),
		// 3: root-relative literal path -> strong via no-wildcard branch, no marker
		jhgClient("n3", map[string]string{"url": "/app/folders/123"}),
		// 4: host already resolved via env var -> skip
		jhgClient("n4", map[string]string{"url": "*/health", "env_var": "API_URL"}),
		// 5: host resolved by react_prop_urls -> skip
		jhgClient("n5", map[string]string{"url": "/x/unlock", "path_resolved_via": "react_prop_url"}),
		// 6: nav link -> skip
		jhgClient("n6", map[string]string{"url": "*/logout", "nav_link": "true"}),
		// 7: already graded -> left untouched
		jhgClient("n7", map[string]string{"url": "*/health", "path_evidence": "strong"}),
		// 8: weak but another pass set a ceiling -> keep that ceiling
		jhgClient("n8", map[string]string{"url": "*/emails", "confidence_ceiling": graph.ConfidenceInferred}),
		// 9: Ruby http_client -- out of scope
		{ID: "n9", Type: graph.NodeTypeHTTPClient, Language: "ruby", Meta: map[string]string{"url": "*/unlock"}},
	}

	res := jhgRun(t, nodes)

	m0, ok := jhgPatchByID(res, "n0")
	if !ok {
		t.Fatal("n0: expected a patch")
	}
	if m0["path_evidence"] != graph.PathEvidenceWeak {
		t.Errorf("n0 path_evidence = %q, want %q", m0["path_evidence"], graph.PathEvidenceWeak)
	}
	if m0["confidence_ceiling"] != graph.ConfidencePartial {
		t.Errorf("n0 confidence_ceiling = %q, want %q", m0["confidence_ceiling"], graph.ConfidencePartial)
	}

	for _, id := range []string{"n1", "n2", "n3", "n4", "n5", "n6", "n7", "n9"} {
		if _, ok := jhgPatchByID(res, id); ok {
			t.Errorf("%s: expected no patch", id)
		}
	}

	m8, ok := jhgPatchByID(res, "n8")
	if !ok {
		t.Fatal("n8: expected a patch")
	}
	if m8["path_evidence"] != graph.PathEvidenceWeak {
		t.Errorf("n8 path_evidence = %q, want %q", m8["path_evidence"], graph.PathEvidenceWeak)
	}
	if _, hasCeiling := m8["confidence_ceiling"]; hasCeiling {
		t.Errorf("n8: must not patch confidence_ceiling, existing ceiling %q must not be clobbered", graph.ConfidenceInferred)
	}

	// Exactly nodes 0 and 8 patched.
	patchedIDs := map[string]bool{}
	for _, p := range res.Patches {
		patchedIDs[p.ID] = true
	}
	if len(patchedIDs) != 2 {
		t.Errorf("got %d distinct patched ids, want 2: %v", len(patchedIDs), patchedIDs)
	}
}

func TestJSHTTPGradeRule_Idempotent(t *testing.T) {
	nodes := []graph.Node{
		jhgClient("n0", map[string]string{"url": "*/*/unlock", "path_evidence": graph.PathEvidenceWeak, "confidence_ceiling": graph.ConfidencePartial}),
	}
	res := jhgRun(t, nodes)
	if len(res.Patches) != 0 {
		t.Errorf("already-graded node must not repatch, got %d patches", len(res.Patches))
	}
}
