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
	nodes, edges := LinkJSClientRoutes(in, map[string][]string{"svc": {rf, nf}})

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
	nodes, _ := LinkJSClientRoutes([]graph.Node{anchorNode("svc", rf)}, map[string][]string{"svc": {rf}})
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

	nodes, edges := LinkJSClientRoutes(
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
	nodes, edges := LinkJSClientRoutes(
		[]graph.Node{anchorNode("svc", p["config.jsx"]), anchorNode("svc", p["Component.jsx"])},
		map[string][]string{"svc": files},
	)
	if len(nodes) != 0 || len(edges) != 0 {
		t.Errorf("expected no output, got nodes=%+v edges=%+v", nodes, edges)
	}
}
