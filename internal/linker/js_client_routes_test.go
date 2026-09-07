package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func crHandlerNode(svc, file, label, method, path string, line int) graph.Node {
	return graph.Node{
		ID:       svc + ":" + file + ":http_handler:" + label,
		Type:     graph.NodeTypeHTTPHandler,
		Label:    label,
		Service:  svc,
		File:     file,
		Line:     line,
		Language: "ruby",
		Meta:     map[string]string{"method": method, "path": path},
	}
}

// anchorNode gives a fixture file a service association so svcOfFile resolves.
func anchorNode(svc, file string) graph.Node {
	return graph.Node{
		ID:       svc + ":" + file + ":function:(module):1",
		Type:     graph.NodeTypeFunction,
		Label:    "(module)",
		Service:  svc,
		File:     file,
		Line:     1,
		Language: "javascript",
	}
}

func crNodesByLabel(nodes []graph.Node) map[string]graph.Node {
	m := make(map[string]graph.Node)
	for _, n := range nodes {
		if n.Type == graph.NodeTypeClientRoute {
			m[n.Label] = n
		}
	}
	return m
}

func hasEdge(edges []graph.Edge, typ graph.EdgeType, from, to string) bool {
	for _, e := range edges {
		if e.Type == typ && e.From == from && e.To == to {
			return true
		}
	}
	return false
}

// TestLinkJSClientRoutes_TableAndSwitch: an object route table + a
// `switch (routeName)` in a second file → one client_route node per entry and a
// `renders` edge to the case component.
func TestLinkJSClientRoutes_TableAndSwitch(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
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
		anchorNode("svc", rf),
		anchorNode("svc", nf),
		jsClassNode("svc", cf, "CDMTopLevel", 1, 3),
		jsClassNode("svc", cf, "CodeListTopLevelNew", 5, 7),
	}
	nodes, edges, _, _ := LinkJSClientRoutes(in, map[string][]string{"svc": {rf, nf}})

	byLabel := crNodesByLabel(nodes)
	for _, name := range []string{"cdm", "codelists", "contents"} {
		if _, ok := byLabel[name]; !ok {
			t.Fatalf("missing client_route %q; got %+v", name, nodes)
		}
	}
	if got := byLabel["cdm"].Meta["path"]; got != "/standards/*" {
		t.Errorf("cdm path = %q, want /standards/*", got)
	}
	if byLabel["cdm"].Meta["hash"] != "true" {
		t.Errorf("cdm hash = %q", byLabel["cdm"].Meta["hash"])
	}
	if !hasEdge(edges, graph.EdgeTypeRenders, byLabel["cdm"].ID, "svc:"+cf+":class:CDMTopLevel:1") {
		t.Errorf("no renders cdm -> CDMTopLevel; edges=%+v", edges)
	}
	if !hasEdge(edges, graph.EdgeTypeRenders, byLabel["codelists"].ID, "svc:"+cf+":class:CodeListTopLevelNew:5") {
		t.Errorf("no renders codelists -> CodeListTopLevelNew")
	}
	if hasEdge(edges, graph.EdgeTypeRenders, byLabel["contents"].ID, "") {
		t.Errorf("contents (plain div) should have no renders edge")
	}
}

// TestLinkJSClientRoutes_HashAndPlainPatterns: `#/x/:id` and `/x/:id` both
// normalise to `/x/*`.
func TestLinkJSClientRoutes_HashAndPlainPatterns(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"routes.jsx": `export default {
  hashed: "#/x/:id",
  plain: "/x/:id",
  other: "/y/:slug",
};
`,
	})
	rf := p["routes.jsx"]
	nodes, _, _, _ := LinkJSClientRoutes([]graph.Node{anchorNode("svc", rf)}, map[string][]string{"svc": {rf}})
	byLabel := crNodesByLabel(nodes)
	if byLabel["hashed"].Meta["path"] != "/x/*" {
		t.Errorf("hashed path = %q, want /x/*", byLabel["hashed"].Meta["path"])
	}
	if byLabel["plain"].Meta["path"] != "/x/*" {
		t.Errorf("plain path = %q, want /x/*", byLabel["plain"].Meta["path"])
	}
}

// TestLinkJSClientRoutes_NavToHandler: an http_handler whose path shape equals a
// client_route's → `http_handler --navigates_to--> client_route`.
func TestLinkJSClientRoutes_NavToHandler(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"routes.jsx": `export default {
  foo: "/x/:id",
  bar: "/y/:id",
  baz: "/z/:id",
};
`,
	})
	rf := p["routes.jsx"]
	h := crHandlerNode("svc", "config/routes.rb", "x#show", "GET", "/x/:id", 4)
	post := crHandlerNode("svc", "config/routes.rb", "x#update", "POST", "/x/:id", 5)

	nodes, edges, _, _ := LinkJSClientRoutes(
		[]graph.Node{anchorNode("svc", rf), h, post},
		map[string][]string{"svc": {rf}},
	)
	foo := crNodesByLabel(nodes)["foo"]
	if foo.ID == "" {
		t.Fatal("no client_route foo")
	}
	if !hasEdge(edges, graph.EdgeTypeNavigatesTo, h.ID, foo.ID) {
		t.Errorf("no navigates_to GET handler -> foo; edges=%+v", edges)
	}
	if hasEdge(edges, graph.EdgeTypeNavigatesTo, post.ID, foo.ID) {
		t.Errorf("POST handler should not navigate_to a client route")
	}
}

// TestLinkJSClientRoutes_NoTableNoCrash: a file with no recognisable route table
// is a no-op.
func TestLinkJSClientRoutes_NoTableNoCrash(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"config.jsx":    `export default { timeout: 30, retries: 3, name: "api" };`,
		"Component.jsx": `export default function Widget() { return <div/>; }`,
	})
	var files []string
	for _, v := range p {
		files = append(files, v)
	}
	nodes, edges, _, _ := LinkJSClientRoutes(
		[]graph.Node{anchorNode("svc", p["config.jsx"]), anchorNode("svc", p["Component.jsx"])},
		map[string][]string{"svc": files},
	)
	if len(nodes) != 0 || len(edges) != 0 {
		t.Errorf("expected no output, got nodes=%+v edges=%+v", nodes, edges)
	}
}

// TestFeatureRegistry_ObjectLiteral (SPA.3): `export default { Foo }` registry +
// `const C = getFeatureComponent("Foo"); <C/>` → `renders` from the enclosing
// function to the Foo component node.
func TestFeatureRegistry_ObjectLiteral(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
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
	foo := jsClassNode("svc", "components/Foo.jsx", "Foo", 1, 3)
	pageFn := jsFuncNode("svc", pf, "Page", 1)

	nodes, edges, _, resolved := LinkJSClientRoutes(
		[]graph.Node{anchorNode("svc", rf), anchorNode("svc", pf), foo, pageFn},
		map[string][]string{"svc": {rf, pf}},
	)
	if !hasEdge(edges, graph.EdgeTypeRenders, pageFn.ID, foo.ID) {
		t.Errorf("no renders Page -> Foo; edges=%+v nodes=%+v", edges, nodes)
	}
	if !resolved["svc\x00Foo"] {
		t.Errorf("Foo not marked resolved: %v", resolved)
	}
}

// TestFeatureRegistry_BarrelDefaultExport (SPA.3): a registry in one file mapping
// a key to a differently-named impl, accessed via `R["key"]` in another file of
// the same service.
func TestFeatureRegistry_BarrelDefaultExport(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"barrel.jsx": `export default { Widget: WidgetImpl, Panel: PanelImpl };`,
		"host.jsx": `import componentRegistry from "./barrel";
function Host() {
  const X = componentRegistry["Widget"];
  return <X />;
}
`,
	})
	bf, hf := p["barrel.jsx"], p["host.jsx"]
	impl := jsClassNode("svc", "components/WidgetImpl.jsx", "WidgetImpl", 1, 3)
	hostFn := jsFuncNode("svc", hf, "Host", 1)

	_, edges, _, _ := LinkJSClientRoutes(
		[]graph.Node{anchorNode("svc", bf), anchorNode("svc", hf), impl, hostFn},
		map[string][]string{"svc": {bf, hf}},
	)
	if !hasEdge(edges, graph.EdgeTypeRenders, hostFn.ID, impl.ID) {
		t.Errorf("no renders Host -> WidgetImpl; edges=%+v", edges)
	}
}

// TestFeatureRegistry_UnknownKey (SPA.3): a non-literal lookup key is ledgered
// `feature_component_dynamic` and emits no edge.
func TestFeatureRegistry_UnknownKey(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
		"f.jsx": `function F(someVar) {
  const c = getFeatureComponent(someVar);
  return <div>{c}</div>;
}
`,
	})
	ff := p["f.jsx"]
	nodes, edges, ledger, _ := LinkJSClientRoutes(
		[]graph.Node{anchorNode("svc", ff), jsFuncNode("svc", ff, "F", 1)},
		map[string][]string{"svc": {ff}},
	)
	if len(nodes) != 0 || len(edges) != 0 {
		t.Errorf("dynamic key must emit nothing; nodes=%+v edges=%+v", nodes, edges)
	}
	found := false
	for _, u := range ledger {
		if u.Kind == "feature_component_dynamic" {
			found = true
		}
	}
	if !found {
		t.Errorf("no feature_component_dynamic ledger entry; got %+v", ledger)
	}
}

// TestFeatureRegistry_RouteSwitchCase (SPA.3): a `getFeatureComponent("X")` in a
// `switch (routeName)` case whose key names no in-corpus component mints an
// external component node and wires `client_route --renders--> it` (not an
// enclosing-function edge).
func TestFeatureRegistry_RouteSwitchCase(t *testing.T) {
	t.Parallel()
	_, p := writeReduxFixture(t, map[string]string{
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
	nodes, edges, _, resolved := LinkJSClientRoutes(
		[]graph.Node{anchorNode("svc", rf), anchorNode("svc", nf), jsFuncNode("svc", nf, "renderRoute", 1)},
		map[string][]string{"svc": {rf, nf}},
	)
	var synth graph.Node
	for _, n := range nodes {
		if n.Type == graph.NodeTypeComponent && n.Label == "PKPDTopLevel" {
			synth = n
		}
	}
	if synth.ID == "" {
		t.Fatalf("no synthetic PKPDTopLevel component node; nodes=%+v", nodes)
	}
	if synth.Meta["external"] != "true" {
		t.Errorf("synthetic node not marked external: %+v", synth.Meta)
	}
	route := crNodesByLabel(nodes)["pkpdProjects"]
	if !hasEdge(edges, graph.EdgeTypeRenders, route.ID, synth.ID) {
		t.Errorf("no renders pkpdProjects -> PKPDTopLevel; edges=%+v", edges)
	}
	if !resolved["svc\x00PKPDTopLevel"] {
		t.Errorf("PKPDTopLevel not marked resolved")
	}
}
