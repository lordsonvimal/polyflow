package pipeline_test

// FX.8.10 (2026-09-15): js_client_routes — internal/linker/
// js_client_routes.go's retired LinkJSClientRoutes, migrated onto
// patterns/javascript/js_client_routes.yaml + rules/javascript/
// js_client_routes.dl, driven by the js_client_routes_sites hub provider
// (internal/factpipe/hub_js_client_routes.go). Real-parse tests (temp-dir
// fixture files, reusing js_hoc_test.go's jhWriteFixture helper, same package), porting the retired Go test's
// fixtures verbatim. RT.1's re-typed nodes are asserted via res.Patches +
// ID (which already embeds svc/file/label/line) rather than a
// File/Label field on NodePatch, since patch: intentionally never carries
// those — it overlays Meta onto whatever node the ID already names.

import (
	"fmt"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func jcrActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("js_client_routes")
	if fw == nil {
		t.Fatal("js_client_routes framework not embedded")
	}
	return []*pipeline.Framework{fw}
}

func jcrRun(t *testing.T, nodes []graph.Node, files []string) pipeline.Result {
	t.Helper()
	res, err := pipeline.Run(jcrActive(t), nil, graph.Snapshot{Nodes: nodes, Files: files})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func jcrHandlerNode(svc, file, label, method, path string, line int) graph.Node {
	return graph.Node{
		ID: svc + ":" + file + ":http_handler:" + label, Type: graph.NodeTypeHTTPHandler,
		Label: label, Service: svc, File: file, Line: line, Language: "ruby",
		Meta: map[string]string{"method": method, "path": path},
	}
}

// jcrAnchorNode gives a fixture file a service association so svcOfFile
// resolves.
func jcrAnchorNode(svc, file string) graph.Node {
	return graph.Node{
		ID: svc + ":" + file + ":function:(module):1", Type: graph.NodeTypeFunction,
		Label: "(module)", Service: svc, File: file, Line: 1, Language: "javascript",
	}
}

func jcrNodesByLabel(nodes []graph.Node) map[string]graph.Node {
	m := make(map[string]graph.Node)
	for _, n := range nodes {
		if n.Type == graph.NodeTypeRoute {
			m[n.Label] = n
		}
	}
	return m
}

func jcrHasEdge(edges []graph.Edge, typ graph.EdgeType, from, to string) bool {
	for _, e := range edges {
		if e.Type == typ && e.From == from && e.To == to {
			return true
		}
	}
	return false
}

func jcrPatchByID(res pipeline.Result) map[string]int {
	m := make(map[string]int, len(res.Patches))
	for i, p := range res.Patches {
		m[p.ID] = i
	}
	return m
}

func jcrResolvedSet(res pipeline.Result) map[string]bool {
	m := make(map[string]bool, len(res.Resolved))
	for _, r := range res.Resolved {
		m[r] = true
	}
	return m
}

// jcrClassNode / jcrFuncNode build class/function nodes with the
// line-suffixed ID format the retired js_client_routes_test.go's own
// jsClassNode/jsFuncNode (internal/linker/js_type_relations_test.go) used —
// distinct from js_hoc_test.go's jhClassNode/jhFuncNode, whose shorter
// (line-less) ID scheme is specific to that migration's own fixtures.
func jcrClassNode(svc, file, label string, line, endLine int) graph.Node {
	return graph.Node{
		ID: fmt.Sprintf("%s:%s:class:%s:%d", svc, file, label, line),
		Type: graph.NodeTypeClass, Label: label, Service: svc, File: file,
		Line: line, EndLine: endLine, Language: "javascript",
	}
}

func jcrFuncNode(svc, file, label string, line int) graph.Node {
	return graph.Node{
		ID: fmt.Sprintf("%s:%s:function:%s:%d", svc, file, label, line),
		Type: graph.NodeTypeFunction, Label: label, Service: svc, File: file,
		Line: line, Language: "javascript",
	}
}

func jcrNodesRenderedBy(edges []graph.Edge, to string) int {
	n := 0
	for _, e := range edges {
		if e.Type == graph.EdgeTypeRenders && e.To == to {
			n++
		}
	}
	return n
}

// TestJSClientRoutesRule_TableAndSwitch: an object route table + a
// `switch (routeName)` in a second file -> one route node per entry and a
// renders edge to the case component.
func TestJSClientRoutesRule_TableAndSwitch(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"common/ClientRoutes.jsx": `export default {
  cdm: "/standards/:standardId#cdm/:dataModelId(&t=:time)",
  codelists: "/standards/:standardId#code_lists(&t=:time)",
  contents: "/standards/:standardId#contents",
};
`,
		"common/navigation.jsx": `function renderRoute(routeName, params) {
  var kid;
  switch (routeName) {
    case "cdm":
      kid = <CDMTopLevel {...params} />;
      break;
    case "codelists":
      kid = <CodeListTopLevelNew {...params} />;
      break;
    case "contents":
      kid = <div>plain</div>;
      break;
  }
  return kid;
}
`,
	})
	rf := p["common/ClientRoutes.jsx"]
	nf := p["common/navigation.jsx"]
	cf := "components/CDM/CDMTopLevel.jsx"

	in := []graph.Node{
		jcrAnchorNode("svc", rf),
		jcrAnchorNode("svc", nf),
		jcrClassNode("svc", cf, "CDMTopLevel", 1, 3),
		jcrClassNode("svc", cf, "CodeListTopLevelNew", 5, 7),
	}
	res := jcrRun(t, in, []string{rf, nf})

	byLabel := jcrNodesByLabel(res.Nodes)
	for _, name := range []string{"cdm", "codelists", "contents"} {
		if _, ok := byLabel[name]; !ok {
			t.Fatalf("missing route %q; got %+v", name, res.Nodes)
		}
	}
	if got := byLabel["cdm"].Meta["path"]; got != "/standards/*" {
		t.Errorf("cdm path = %q, want /standards/*", got)
	}
	if byLabel["cdm"].Meta["hash"] != "true" {
		t.Errorf("cdm hash = %q", byLabel["cdm"].Meta["hash"])
	}
	if !jcrHasEdge(res.Edges, graph.EdgeTypeRenders, byLabel["cdm"].ID, "svc:"+cf+":class:CDMTopLevel:1") {
		t.Errorf("no renders cdm -> CDMTopLevel; edges=%+v", res.Edges)
	}
	if !jcrHasEdge(res.Edges, graph.EdgeTypeRenders, byLabel["codelists"].ID, "svc:"+cf+":class:CodeListTopLevelNew:5") {
		t.Errorf("no renders codelists -> CodeListTopLevelNew")
	}
	if jcrHasEdge(res.Edges, graph.EdgeTypeRenders, byLabel["contents"].ID, "") {
		t.Errorf("contents (plain div) should have no renders edge")
	}
}

// TestJSClientRoutesRule_HashAndPlainPatterns: `#/x/:id` and `/x/:id` both
// normalise to `/x/*`.
func TestJSClientRoutesRule_HashAndPlainPatterns(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"routes.jsx": `export default {
  hashed: "#/x/:id",
  plain: "/x/:id",
  other: "/y/:slug",
};
`,
	})
	rf := p["routes.jsx"]
	res := jcrRun(t, []graph.Node{jcrAnchorNode("svc", rf)}, []string{rf})
	byLabel := jcrNodesByLabel(res.Nodes)
	if byLabel["hashed"].Meta["path"] != "/x/*" {
		t.Errorf("hashed path = %q, want /x/*", byLabel["hashed"].Meta["path"])
	}
	if byLabel["plain"].Meta["path"] != "/x/*" {
		t.Errorf("plain path = %q, want /x/*", byLabel["plain"].Meta["path"])
	}
}

// TestJSClientRoutesRule_NavToHandler: an http_handler whose path shape
// equals a route's -> http_handler --navigates_to--> route.
func TestJSClientRoutesRule_NavToHandler(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"routes.jsx": `export default {
  foo: "/x/:id",
  bar: "/y/:id",
  baz: "/z/:id",
};
`,
	})
	rf := p["routes.jsx"]
	h := jcrHandlerNode("svc", "config/routes.rb", "x#show", "GET", "/x/:id", 4)
	post := jcrHandlerNode("svc", "config/routes.rb", "x#update", "POST", "/x/:id", 5)

	res := jcrRun(t, []graph.Node{jcrAnchorNode("svc", rf), h, post}, []string{rf})
	foo := jcrNodesByLabel(res.Nodes)["foo"]
	if foo.ID == "" {
		t.Fatal("no route foo")
	}
	if !jcrHasEdge(res.Edges, graph.EdgeTypeNavigatesTo, h.ID, foo.ID) {
		t.Errorf("no navigates_to GET handler -> foo; edges=%+v", res.Edges)
	}
	if jcrHasEdge(res.Edges, graph.EdgeTypeNavigatesTo, post.ID, foo.ID) {
		t.Errorf("POST handler should not navigate_to a client route")
	}
}

// TestJSClientRoutesRule_NoTableNoCrash: a file with no recognisable route
// table is a no-op.
func TestJSClientRoutesRule_NoTableNoCrash(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"config.jsx":    `export default { timeout: 30, retries: 3, name: "api" };`,
		"Component.jsx": `export default function Widget() { return <div/>; }`,
	})
	var files []string
	for _, v := range p {
		files = append(files, v)
	}
	res := jcrRun(t, []graph.Node{jcrAnchorNode("svc", p["config.jsx"]), jcrAnchorNode("svc", p["Component.jsx"])}, files)
	if len(res.Nodes) != 0 || len(res.Edges) != 0 {
		t.Errorf("expected no output, got nodes=%+v edges=%+v", res.Nodes, res.Edges)
	}
}

// TestJSClientRoutesRule_FeatureRegistryObjectLiteral (SPA.3): `export
// default { Foo }` registry + `const C = getFeatureComponent("Foo"); <C/>`
// -> renders from the enclosing function to the Foo component node.
func TestJSClientRoutesRule_FeatureRegistryObjectLiteral(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"registry.jsx": `import Foo from "./components/Foo";
export default { Foo, Bar };
`,
		"page.jsx": `function Page() {
  const C = getFeatureComponent("Foo");
  return <C />;
}
`,
	})
	rf, pf := p["registry.jsx"], p["page.jsx"]
	foo := jcrClassNode("svc", "components/Foo.jsx", "Foo", 1, 3)
	pageFn := jcrFuncNode("svc", pf, "Page", 1)

	res := jcrRun(t, []graph.Node{jcrAnchorNode("svc", rf), jcrAnchorNode("svc", pf), foo, pageFn}, []string{rf, pf})
	if !jcrHasEdge(res.Edges, graph.EdgeTypeRenders, pageFn.ID, foo.ID) {
		t.Errorf("no renders Page -> Foo; edges=%+v nodes=%+v", res.Edges, res.Nodes)
	}
	if !jcrResolvedSet(res)["svc\x00Foo"] {
		t.Errorf("Foo not marked resolved: %v", res.Resolved)
	}
}

// TestJSClientRoutesRule_FeatureRegistryBarrelDefaultExport (SPA.3): a
// registry in one file mapping a key to a differently-named impl, accessed
// via `R["key"]` in another file of the same service.
func TestJSClientRoutesRule_FeatureRegistryBarrelDefaultExport(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"barrel.jsx": `export default { Widget: WidgetImpl, Panel: PanelImpl };`,
		"host.jsx": `import componentRegistry from "./barrel";
function Host() {
  const X = componentRegistry["Widget"];
  return <X />;
}
`,
	})
	bf, hf := p["barrel.jsx"], p["host.jsx"]
	impl := jcrClassNode("svc", "components/WidgetImpl.jsx", "WidgetImpl", 1, 3)
	hostFn := jcrFuncNode("svc", hf, "Host", 1)

	res := jcrRun(t, []graph.Node{jcrAnchorNode("svc", bf), jcrAnchorNode("svc", hf), impl, hostFn}, []string{bf, hf})
	if !jcrHasEdge(res.Edges, graph.EdgeTypeRenders, hostFn.ID, impl.ID) {
		t.Errorf("no renders Host -> WidgetImpl; edges=%+v", res.Edges)
	}
}

// TestJSClientRoutesRule_FeatureRegistryUnknownKey (SPA.3): a non-literal
// lookup key is ledgered feature_component_dynamic and emits no edge.
func TestJSClientRoutesRule_FeatureRegistryUnknownKey(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"f.jsx": `function F(someVar) {
  const c = getFeatureComponent(someVar);
  return <div>{c}</div>;
}
`,
	})
	ff := p["f.jsx"]
	res := jcrRun(t, []graph.Node{jcrAnchorNode("svc", ff), jcrFuncNode("svc", ff, "F", 1)}, []string{ff})
	if len(res.Nodes) != 0 || len(res.Edges) != 0 {
		t.Errorf("dynamic key must emit nothing; nodes=%+v edges=%+v", res.Nodes, res.Edges)
	}
	found := false
	for _, u := range res.Unresolved {
		if u.Kind == "feature_component_dynamic" {
			found = true
		}
	}
	if !found {
		t.Errorf("no feature_component_dynamic ledger entry; got %+v", res.Unresolved)
	}
}

// TestJSClientRoutesRule_FeatureRegistryRouteSwitchCase (SPA.3): a
// `getFeatureComponent("X")` in a `switch (routeName)` case whose key names
// no in-corpus component mints an external component node and wires
// route --renders--> it (not an enclosing-function edge).
func TestJSClientRoutesRule_FeatureRegistryRouteSwitchCase(t *testing.T) {
	p := jhWriteFixture(t, map[string]string{
		"common/ClientRoutes.jsx": `export default {
  pkpdProjects: "/pkpd#projects",
  foo: "/foo/:id",
  bar: "/bar/:id",
};
`,
		"common/navigation.jsx": `function renderRoute(routeName, params) {
  var kid;
  switch (routeName) {
    case "pkpdProjects":
      var PKPDTopLevel = getFeatureComponent("PKPDTopLevel");
      kid = <PKPDTopLevel {...params} />;
      break;
  }
  return kid;
}
`,
	})
	rf, nf := p["common/ClientRoutes.jsx"], p["common/navigation.jsx"]
	res := jcrRun(t, []graph.Node{jcrAnchorNode("svc", rf), jcrAnchorNode("svc", nf), jcrFuncNode("svc", nf, "renderRoute", 1)}, []string{rf, nf})

	var synth graph.Node
	for _, n := range res.Nodes {
		if n.Type == graph.NodeTypeComponent && n.Label == "PKPDTopLevel" {
			synth = n
		}
	}
	if synth.ID == "" {
		t.Fatalf("no synthetic PKPDTopLevel component node; nodes=%+v", res.Nodes)
	}
	if synth.Meta["external"] != "true" {
		t.Errorf("synthetic node not marked external: %+v", synth.Meta)
	}
	route := jcrNodesByLabel(res.Nodes)["pkpdProjects"]
	if !jcrHasEdge(res.Edges, graph.EdgeTypeRenders, route.ID, synth.ID) {
		t.Errorf("no renders pkpdProjects -> PKPDTopLevel; edges=%+v", res.Edges)
	}
	if !jcrResolvedSet(res)["svc\x00PKPDTopLevel"] {
		t.Errorf("PKPDTopLevel not marked resolved")
	}
}

// --- Tier RT.1: SPA route render targets ---
//
// jcrVarComponentNode builds a `variable` node as the JS parser emits one
// for a `const Foo = ...` its own heuristics thought was a component. Only
// nodes carrying that stamp are eligible render targets in the first
// place, so RT.1's initialiser check is a second opinion on the parser's,
// not a replacement.
func jcrVarComponentNode(svc, file, label string, line int) graph.Node {
	return graph.Node{
		ID: fmt.Sprintf("%s:%s:variable:%s:%d", svc, file, label, line),
		Type: graph.NodeTypeVariable, Label: label, Service: svc, File: file, Line: line,
		Language: "javascript", Meta: map[string]string{"component": "true"},
	}
}

// jcrRTFixture wires four routes against the four declaration shapes that
// reach a route: a plain function, an arrow const, an HOC-wrapped const,
// and a const holding an object. Returns the pass's Result plus the
// route-table path.
func jcrRTFixture(t *testing.T) (res pipeline.Result, cf string) {
	t.Helper()
	p := jhWriteFixture(t, map[string]string{
		"common/ClientRoutes.jsx": `export default {
  plain: "/orion/:id#plain",
  arrow: "/orion/:id#arrow",
  wrapped: "/orion/:id#wrapped",
  settings: "/orion/:id#settings",
};
`,
		"common/navigation.jsx": `function renderRoute(routeName, params) {
  var kid;
  switch (routeName) {
    case "plain":
      kid = <PlainTopLevel {...params} />;
      break;
    case "arrow":
      kid = <ArrowTopLevel {...params} />;
      break;
    case "wrapped":
      kid = <WrappedTopLevel {...params} />;
      break;
    case "settings":
      kid = <SettingsConfig {...params} />;
      break;
  }
  return kid;
}
`,
		"components/TopLevels.jsx": `export function PlainTopLevel(props) {
  return <div>{props.id}</div>;
}

export const ArrowTopLevel = (props) => <div>{props.id}</div>;

const Inner = (props) => <div>{props.id}</div>;
export const WrappedTopLevel = connect(mapStateToProps)(Inner);

export const SettingsConfig = { tabs: ["a", "b"] };
`,
	})
	rf, nf, cf := p["common/ClientRoutes.jsx"], p["common/navigation.jsx"], p["components/TopLevels.jsx"]

	in := []graph.Node{
		jcrAnchorNode("svc", rf),
		jcrAnchorNode("svc", nf),
		jcrFuncNode("svc", nf, "renderRoute", 1),
		jcrFuncNode("svc", cf, "PlainTopLevel", 1),
		jcrVarComponentNode("svc", cf, "ArrowTopLevel", 5),
		jcrVarComponentNode("svc", cf, "WrappedTopLevel", 8),
		jcrVarComponentNode("svc", cf, "SettingsConfig", 10),
	}
	res = jcrRun(t, in, []string{rf, nf, cf})
	return res, cf
}

// TestJSClientRoutesRule_RetypedToComponent (RT.1): every declaration shape
// a route actually renders ends up patched to component type, reached by
// exactly one renders edge.
func TestJSClientRoutesRule_RetypedToComponent(t *testing.T) {
	res, cf := jcrRTFixture(t)
	patches := jcrPatchByID(res)

	for _, label := range []string{"ArrowTopLevel", "WrappedTopLevel"} {
		id := fmt.Sprintf("svc:%s:variable:%s:%d", cf, label, map[string]int{"ArrowTopLevel": 5, "WrappedTopLevel": 8}[label])
		idx, ok := patches[id]
		if !ok {
			t.Fatalf("%s not patched; patches=%+v", label, res.Patches)
		}
		p := res.Patches[idx]
		if p.Type != graph.NodeTypeComponent {
			t.Errorf("%s patch type = %q, want component", label, p.Type)
		}
		if p.Meta["retyped_from"] != string(graph.NodeTypeVariable) {
			t.Errorf("%s missing retyped_from provenance: %+v", label, p.Meta)
		}
	}
	plainID := fmt.Sprintf("svc:%s:function:PlainTopLevel:1", cf)
	if _, ok := patches[plainID]; ok {
		t.Errorf("PlainTopLevel patched; RT.1 only touches variable targets")
	}

	routes := jcrNodesByLabel(res.Nodes)
	for _, r := range []struct{ route, target string }{
		{"plain", "svc:" + cf + ":function:PlainTopLevel:1"},
		{"arrow", "svc:" + cf + ":variable:ArrowTopLevel:5"},
		{"wrapped", "svc:" + cf + ":variable:WrappedTopLevel:8"},
	} {
		if !jcrHasEdge(res.Edges, graph.EdgeTypeRenders, routes[r.route].ID, r.target) {
			t.Errorf("no renders %s -> %s; edges=%+v", r.route, r.target, res.Edges)
		}
	}
	for _, p := range res.Patches {
		if got := jcrNodesRenderedBy(res.Edges, p.ID); got != 1 {
			t.Errorf("%s is the target of %d renders edges, want 1", p.ID, got)
		}
	}
}

// TestJSClientRoutesRule_NonComponentStaysVariable (RT.1): the gate. A
// route naming a const that holds an object resolves to the right node and
// that node is not a component, so it is not patched and says so in the
// ledger.
func TestJSClientRoutesRule_NonComponentStaysVariable(t *testing.T) {
	res, cf := jcrRTFixture(t)

	settingsID := fmt.Sprintf("svc:%s:variable:SettingsConfig:10", cf)
	for _, p := range res.Patches {
		if p.ID == settingsID {
			t.Fatalf("an object literal was re-typed as a component: %+v", p)
		}
	}
	var row *graph.UnresolvedRef
	for i := range res.Unresolved {
		if res.Unresolved[i].Kind == "client_route_target_not_component" {
			row = &res.Unresolved[i]
		}
	}
	if row == nil {
		t.Fatalf("no client_route_target_not_component row; unresolved=%+v", res.Unresolved)
	}
	if row.Name != "SettingsConfig" || row.File != cf {
		t.Errorf("ledger row names the wrong node: %+v", *row)
	}

	routes := jcrNodesByLabel(res.Nodes)
	if !jcrHasEdge(res.Edges, graph.EdgeTypeRenders, routes["settings"].ID, settingsID) {
		t.Errorf("the renders edge to a non-component target was dropped")
	}
}

// TestJSClientRoutesRule_MintsNoSecondNode is the in-place-mutation gate:
// res.Nodes only ever carries route/external-component mints (RT.1 retypes
// live in res.Patches, never in res.Nodes), and no ID is patched twice.
func TestJSClientRoutesRule_MintsNoSecondNode(t *testing.T) {
	res, _ := jcrRTFixture(t)

	for _, n := range res.Nodes {
		if n.Type != graph.NodeTypeRoute {
			t.Errorf("js_client_routes minted a non-route node %s (%s); "+
				"re-typed targets belong in Patches, not Nodes", n.ID, n.Type)
		}
	}
	seen := map[string]bool{}
	for _, p := range res.Patches {
		if seen[p.ID] {
			t.Errorf("node %s patched twice", p.ID)
		}
		seen[p.ID] = true
	}
}
