package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier VG.3 acceptance (docs/js-value-graph-pilot-plan.md) was a differential:
// the engine had to reproduce the legacy walker byte-for-byte — zero LOST, zero
// CHANGED. VG.5 retired the walker, so there is no second arm left to compare
// against and these fixtures became goldens: the shapes and the exact paths the
// engine reads out of them, frozen. A diff here is a behaviour change, and
// changing the table is how you declare one.
//
// TestVGLocalURLFixtures (the LinkJSPropClients-driven half of this file)
// moved to internal/factpipe/pipeline/vg_differential_test.go when that pass
// migrated to Tier FX (schema_url_link + js_prop_clients combined migration,
// 2026-09-16). TestVGLocalURLProvenanceStamp drives ResolveJSLocalURLs
// directly, never part of that migration, so it stays here.

// TestVGLocalURLProvenanceStamp asserts the resolver stamps SA.1 layer/rule
// onto every node it mints or mutates — writeEdges propagates it from there
// onto the http_call edge.
func TestVGLocalURLProvenanceStamp(t *testing.T) {
	_, p := writeReduxFixture(t, map[string]string{"modules/arms.es6": `function reload(id, isNew) {
  let url;
  if (isNew) {
    url = "/app/arms/new";
  } else {
    url = ` + "`/app/arms/${id}/edit`" + `;
  }
  $.ajax({ url, type: "GET" });
}
`})
	in := []graph.Node{{
		ID: "svc:modules/arms.es6:http_client:8", Type: graph.NodeTypeHTTPClient,
		Label: "dynamic", Service: "svc", File: p["modules/arms.es6"], Line: 8,
		Language: "javascript",
		Meta: map[string]string{
			"method": "GET", "key_dynamic": "true", "key_dynamic_raw": `{ url, type: "GET" }`,
		},
	}}

	changed, added, _ := ResolveJSLocalURLs(in, nil)
	if len(changed) != 1 || len(added) != 1 {
		t.Fatalf("changed=%d added=%d, want 1 and 1", len(changed), len(added))
	}
	for _, n := range append(changed, added...) {
		if n.Meta["vg_layer"] != vgLayer || n.Meta["vg_rule"] != vgLocalBindingRule {
			t.Errorf("node %s missing VG provenance: layer=%q rule=%q",
				n.ID, n.Meta["vg_layer"], n.Meta["vg_rule"])
		}
	}
}
