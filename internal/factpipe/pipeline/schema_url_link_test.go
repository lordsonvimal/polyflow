package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier FX — schema_url_link + js_prop_clients combined migration
// (2026-09-16). Ports internal/linker/js_prop_client_test.go's fixtures
// verbatim through pipeline.Run, replacing direct calls to the retired
// LinkJSPropClients.

const sulAjaxStatusHOC = `import React from "react";
const ComponentWithAjaxStatus = function (WrappedComponent) {
  class component extends React.Component {
    get = (msg, url) => {
      const request = { method: "GET", url };
      return this.ajax(msg, request);
    };
    ajax = (msg, request) => {
      return window.$.ajax(request);
    };
    render() {
      return (
        <div>
          <WrappedComponent ajaxStatus={this} {...this.props} />
        </div>
      );
    }
  }
  return component;
};
export default ComponentWithAjaxStatus;
`

// sulWriteFixture writes files (name→content) under a fresh temp dir and
// returns both the name→abs-path map and the flat abs-path list Files needs.
func sulWriteFixture(t *testing.T, files map[string]string) (map[string]string, []string) {
	t.Helper()
	dir := t.TempDir()
	paths := make(map[string]string, len(files))
	var list []string
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		paths[name] = p
		list = append(list, p)
	}
	return paths, list
}

// sulRun loads the schema_url_link_props framework and runs it once over
// nodes — the mint/edge/ledger half (SPA.4/SPA.5).
func sulRun(t *testing.T, nodes []graph.Node, files []string) Result {
	t.Helper()
	return sulRunFramework(t, "schema_url_link_props", nodes, files)
}

// sulRunSweep runs the schema_url_link_sweep framework — the existing-node
// patch half (retired ResolveSchemaURLs).
func sulRunSweep(t *testing.T, nodes []graph.Node, files []string) Result {
	t.Helper()
	return sulRunFramework(t, "schema_url_link_sweep", nodes, files)
}

func sulRunFramework(t *testing.T, name string, nodes []graph.Node, files []string) Result {
	t.Helper()
	reg, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	fw := reg.ByName(name)
	if fw == nil {
		t.Fatalf("%s framework not embedded", name)
	}
	res, err := Run([]*Framework{fw}, nil, graph.Snapshot{Nodes: nodes, Files: files})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// sulHasEdge reports whether edges contains one of type typ from -> to.
func sulHasEdge(edges []graph.Edge, typ graph.EdgeType, from, to string) bool {
	for _, e := range edges {
		if e.Type == typ && e.From == from && e.To == to {
			return true
		}
	}
	return false
}

// sulURLsOf returns the Meta["url"] of every http_client node minted.
func sulURLsOf(nodes []graph.Node) []string {
	var out []string
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeHTTPClient {
			out = append(out, nodes[i].Meta["url"])
		}
	}
	return out
}

func TestSUL_CallSiteResolves(t *testing.T) {
	t.Parallel()
	_, files := sulWriteFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC,
		"grids/VariablesGridView.jsx": `import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
class VariablesGridView extends React.Component {
  loadVariables() {
    this.props.ajaxStatus.get("loading", ` + "`/api/standards/${this.props.standardId}/variables`" + `);
  }
}
export default ComponentWithAjaxStatus(VariablesGridView);
`,
	})
	in := []graph.Node{
		{ID: "svc:grids/VariablesGridView.jsx:method:loadVariables:3", Type: graph.NodeTypeMethod,
			Label: "loadVariables", Service: "svc", File: "grids/VariablesGridView.jsx", Line: 3, Language: "javascript"},
	}
	res := sulRun(t, in, files)
	if len(res.Unresolved) != 0 {
		t.Errorf("unexpected ledger: %+v", res.Unresolved)
	}
	var hc *graph.Node
	for i := range res.Nodes {
		if res.Nodes[i].Type == graph.NodeTypeHTTPClient {
			hc = &res.Nodes[i]
		}
	}
	if hc == nil {
		t.Fatalf("no http_client node minted; nodes=%+v", res.Nodes)
	}
	if hc.Meta["url"] != "/api/standards/*/variables" {
		t.Errorf("url = %q, want /api/standards/*/variables", hc.Meta["url"])
	}
	if hc.Meta["method"] != "GET" {
		t.Errorf("method = %q", hc.Meta["method"])
	}
	if !sulHasEdge(res.Edges, graph.EdgeTypeCalls, "svc:grids/VariablesGridView.jsx:method:loadVariables:3", hc.ID) {
		t.Errorf("no calls edge from loadVariables; edges=%+v", res.Edges)
	}
}

func TestSUL_BareUnrelatedGetNoNode(t *testing.T) {
	t.Parallel()
	_, files := sulWriteFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC,
		"util/thing.jsx": `function doThing(store) {
  return store.get("a", "b");
}
`,
	})
	res := sulRun(t, []graph.Node{{ID: "anchor", Type: graph.NodeTypeFile, Service: "svc", File: "anchor.jsx", Line: 1}}, files)
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			t.Errorf("unrelated .get() minted a node: %+v", n)
		}
	}
	if len(res.Edges) != 0 {
		t.Errorf("unexpected edges: %+v", res.Edges)
	}
}

func TestSUL_DynamicURLBuilder_TemplateReturn(t *testing.T) {
	t.Parallel()
	_, files := sulWriteFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC,
		"grids/T.jsx": `import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
const getDataURL = (t) => ` + "`/api/${t}s`" + `;
class T extends React.Component {
  load() {
    this.props.ajaxStatus.get("loading", getDataURL(this.props.type));
  }
}
export default ComponentWithAjaxStatus(T);
`,
	})
	res := sulRun(t, []graph.Node{{ID: "anchor", Type: graph.NodeTypeFile, Service: "svc", File: "anchor.jsx", Line: 1}}, files)
	if got := sulURLsOf(res.Nodes); len(got) != 1 || got[0] != "/api/*s" {
		t.Errorf("urls = %v, want [/api/*s]; unresolved=%+v", got, res.Unresolved)
	}
}

func TestSUL_DynamicURLBuilder_SwitchReturns(t *testing.T) {
	t.Parallel()
	_, files := sulWriteFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC,
		"grids/S.jsx": `import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
function getDataURL(t) {
  switch (t) {
    case "a": return "/api/a";
    case "b": return "/api/b";
  }
}
class S extends React.Component {
  load() {
    this.props.ajaxStatus.get("loading", getDataURL(this.props.type));
  }
}
export default ComponentWithAjaxStatus(S);
`,
	})
	res := sulRun(t, []graph.Node{{ID: "anchor", Type: graph.NodeTypeFile, Service: "svc", File: "anchor.jsx", Line: 1}}, files)
	got := sulURLsOf(res.Nodes)
	want := map[string]bool{"/api/a": true, "/api/b": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Errorf("urls = %v, want /api/a + /api/b", got)
	}
}

func TestSUL_DynamicURLBuilder_Opaque(t *testing.T) {
	t.Parallel()
	_, files := sulWriteFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC,
		"grids/O.jsx": `import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
const getDataURL = (t) => buildIt(t);
class O extends React.Component {
  load() {
    this.props.ajaxStatus.get("loading", getDataURL(this.props.type));
  }
}
export default ComponentWithAjaxStatus(O);
`,
	})
	res := sulRun(t, []graph.Node{{ID: "anchor", Type: graph.NodeTypeFile, Service: "svc", File: "anchor.jsx", Line: 1}}, files)
	if got := sulURLsOf(res.Nodes); len(got) != 0 {
		t.Errorf("opaque builder minted nodes: %v", got)
	}
	found := false
	for _, u := range res.Unresolved {
		if u.Kind == "dynamic_url_builder" && u.Name == "getDataURL" {
			found = true
		}
	}
	if !found {
		t.Errorf("want dynamic_url_builder ledger naming getDataURL; got %+v", res.Unresolved)
	}
}

func TestSUL_DynamicURLLedger(t *testing.T) {
	t.Parallel()
	_, files := sulWriteFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC,
		"grids/G.jsx": `import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
class G extends React.Component {
  load(type) {
    this.props.ajaxStatus.get("loading", getDataURL(type));
  }
}
export default ComponentWithAjaxStatus(G);
`,
	})
	res := sulRun(t, []graph.Node{{ID: "anchor", Type: graph.NodeTypeFile, Service: "svc", File: "anchor.jsx", Line: 1}}, files)
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeHTTPClient {
			t.Errorf("dynamic URL minted a node: %+v", n)
		}
	}
	found := false
	for _, u := range res.Unresolved {
		if u.Kind == "dynamic_url_builder" && u.Name == "getDataURL" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected dynamic_url_builder ledger entry for getDataURL; got %+v", res.Unresolved)
	}
}

// ── Tier CW: a plain-parameter receiver ─────────────────────────────────────

func TestSUL_ParamReceiverResolves(t *testing.T) {
	t.Parallel()
	_, files := sulWriteFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC,
		"utils/config.jsx": `function fetchConfig(ajaxStatus) {
  if (!ajaxStatus || !ajaxStatus.get) return;
  ajaxStatus.get("Loading...", "/api/prefs/assistant_config");
}
export default fetchConfig;
`,
	})
	in := []graph.Node{
		{ID: "svc:utils/config.jsx:function:fetchConfig:1", Type: graph.NodeTypeFunction,
			Label: "fetchConfig", Service: "svc", File: "utils/config.jsx", Line: 1, Language: "javascript"},
	}
	res := sulRun(t, in, files)
	if len(res.Unresolved) != 0 {
		t.Errorf("unexpected ledger: %+v", res.Unresolved)
	}
	if got := sulURLsOf(res.Nodes); len(got) != 1 || got[0] != "/api/prefs/assistant_config" {
		t.Fatalf("urls = %v, want [/api/prefs/assistant_config]", got)
	}
	var id string
	for i := range res.Nodes {
		if res.Nodes[i].Type == graph.NodeTypeHTTPClient {
			id = res.Nodes[i].ID
		}
	}
	if !sulHasEdge(res.Edges, graph.EdgeTypeCalls, "svc:utils/config.jsx:function:fetchConfig:1", id) {
		t.Errorf("no calls edge from the enclosing helper; edges=%+v", res.Edges)
	}
}

func TestSUL_ParamNameMustMatchASpec(t *testing.T) {
	t.Parallel()
	_, files := sulWriteFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC,
		"utils/cache.jsx": `function readThrough(store) {
  return store.get("Loading...", "/api/prefs/assistant_config");
}
`,
	})
	res := sulRun(t, []graph.Node{{ID: "anchor", Type: graph.NodeTypeFile, Service: "svc", File: "anchor.jsx", Line: 1}}, files)
	if got := sulURLsOf(res.Nodes); len(got) != 0 {
		t.Errorf("a parameter matching no spec minted %v", got)
	}
	if len(res.Edges) != 0 || len(res.Unresolved) != 0 {
		t.Errorf("edges=%+v unresolved=%+v, want both empty", res.Edges, res.Unresolved)
	}
}

func TestSUL_ParamReceiverDynamicURLLedgers(t *testing.T) {
	t.Parallel()
	_, files := sulWriteFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC,
		"utils/load.jsx": `function load(ajaxStatus, kind) {
  ajaxStatus.get("Loading...", buildUrl(kind));
}
`,
	})
	res := sulRun(t, []graph.Node{{ID: "anchor", Type: graph.NodeTypeFile, Service: "svc", File: "anchor.jsx", Line: 1}}, files)
	if got := sulURLsOf(res.Nodes); len(got) != 0 {
		t.Errorf("dynamic URL minted %v", got)
	}
	if len(res.Edges) != 0 {
		t.Errorf("unexpected edges: %+v", res.Edges)
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Kind != "dynamic_url_builder" ||
		res.Unresolved[0].Name != "buildUrl" || res.Unresolved[0].Line != 2 {
		t.Fatalf("unresolved = %+v, want one dynamic_url_builder for buildUrl at line 2", res.Unresolved)
	}
}

func TestSUL_MissingURLArgLedgers(t *testing.T) {
	t.Parallel()
	_, files := sulWriteFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC,
		"utils/ping.jsx": `function ping(ajaxStatus) {
  ajaxStatus.get("Loading...");
}
`,
	})
	res := sulRun(t, []graph.Node{{ID: "anchor", Type: graph.NodeTypeFile, Service: "svc", File: "anchor.jsx", Line: 1}}, files)
	if got := sulURLsOf(res.Nodes); len(got) != 0 {
		t.Errorf("a call with no URL argument minted %v", got)
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Kind != "prop_client_dynamic_url" || res.Unresolved[0].Line != 2 {
		t.Fatalf("unresolved = %+v, want one prop_client_dynamic_url at line 2", res.Unresolved)
	}
}

func TestSUL_ParamScopeDoesNotLeak(t *testing.T) {
	t.Parallel()
	_, files := sulWriteFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC,
		"utils/two.jsx": `function withTransport(ajaxStatus) {
  ajaxStatus.get("Loading...", "/api/one");
}
function withoutTransport(ajaxStatus2) {
  const ajaxStatus = makeSomethingElse();
  ajaxStatus.get("Loading...", "/api/two");
}
`,
	})
	res := sulRun(t, []graph.Node{{ID: "anchor", Type: graph.NodeTypeFile, Service: "svc", File: "anchor.jsx", Line: 1}}, files)
	got := sulURLsOf(res.Nodes)
	if len(got) != 1 || got[0] != "/api/one" {
		t.Errorf("urls = %v, want only [/api/one]", got)
	}
}

func TestSUL_DestructuredParamReceiver(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		src  string
	}{
		{"object pattern parameter", `const Table = ({ ajaxStatus, canEdit }) => {
  ajaxStatus.get("Loading...", "/api/vlms");
};
`},
		{"destructured from a props parameter", `const Listing = props => {
  const { allowBlank, ajaxStatus } = props;
  ajaxStatus.get("Loading...", "/api/vlms");
};
`},
		{"destructured from a parameter's props", `function loadData(component) {
  const { ajaxStatus, i18n } = component.props;
  ajaxStatus.get("Loading...", "/api/vlms");
}
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, files := sulWriteFixture(t, map[string]string{
				"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC,
				"components/T.jsx":                   tc.src,
			})
			res := sulRun(t, []graph.Node{{ID: "anchor", Type: graph.NodeTypeFile, Service: "svc", File: "anchor.jsx", Line: 1}}, files)
			if got := sulURLsOf(res.Nodes); len(got) != 1 || got[0] != "/api/vlms" {
				t.Errorf("urls = %v, want [/api/vlms]", got)
			}
		})
	}
}

func TestSUL_DestructureFromACallIsNotVouched(t *testing.T) {
	t.Parallel()
	_, files := sulWriteFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC,
		"utils/listener.jsx": `function createListener(getProps) {
  const { actions, ajaxStatus } = getProps();
  ajaxStatus.get("Loading...", "/api/flags");
}
`,
	})
	res := sulRun(t, []graph.Node{{ID: "anchor", Type: graph.NodeTypeFile, Service: "svc", File: "anchor.jsx", Line: 1}}, files)
	if got := sulURLsOf(res.Nodes); len(got) != 0 {
		t.Errorf("a call-return destructure minted %v", got)
	}
}

// ── schema_url_links: existing-node sweep ───────────────────────────────────

func sulSchemaClient(file string, line int, verb, raw string) graph.Node {
	return graph.Node{
		ID: "c", Type: graph.NodeTypeHTTPClient, Service: "svc", File: file, Line: line,
		Language: "javascript",
		Meta: map[string]string{
			"method": verb, "key_dynamic": "true", "key_dynamic_raw": raw,
		},
	}
}

func TestSUL_SchemaSweep_NoTableIsNoOp(t *testing.T) {
	t.Parallel()
	// No schema asset present -> no schema table -> resolver is nil -> the
	// sweep is a pure no-op (never resolves, never patches, never ledgers).
	src := `export function go(resources) {
  fetch(resources.widget.update);
}
`
	_, files := sulWriteFixture(t, map[string]string{"a.jsx": src})
	nodes := []graph.Node{sulSchemaClient(files[0], 2, "GET", `resources.widget.update`)}
	res := sulRunSweep(t, nodes, files)
	if len(res.Patches) != 0 {
		t.Errorf("unexpected patches with no schema table: %+v", res.Patches)
	}
	if len(res.Unresolved) != 0 {
		t.Errorf("unexpected unresolved with no schema table: %+v", res.Unresolved)
	}
}

// TestSUL_SchemaSweep_ResolvesWorkedExample exercises the sweep half against
// a real discovered schema asset (Tier MS.0) — ported from
// internal/linker/schema_url_table_test.go's widgetJSON fixture, now driven
// end to end through the framework instead of calling LoadSchemaURLTables
// directly.
func TestSUL_SchemaSweep_ResolvesWorkedExample(t *testing.T) {
	t.Parallel()
	const widgetJSON = `{
  "resources": {
    "widget":  { "endpoint": "/api/widgets", "reorder": "/api/widgets/:id/reorder" },
    "gadget":  { "endpoint": "/api/gadgets", "reorder": "/api/gadgets/:id/reorder" },
    "sprocket":{ "endpoint": "/api/sprockets", "update": "/api/sprockets/{id}" }
  }
}`
	src := `export function go(resources) {
  fetch(resources.widget.reorder);
}
`
	_, files := sulWriteFixture(t, map[string]string{
		"config/resources.json": widgetJSON,
		"a.jsx":                 src,
	})
	var jsFile string
	for _, f := range files {
		if filepath.Ext(f) == ".jsx" {
			jsFile = f
		}
	}
	handlerNodes := []graph.Node{
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets"}},
		{ID: "h2", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets/:id/reorder"}},
		{ID: "h3", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets"}},
		{ID: "h4", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets/:id/reorder"}},
		{ID: "h5", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets"}},
		{ID: "h6", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets/{id}"}},
	}
	client := sulSchemaClient(jsFile, 2, "PUT", `resources.widget.reorder`)
	nodes := append(append([]graph.Node{}, handlerNodes...), client)

	res := sulRunSweep(t, nodes, files)
	if len(res.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %+v", res.Unresolved)
	}
	if len(res.Patches) != 1 {
		t.Fatalf("patches = %d, want 1: %+v", len(res.Patches), res.Patches)
	}
	p := res.Patches[0]
	if p.ID != "c" {
		t.Errorf("patch ID = %q, want c", p.ID)
	}
	if p.Meta["url"] != "/api/widgets/*/reorder" {
		t.Errorf("patch url = %q", p.Meta["url"])
	}
	if p.Meta["schema_entity"] != "widget" || p.Meta["schema_key"] != "reorder" {
		t.Errorf("patch provenance = %+v", p.Meta)
	}
	if p.Label != "PUT /api/widgets/*/reorder" {
		t.Errorf("patch label = %q", p.Label)
	}
}

// TestSUL_SchemaSweep_CrossComponentPropResolves is Tier RC.5 (MS.3 kind 2,
// docs/js-declarative-composition-cluster-plan.md): CreateModal reads
// `this.props.schema.reorder` directly — no local pin, no URL-only prop to
// cross the way kind 1 (TestSUL_SchemaSweep_ResolvesWorkedExample's sibling
// in js_prop_crossings_test.go) does. Parent renders
// `<CreateModal schema={resources.widget} />`; resolver.ResolveURLExpr alone
// cannot pin "schema" to an entity (it is a prop name, not a local
// binding), so the sweep hub's new cross-component fallback must resolve it
// through the same "jsx_attribute" crossing rule UB.2 uses, then pin the
// PRODUCER's attribute value and look the (entity, key) up in the SAME
// table.
func TestSUL_SchemaSweep_CrossComponentPropResolves(t *testing.T) {
	t.Parallel()
	// >=5 corroborated paths, the discovery gate's default threshold
	// (internal/schemaurl/table.go's defaultMinCorroboratedPaths) — the
	// same reason TestSUL_SchemaSweep_ResolvesWorkedExample's fixture above
	// carries 3 entities x 2 keys rather than just "widget".
	const widgetJSON = `{
  "resources": {
    "widget":  { "endpoint": "/api/widgets", "reorder": "/api/widgets/:id/reorder" },
    "gadget":  { "endpoint": "/api/gadgets", "reorder": "/api/gadgets/:id/reorder" },
    "sprocket":{ "endpoint": "/api/sprockets", "update": "/api/sprockets/{id}" }
  }
}`
	const parentSrc = `import CreateModal from "./CreateModal";
class Parent extends React.Component {
  render() {
    return <CreateModal schema={resources.widget} />;
  }
}
export default Parent;
`
	const childSrc = `class CreateModal extends React.Component {
  save() {
    fetch(this.props.schema.reorder);
  }
}
export default CreateModal;
`
	paths, files := sulWriteFixture(t, map[string]string{
		"config/resources.json": widgetJSON,
		"Parent.jsx":            parentSrc,
		"CreateModal.jsx":       childSrc,
	})
	handlerNodes := []graph.Node{
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets"}},
		{ID: "h2", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets/:id/reorder"}},
		{ID: "h3", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets"}},
		{ID: "h4", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets/:id/reorder"}},
		{ID: "h5", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets"}},
		{ID: "h6", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets/{id}"}},
	}
	owner := graph.Node{
		ID: "owner", Type: graph.NodeTypeClass, Label: "CreateModal", Service: "svc",
		File: paths["CreateModal.jsx"], Line: 1, Language: "javascript",
	}
	client := sulSchemaClient(paths["CreateModal.jsx"], 3, "GET", `this.props.schema.reorder`)
	nodes := append(append([]graph.Node{owner}, handlerNodes...), client)

	res := sulRunSweep(t, nodes, files)
	if len(res.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %+v", res.Unresolved)
	}
	if len(res.Patches) != 1 {
		t.Fatalf("patches = %d, want 1: %+v", len(res.Patches), res.Patches)
	}
	p := res.Patches[0]
	if p.ID != "c" {
		t.Errorf("patch ID = %q, want c", p.ID)
	}
	if p.Meta["url"] != "/api/widgets/*/reorder" {
		t.Errorf("patch url = %q", p.Meta["url"])
	}
	if p.Meta["schema_entity"] != "widget" || p.Meta["schema_key"] != "reorder" {
		t.Errorf("patch provenance = %+v", p.Meta)
	}
	if p.Label != "GET /api/widgets/*/reorder" {
		t.Errorf("patch label = %q", p.Label)
	}
}

// TestSUL_SchemaSweep_CrossComponentPropDestructuredResolves is the same
// shape as TestSUL_SchemaSweep_CrossComponentPropResolves, but the child
// destructures `schema` out of `this.props` first instead of reading
// `this.props.schema.<key>` directly — cedar's actual 5
// schema_entity_unresolved sites are ALL this shape, not the direct member
// chain (a gap the first version of RC.5 missed: PropsKeyRead originally
// required the receiver to be a `this.props.<Prop>` member expression, and
// a destructured local is a bare identifier). Confirmed against the real
// corpus after this fixture: 0-diff before the fix, 5 sites recovered
// after — see docs/js-declarative-composition-cluster-plan.md's RC.5
// section.
func TestSUL_SchemaSweep_CrossComponentPropDestructuredResolves(t *testing.T) {
	t.Parallel()
	const widgetJSON = `{
  "resources": {
    "widget":  { "endpoint": "/api/widgets", "reorder": "/api/widgets/:id/reorder" },
    "gadget":  { "endpoint": "/api/gadgets", "reorder": "/api/gadgets/:id/reorder" },
    "sprocket":{ "endpoint": "/api/sprockets", "update": "/api/sprockets/{id}" }
  }
}`
	const parentSrc = `import CreateModal from "./CreateModal";
class Parent extends React.Component {
  render() {
    return <CreateModal schema={resources.widget} />;
  }
}
export default Parent;
`
	const childSrc = `class CreateModal extends React.Component {
  save() {
    const { schema } = this.props;
    fetch(schema.reorder);
  }
}
export default CreateModal;
`
	paths, files := sulWriteFixture(t, map[string]string{
		"config/resources.json": widgetJSON,
		"Parent.jsx":            parentSrc,
		"CreateModal.jsx":       childSrc,
	})
	handlerNodes := []graph.Node{
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets"}},
		{ID: "h2", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets/:id/reorder"}},
		{ID: "h3", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets"}},
		{ID: "h4", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets/:id/reorder"}},
		{ID: "h5", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets"}},
		{ID: "h6", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets/{id}"}},
	}
	owner := graph.Node{
		ID: "owner", Type: graph.NodeTypeClass, Label: "CreateModal", Service: "svc",
		File: paths["CreateModal.jsx"], Line: 1, Language: "javascript",
	}
	client := sulSchemaClient(paths["CreateModal.jsx"], 4, "GET", `schema.reorder`)
	nodes := append(append([]graph.Node{owner}, handlerNodes...), client)

	res := sulRunSweep(t, nodes, files)
	if len(res.Unresolved) != 0 {
		t.Fatalf("unexpected unresolved: %+v", res.Unresolved)
	}
	if len(res.Patches) != 1 {
		t.Fatalf("patches = %d, want 1: %+v", len(res.Patches), res.Patches)
	}
	p := res.Patches[0]
	if p.Meta["url"] != "/api/widgets/*/reorder" {
		t.Errorf("patch url = %q", p.Meta["url"])
	}
	if p.Meta["schema_entity"] != "widget" || p.Meta["schema_key"] != "reorder" {
		t.Errorf("patch provenance = %+v", p.Meta)
	}
}

// TestSUL_SchemaSweep_CrossComponentPropAmbiguousLedgers is the same shape
// as above, but TWO parents render CreateModal with a DIFFERENT entity
// pinned to `schema` — the join cannot pick one, so RC.5 must ledger
// schema_entity_ambiguous rather than guessing.
func TestSUL_SchemaSweep_CrossComponentPropAmbiguousLedgers(t *testing.T) {
	t.Parallel()
	const widgetJSON = `{
  "resources": {
    "widget":  { "endpoint": "/api/widgets", "reorder": "/api/widgets/:id/reorder" },
    "gadget":  { "endpoint": "/api/gadgets", "reorder": "/api/gadgets/:id/reorder" },
    "sprocket":{ "endpoint": "/api/sprockets", "update": "/api/sprockets/{id}" }
  }
}`
	const parentASrc = `class ParentA extends React.Component {
  render() {
    return <CreateModal schema={resources.widget} />;
  }
}
export default ParentA;
`
	const parentBSrc = `class ParentB extends React.Component {
  render() {
    return <CreateModal schema={resources.gadget} />;
  }
}
export default ParentB;
`
	const childSrc = `class CreateModal extends React.Component {
  save() {
    fetch(this.props.schema.reorder);
  }
}
export default CreateModal;
`
	paths, files := sulWriteFixture(t, map[string]string{
		"config/resources.json": widgetJSON,
		"ParentA.jsx":           parentASrc,
		"ParentB.jsx":           parentBSrc,
		"CreateModal.jsx":       childSrc,
	})
	handlerNodes := []graph.Node{
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets"}},
		{ID: "h2", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets/:id/reorder"}},
		{ID: "h3", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets"}},
		{ID: "h4", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets/:id/reorder"}},
		{ID: "h5", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets"}},
		{ID: "h6", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets/{id}"}},
	}
	owner := graph.Node{
		ID: "owner", Type: graph.NodeTypeClass, Label: "CreateModal", Service: "svc",
		File: paths["CreateModal.jsx"], Line: 1, Language: "javascript",
	}
	client := sulSchemaClient(paths["CreateModal.jsx"], 3, "GET", `this.props.schema.reorder`)
	nodes := append(append([]graph.Node{owner}, handlerNodes...), client)

	res := sulRunSweep(t, nodes, files)
	if len(res.Patches) != 0 {
		t.Fatalf("expected no patch on an ambiguous producer entity: %+v", res.Patches)
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Kind != "schema_entity_ambiguous" {
		t.Fatalf("unresolved = %+v, want one schema_entity_ambiguous", res.Unresolved)
	}
}

// TestSUL_PropClient_CrossComponentAccessorResolves is Tier RC.5's SECOND
// real shape, found only after measuring against the real corpus this tier
// targets: cedar's actual 5 schema_entity_unresolved sites are ALL an
// ACCESSOR CALL whose schema argument is destructured from props
// (`Validation.getDeleteURL(schema, urlData)`, `schema` from
// `const { schema } = this.props`), not the direct member-chain shape
// TestSUL_SchemaSweep_CrossComponentProp*Resolves above cover. That shape
// lives in the OTHER framework (schema_url_link_props / SPA.4's
// sulPropClientFacts, ResolveURLExpr's resolveAccessorCall branch), not the
// sweep. A first version of this fix only touched the sweep and measured
// ZERO change against cedar — this test is the regression guard for that.
func TestSUL_PropClient_CrossComponentAccessorResolves(t *testing.T) {
	t.Parallel()
	const widgetJSON = `{
  "resources": {
    "widget":  { "endpoint": "/api/widgets", "reorder": "/api/widgets/:id/reorder" },
    "gadget":  { "endpoint": "/api/gadgets", "reorder": "/api/gadgets/:id/reorder" },
    "sprocket":{ "endpoint": "/api/sprockets", "update": "/api/sprockets/{id}" }
  }
}`
	const accessorSrc = `function getCreateURL(schema, extra) {
  return schema.reorder;
}
`
	const parentSrc = `class Parent extends React.Component {
  render() {
    return <T schema={resources.widget} />;
  }
}
export default Parent;
`
	const childSrc = `import ComponentWithAjaxStatus from "../common/ComponentWithAjaxStatus";
class T extends React.Component {
  load() {
    const { schema } = this.props;
    this.props.ajaxStatus.get("loading", getCreateURL(schema, {}));
  }
}
export default ComponentWithAjaxStatus(T);
`
	paths, files := sulWriteFixture(t, map[string]string{
		"common/ComponentWithAjaxStatus.jsx": sulAjaxStatusHOC,
		"config/resources.json":              widgetJSON,
		"Accessor.jsx":                       accessorSrc,
		"Parent.jsx":                         parentSrc,
		"grids/T.jsx":                        childSrc,
	})
	handlerNodes := []graph.Node{
		{ID: "h1", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets"}},
		{ID: "h2", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/widgets/:id/reorder"}},
		{ID: "h3", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets"}},
		{ID: "h4", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/gadgets/:id/reorder"}},
		{ID: "h5", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets"}},
		{ID: "h6", Type: graph.NodeTypeHTTPHandler, Service: "svc", Meta: map[string]string{"path": "/api/sprockets/{id}"}},
	}
	owner := graph.Node{
		ID: "owner", Type: graph.NodeTypeClass, Label: "T", Service: "svc",
		File: paths["grids/T.jsx"], Line: 1, Language: "javascript",
	}
	nodes := append([]graph.Node{owner}, handlerNodes...)

	res := sulRun(t, nodes, files)
	if len(res.Unresolved) != 0 {
		t.Fatalf("unexpected ledger: %+v", res.Unresolved)
	}
	if got := sulURLsOf(res.Nodes); len(got) != 1 || got[0] != "/api/widgets/*/reorder" {
		t.Fatalf("urls = %v, want [/api/widgets/*/reorder]", got)
	}
	var hc *graph.Node
	for i := range res.Nodes {
		if res.Nodes[i].Type == graph.NodeTypeHTTPClient {
			hc = &res.Nodes[i]
		}
	}
	if hc.Meta["schema_entity"] != "widget" || hc.Meta["schema_key"] != "reorder" {
		t.Errorf("mint provenance = %+v", hc.Meta)
	}
}
