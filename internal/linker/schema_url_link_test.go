package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// orionTable is the worked example's asset as a hand-built table — the dialect
// is deliberately not the motivating corpus's.
func orionTable() map[string]*SchemaURLTable {
	return map[string]*SchemaURLTable{
		"svc": {
			File: "config/orion-resources.yml",
			ByEntity: map[string]map[string]schemaURLEntry{
				"widget": {
					"endpoint": {Raw: "/api/gadgets/{gadget_id}/widgets", Path: "/api/gadgets/*/widgets", Key: "endpoint"},
					"update":   {Raw: "/api/widgets/<id>", Path: "/api/widgets/*", Key: "update"},
				},
				"gadget": {
					"endpoint": {Raw: "/api/gadgets", Path: "/api/gadgets", Key: "endpoint"},
					"reorder":  {Raw: "/api/gadgets/:id/reorder", Path: "/api/gadgets/*/reorder", Key: "reorder"},
				},
			},
			Aliases: map[string]string{},
		},
	}
}

const widgetPane = `export default class WidgetPane extends React.Component {
  save = values => {
    const { ajaxStatus, resources } = this.props;
    const r = resources.widget;
    ajaxStatus.ajax("Saving...", { url: r.update.replace("<id>", values.id), method: "PUT" });
  };
  reorder = ids => {
    const { ajaxStatus, resources, id } = this.props;
    const url = resources["gadget"].reorder.replace(":id", id);
    ajaxStatus.ajax("Reordering...", { url, method: "POST" });
  };
}
`

func schemaClient(file string, line int, verb, raw string) graph.Node {
	return graph.Node{
		ID: "c", Type: graph.NodeTypeHTTPClient, Service: "svc", File: file, Line: line,
		Language: "javascript",
		Meta: map[string]string{
			"method": verb, "key_dynamic": "true", "key_dynamic_raw": raw,
		},
	}
}

func TestResolveSchemaURLs_WorkedExample(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{"components/WidgetPane.jsx": widgetPane})
	f := p["components/WidgetPane.jsx"]

	nodes := []graph.Node{
		schemaClient(f, 5, "PUT", `r.update.replace("<id>", values.id)`),
		schemaClient(f, 9, "POST", `resources["gadget"].reorder.replace(":id", id)`),
	}
	changed, ledger := ResolveSchemaURLs(nodes, orionTable())
	if len(ledger) != 0 {
		t.Fatalf("unexpected ledger: %+v", ledger)
	}
	if len(changed) != 2 {
		t.Fatalf("changed = %d, want 2: %+v", len(changed), changed)
	}
	want := map[string]struct{ url, entity, key string }{
		"PUT /api/widgets/*":          {"/api/widgets/*", "widget", "update"},
		"POST /api/gadgets/*/reorder": {"/api/gadgets/*/reorder", "gadget", "reorder"},
	}
	for _, n := range changed {
		w, ok := want[n.Label]
		if !ok {
			t.Fatalf("unexpected label %q", n.Label)
		}
		if n.Meta["url"] != w.url || n.Meta["schema_entity"] != w.entity || n.Meta["schema_key"] != w.key {
			t.Errorf("%s: meta = %+v", n.Label, n.Meta)
		}
		if n.Meta["url_origin"] != "schema_asset" || n.Meta["schema_file"] != "config/orion-resources.yml" {
			t.Errorf("%s: provenance meta = %+v", n.Label, n.Meta)
		}
		if n.Meta["schema_url_raw"] == "" {
			t.Errorf("%s: missing schema_url_raw", n.Label)
		}
		if n.Meta["key_dynamic"] != "" {
			t.Errorf("%s: dynamic markers not retired", n.Label)
		}
	}
}

func TestResolveSchemaURLs_VocabularyCollision(t *testing.T) {
	t.Parallel()
	src := `export function submit(form) {
  const el = form.action;
  fetch(el);
}
`
	_, p := writeReduxFixture(t, map[string]string{"a.jsx": src})
	tbl := map[string]*SchemaURLTable{"svc": {
		File: "s.yml",
		ByEntity: map[string]map[string]schemaURLEntry{
			"form": {"endpoint": {Raw: "/api/forms", Path: "/api/forms", Key: "endpoint"}},
		},
		Aliases: map[string]string{},
	}}
	nodes := []graph.Node{schemaClient(p["a.jsx"], 2, "GET", `form.action`)}
	changed, ledger := ResolveSchemaURLs(nodes, tbl)
	if len(changed) != 0 || len(ledger) != 0 {
		t.Fatalf("collision produced changed=%v ledger=%v", changed, ledger)
	}
}

func TestResolveSchemaURLs_AmbiguousPin(t *testing.T) {
	t.Parallel()
	src := `export function go(resources) {
  let r = resources.widget;
  r = resources.gadget;
  fetch(r.update);
}
`
	_, p := writeReduxFixture(t, map[string]string{"b.jsx": src})
	nodes := []graph.Node{schemaClient(p["b.jsx"], 4, "PUT", `r.update`)}
	changed, ledger := ResolveSchemaURLs(nodes, orionTable())
	if len(changed) != 0 {
		t.Fatalf("ambiguous pin resolved: %+v", changed)
	}
	if len(ledger) != 1 || ledger[0].Kind != "schema_entity_ambiguous" {
		t.Fatalf("ledger = %+v", ledger)
	}
}

func TestResolveSchemaURLs_ReplaceRegexPoisons(t *testing.T) {
	t.Parallel()
	src := `export function go(resources) {
  const r = resources.widget;
  fetch(r.update.replace(/x/, "y"));
}
`
	_, p := writeReduxFixture(t, map[string]string{"c.jsx": src})
	nodes := []graph.Node{schemaClient(p["c.jsx"], 3, "PUT", `r.update.replace(/x/, "y")`)}
	changed, ledger := ResolveSchemaURLs(nodes, orionTable())
	if len(changed) != 0 {
		t.Fatalf("poisoned site resolved: %+v", changed)
	}
	if len(ledger) != 1 || ledger[0].Kind != "schema_entity_unresolved" {
		t.Fatalf("ledger = %+v", ledger)
	}
}

func TestResolveSchemaURLs_Rung4Ledgers(t *testing.T) {
	t.Parallel()
	src := `export function go(response) {
  const s = response.schema;
  fetch(s.endpoint);
}
`
	_, p := writeReduxFixture(t, map[string]string{"d.jsx": src})
	nodes := []graph.Node{schemaClient(p["d.jsx"], 3, "GET", `s.endpoint`)}
	changed, ledger := ResolveSchemaURLs(nodes, orionTable())
	if len(changed) != 0 {
		t.Fatalf("rung 4 minted a node: %+v", changed)
	}
	if len(ledger) != 1 || ledger[0].Kind != "schema_entity_unresolved" {
		t.Fatalf("ledger = %+v", ledger)
	}
}

func TestResolveSchemaURLs_PinShapesAndCopyWrapper(t *testing.T) {
	t.Parallel()
	src := `export function go(resources, getSchema) {
  const a = resources.widget;
  const b = resources["widget"];
  const c = getSchema("widget");
  const d = clone(resources.widget);
  fetch(a.update); fetch(b.update); fetch(c.update); fetch(d.update);
}
`
	_, p := writeReduxFixture(t, map[string]string{"e.jsx": src})
	f := p["e.jsx"]
	nodes := []graph.Node{
		schemaClient(f, 6, "PUT", `a.update`),
		schemaClient(f, 6, "PUT", `b.update`),
		schemaClient(f, 6, "PUT", `c.update`),
		schemaClient(f, 6, "PUT", `d.update`),
	}
	changed, ledger := ResolveSchemaURLs(nodes, orionTable())
	if len(ledger) != 0 {
		t.Fatalf("ledger: %+v", ledger)
	}
	if len(changed) != 4 {
		t.Fatalf("changed = %d, want 4", len(changed))
	}
	for _, n := range changed {
		if n.Meta["url"] != "/api/widgets/*" {
			t.Errorf("url = %q", n.Meta["url"])
		}
	}
}

func TestResolveSchemaURLs_TransformCallDoesNotPin(t *testing.T) {
	t.Parallel()
	src := `export function go(resources) {
  const d = pickOne(resources.widget, resources.gadget);
  fetch(d.update);
}
`
	_, p := writeReduxFixture(t, map[string]string{"g.jsx": src})
	nodes := []graph.Node{schemaClient(p["g.jsx"], 3, "PUT", `d.update`)}
	changed, ledger := ResolveSchemaURLs(nodes, orionTable())
	if len(changed) != 0 {
		t.Fatalf("two-arg transform pinned: %+v", changed)
	}
	// key "update" is in the table but receiver is unpinnable -> blind-spot row.
	if len(ledger) != 1 || ledger[0].Kind != "schema_entity_unresolved" {
		t.Fatalf("ledger = %+v", ledger)
	}
}
