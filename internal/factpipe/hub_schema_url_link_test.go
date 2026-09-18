package factpipe

import (
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier RC.4 (docs/js-declarative-composition-cluster-plan.md) — asserts
// sulSchemaEntityPinFacts dumps the SAME (entity, key) -> path rows
// schemaurl.Resolver already resolves individual expressions against,
// ported from the widgetJSON fixture in
// internal/factpipe/pipeline/schema_url_link_test.go's
// TestSUL_SchemaSweep_ResolvesWorkedExample.
func TestSulSchemaEntityPinFacts_MatchesResolverTable(t *testing.T) {
	const widgetJSON = `{
  "resources": {
    "widget":  { "endpoint": "/api/widgets", "reorder": "/api/widgets/:id/reorder" },
    "gadget":  { "endpoint": "/api/gadgets", "reorder": "/api/gadgets/:id/reorder" },
    "sprocket":{ "endpoint": "/api/sprockets", "update": "/api/sprockets/{id}" }
  }
}`
	svcPath := writeTableFixture(t, map[string]string{
		"config/resources.json": widgetJSON,
		"a.jsx":                 "export function go(resources) {\n  fetch(resources.widget.reorder);\n}\n",
	})
	nodes := []graph.Node{
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets"}},
		{ID: "h2", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets/:id/reorder"}},
		{ID: "h3", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets"}},
		{ID: "h4", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets/:id/reorder"}},
		{ID: "h5", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets"}},
		{ID: "h6", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets/{id}"}},
	}

	svc, resolver := sulBuildResolver(nodes, []string{filepath.Join(svcPath, "a.jsx")}, svcPath, graph.SchemaConfig{})
	if svc == "" || resolver == nil {
		t.Fatalf("sulBuildResolver produced no resolver; svc=%q resolver=%v", svc, resolver)
	}

	facts := sulSchemaEntityPinFacts(svc, resolver)
	if len(facts) != 6 {
		t.Fatalf("got %d entity pin facts, want 6: %+v", len(facts), facts)
	}
	got := map[[3]string]bool{}
	for _, f := range facts {
		if f.Pred != sulSchemaEntityPinPred {
			t.Errorf("fact pred = %q, want %q", f.Pred, sulSchemaEntityPinPred)
		}
		if len(f.Args) != 3 {
			t.Fatalf("fact args = %+v, want 3 (Entity, Key, Path)", f.Args)
		}
		got[[3]string{f.Args[0].Str, f.Args[1].Str, f.Args[2].Str}] = true
	}
	want := map[[3]string]bool{
		{"widget", "endpoint", "/api/widgets"}:          true,
		{"widget", "reorder", "/api/widgets/*/reorder"}: true,
		{"gadget", "endpoint", "/api/gadgets"}:          true,
		{"gadget", "reorder", "/api/gadgets/*/reorder"}: true,
		{"sprocket", "endpoint", "/api/sprockets"}:      true,
		{"sprocket", "update", "/api/sprockets/*"}:      true,
	}
	if len(got) != len(want) {
		t.Fatalf("pins = %+v, want %+v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing pin %+v; got %+v", k, got)
		}
	}
}

func TestSulSchemaEntityPinFacts_NilResolverIsNoOp(t *testing.T) {
	if facts := sulSchemaEntityPinFacts("svc", nil); facts != nil {
		t.Errorf("nil resolver produced facts: %+v", facts)
	}
}
