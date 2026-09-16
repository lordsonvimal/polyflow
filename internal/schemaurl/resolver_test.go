package schemaurl_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/jsast"
	"github.com/lordsonvimal/polyflow/internal/schemaurl"
)

// writeJSFixture writes files (name→content) under a fresh temp dir and
// returns the name→abs-path map.
func writeJSFixture(t *testing.T, files map[string]string) map[string]string {
	t.Helper()
	dir := t.TempDir()
	paths := make(map[string]string, len(files))
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths[name] = p
	}
	return paths
}

// exprAtLine finds the expression whose source text is exactly raw, starting
// at or after line — the same "match on recorded text" idiom
// internal/linker/js_local_url.go's jsHostFile.exprAtLine uses, ported here
// so this test package can drive Resolver.ResolveURLExpr the same way the
// real hub (internal/factpipe/hub_schema_url_link.go) does, without
// depending on internal/linker.
func exprAtLine(root *sitter.Node, src []byte, line int, raw string) *sitter.Node {
	var found *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != nil || n == nil {
			return
		}
		row := int(n.StartPoint().Row) + 1
		if row > line+6 {
			return
		}
		if row >= line && n.Content(src) == raw {
			found = n
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return found
}

// resolveSchemaURLs is a minimal test-only re-implementation of the retired
// internal/linker/schema_url_link.go's ResolveSchemaURLs sweep: for each node
// naming a raw dynamic-URL expression, parse its file, find the expression,
// resolve it through r, and apply the hit. Exercises exactly the API surface
// the real hub calls.
func resolveSchemaURLs(nodes []graph.Node, r *schemaurl.Resolver) (changed []graph.Node, ledger []graph.UnresolvedRef) {
	fileCache := map[string]struct {
		src  []byte
		root *sitter.Node
	}{}
	for i := range nodes {
		n := &nodes[i]
		raw := n.Meta["key_dynamic_raw"]
		if raw == "" {
			continue
		}
		jf, cached := fileCache[n.File]
		if !cached {
			src, root, _, ok := jsast.Parse(n.File)
			if ok {
				jf = struct {
					src  []byte
					root *sitter.Node
				}{src, root}
			}
			fileCache[n.File] = jf
		}
		if jf.root == nil {
			continue
		}
		expr := exprAtLine(jf.root, jf.src, n.Line, raw)
		if expr == nil {
			continue
		}
		fn := jsast.EnclosingFunction(expr)
		hit, ok, kind := r.ResolveURLExpr(expr, fn, jf.src, n.Service)
		if kind != "" {
			ledger = append(ledger, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line, Name: raw, Kind: kind,
			})
			continue
		}
		if !ok {
			continue
		}
		verb := strings.ToUpper(n.Meta["method"])
		if verb == "" {
			ledger = append(ledger, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line, Name: raw, Kind: "schema_entity_unresolved",
			})
			continue
		}
		schemaurl.ApplyURL(n, hit, verb)
		changed = append(changed, *n)
	}
	return changed, ledger
}

// orionTable is the worked example's asset as a hand-built table — the
// dialect is deliberately not the motivating corpus's.
func orionTable() map[string]*schemaurl.Table {
	return map[string]*schemaurl.Table{
		"svc": {
			File: "config/orion-resources.yml",
			ByEntity: map[string]map[string]schemaurl.Entry{
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

func orionResolver() *schemaurl.Resolver {
	return schemaurl.NewResolver(orionTable(), nil)
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
	p := writeJSFixture(t, map[string]string{"components/WidgetPane.jsx": widgetPane})
	f := p["components/WidgetPane.jsx"]

	nodes := []graph.Node{
		schemaClient(f, 5, "PUT", `r.update.replace("<id>", values.id)`),
		schemaClient(f, 9, "POST", `resources["gadget"].reorder.replace(":id", id)`),
	}
	changed, ledger := resolveSchemaURLs(nodes, orionResolver())
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
	p := writeJSFixture(t, map[string]string{"a.jsx": src})
	tbl := map[string]*schemaurl.Table{"svc": {
		File: "s.yml",
		ByEntity: map[string]map[string]schemaurl.Entry{
			"form": {"endpoint": {Raw: "/api/forms", Path: "/api/forms", Key: "endpoint"}},
		},
		Aliases: map[string]string{},
	}}
	nodes := []graph.Node{schemaClient(p["a.jsx"], 2, "GET", `form.action`)}
	changed, ledger := resolveSchemaURLs(nodes, schemaurl.NewResolver(tbl, nil))
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
	p := writeJSFixture(t, map[string]string{"b.jsx": src})
	nodes := []graph.Node{schemaClient(p["b.jsx"], 4, "PUT", `r.update`)}
	changed, ledger := resolveSchemaURLs(nodes, orionResolver())
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
	p := writeJSFixture(t, map[string]string{"c.jsx": src})
	nodes := []graph.Node{schemaClient(p["c.jsx"], 3, "PUT", `r.update.replace(/x/, "y")`)}
	changed, ledger := resolveSchemaURLs(nodes, orionResolver())
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
	p := writeJSFixture(t, map[string]string{"d.jsx": src})
	nodes := []graph.Node{schemaClient(p["d.jsx"], 3, "GET", `s.endpoint`)}
	changed, ledger := resolveSchemaURLs(nodes, orionResolver())
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
	p := writeJSFixture(t, map[string]string{"e.jsx": src})
	f := p["e.jsx"]
	nodes := []graph.Node{
		schemaClient(f, 6, "PUT", `a.update`),
		schemaClient(f, 6, "PUT", `b.update`),
		schemaClient(f, 6, "PUT", `c.update`),
		schemaClient(f, 6, "PUT", `d.update`),
	}
	changed, ledger := resolveSchemaURLs(nodes, orionResolver())
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
	p := writeJSFixture(t, map[string]string{"g.jsx": src})
	nodes := []graph.Node{schemaClient(p["g.jsx"], 3, "PUT", `d.update`)}
	changed, ledger := resolveSchemaURLs(nodes, orionResolver())
	if len(changed) != 0 {
		t.Fatalf("two-arg transform pinned: %+v", changed)
	}
	// key "update" is in the table but receiver is unpinnable -> blind-spot row.
	if len(ledger) != 1 || ledger[0].Kind != "schema_entity_unresolved" {
		t.Fatalf("ledger = %+v", ledger)
	}
}

// ── MS.2: accessor learning ──────────────────────────────────────────────────

func TestNewResolver_LearnsAccessorsFromBodies(t *testing.T) {
	t.Parallel()
	src := `export function listURL(res) { return res.endpoint; }
export function itemURL(res) { return res.update || listURL(res); }
export function wrapped(res, opts) { return format(opts, res.reorder); }
export function notAccessor(res) { return globalThing.value; }
`
	p := writeJSFixture(t, map[string]string{"helpers.js": src})
	r := schemaurl.NewResolver(orionTable(), map[string][]string{"svc": {p["helpers.js"]}})
	if r == nil {
		t.Fatal("nil resolver")
	}
	if got := r.LearntAccessorCount(); got < 3 {
		t.Errorf("LearntAccessorCount = %d, want >= 3", got)
	}
	// Exercise the learnt accessors indirectly through ResolveURLExpr, since
	// the accessor map itself is unexported. wrapped's learnt key ("reorder")
	// only exists on the "gadget" entity, so it is pinned separately.
	src2 := `import { listURL, itemURL, wrapped } from "./helpers";
export function go(props) {
  const schema = props.resources.widget;
  const g = props.resources.gadget;
  fetch(listURL(schema));
  fetch(itemURL(schema));
  fetch(wrapped(g, {}));
}
`
	p2 := writeJSFixture(t, map[string]string{"c.jsx": src2})
	r2 := schemaurl.NewResolver(orionTable(), map[string][]string{"svc": {p["helpers.js"], p2["c.jsx"]}})
	nodes := []graph.Node{
		schemaClient(p2["c.jsx"], 5, "GET", `listURL(schema)`),
		schemaClient(p2["c.jsx"], 7, "GET", `wrapped(g, {})`),
	}
	changed, ledger := resolveSchemaURLs(nodes, r2)
	if len(ledger) != 0 {
		t.Fatalf("ledger: %+v", ledger)
	}
	if len(changed) != 2 {
		t.Fatalf("changed = %d, want 2: %+v", len(changed), changed)
	}
}

func TestNewResolver_DepthBound(t *testing.T) {
	t.Parallel()
	src := `export function a(s) { return b(s); }
export function b(s) { return c(s); }
export function c(s) { return d(s); }
export function d(s) { return s.update; }
export function useA(props) {
  const schema = props.resources.widget;
  fetch(a(schema));
}
`
	p := writeJSFixture(t, map[string]string{"chain.js": src})
	r := schemaurl.NewResolver(orionTable(), map[string][]string{"svc": {p["chain.js"]}})
	nodes := []graph.Node{schemaClient(p["chain.js"], 8, "GET", `a(schema)`)}
	changed, ledger := resolveSchemaURLs(nodes, r)
	if len(changed) != 0 {
		t.Fatalf("a classified through a 4-deep chain; maxAccessorDepth is 3: changed=%+v", changed)
	}
	_ = ledger
}

func schemaResolver2(t *testing.T, files map[string]string) (*schemaurl.Resolver, map[string]string) {
	t.Helper()
	p := writeJSFixture(t, files)
	var abs []string
	for _, v := range p {
		abs = append(abs, v)
	}
	return schemaurl.NewResolver(orionTable(), map[string][]string{"svc": abs}), p
}

func TestResolveSchemaURLs_ThroughAccessor(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"h.js": `export function getCreateURL(schema, props) { return schema.update; }`,
		"c.jsx": `import { getCreateURL } from "./h";
export function save(props) {
  const schema = props.resources.widget;
  const url = getCreateURL(schema, props);
  fetch(url, { method: "PUT" });
}
`,
	}
	r, p := schemaResolver2(t, files)
	nodes := []graph.Node{schemaClient(p["c.jsx"], 5, "PUT", `url`)}
	// key_dynamic_raw is the bare `url` local; the resolver backtracks it one hop.
	changed, ledger := resolveSchemaURLs(nodes, r)
	if len(ledger) != 0 {
		t.Fatalf("ledger: %+v", ledger)
	}
	if len(changed) != 1 || changed[0].Meta["url"] != "/api/widgets/*" {
		t.Fatalf("changed = %+v", changed)
	}
	if changed[0].Meta["schema_key"] != "update" || changed[0].Meta["schema_entity"] != "widget" {
		t.Errorf("provenance = %+v", changed[0].Meta)
	}
}

func TestResolveSchemaURLs_ConditionalAccessorAmbiguousPaths(t *testing.T) {
	t.Parallel()
	// widget.endpoint and widget.update resolve to different paths, so an
	// accessor returning either is unresolvable at the call site.
	files := map[string]string{
		"h.js": `export function u(s) { return s.endpoint || s.update; }`,
		"c.jsx": `import { u } from "./h";
export function go(props) {
  const s = props.r.widget;
  const url = u(s);
  fetch(url, { method: "GET" });
}
`,
	}
	r, p := schemaResolver2(t, files)
	nodes := []graph.Node{schemaClient(p["c.jsx"], 5, "GET", `url`)}
	changed, ledger := resolveSchemaURLs(nodes, r)
	if len(changed) != 0 {
		t.Fatalf("ambiguous accessor resolved: %+v", changed)
	}
	if len(ledger) != 1 || ledger[0].Kind != "schema_key_ambiguous" {
		t.Fatalf("ledger = %+v", ledger)
	}
}
