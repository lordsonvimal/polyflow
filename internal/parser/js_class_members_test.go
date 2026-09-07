package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// JCM.1 — class members (including arrow-function class fields) become
// first-class function nodes, wired to the class with `contains` edges.

const jsClassMemberFixture = `import React from "react";

function helper() { return 1; }

class Grid extends React.Component {
  static displayName = "Grid";
  count = 0;
  handleClick = (e) => {
    helper();
    this.finish();
  };
  handleKey = async function (e) {
    return e;
  };
  finish() {
    helper();
  }
  get ready() {
    return this.count > 0;
  }
  render() {
    return null;
  }
}

export default Grid;
`

func parseJSClassMembers(t *testing.T, ext, grammar string) ([]graph.Node, []graph.Edge) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "Grid"+ext)
	if err := os.WriteFile(file, []byte(jsClassMemberFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(file)
	nodes, edges, _, _ := extractJSVariables(file, "web", "javascript", grammar, src, nil)
	return nodes, edges
}

func TestJSClassArrowFieldMethodsBecomeNodes(t *testing.T) {
	nodes, edges := parseJSClassMembers(t, ".jsx", "tsx")

	for _, name := range []string{"handleClick", "handleKey", "finish", "render", "ready"} {
		n := jsNode(nodes, graph.NodeTypeFunction, name)
		if n == nil {
			t.Fatalf("expected a function node for class member %q", name)
			continue
		}
		if n.Meta["class"] != "Grid" {
			t.Errorf("member %q: Meta[class] = %q, want Grid", name, n.Meta["class"])
		}
		if jsEdge(edges, graph.EdgeTypeContains, "class:Grid", "function:"+name) == nil {
			t.Errorf("expected class:Grid -contains-> function:%s", name)
		}
	}

	// The accessor is flagged so classifyRoot doesn't read it as dead code.
	if n := jsNode(nodes, graph.NodeTypeFunction, "ready"); n != nil && n.Meta["js_accessor"] != "true" {
		t.Errorf("getter `ready`: Meta[js_accessor] = %q, want true", n.Meta["js_accessor"])
	}

	// A pure-data field is not a function node.
	if n := jsNode(nodes, graph.NodeTypeFunction, "count"); n != nil {
		t.Errorf("data field `count` must not get a function node")
	}
}

func TestJSClassMembersBothGrammars(t *testing.T) {
	// The tsx grammar names the field child `name`; the plain-JS grammar
	// names it `property`. Both must yield the handleClick node.
	for _, tc := range []struct{ ext, grammar string }{
		{".jsx", "tsx"},
		{".js", "javascript"},
	} {
		nodes, edges := parseJSClassMembers(t, tc.ext, tc.grammar)
		if jsNode(nodes, graph.NodeTypeFunction, "handleClick") == nil {
			t.Errorf("%s/%s: no function node for arrow-field member handleClick", tc.ext, tc.grammar)
		}
		if jsEdge(edges, graph.EdgeTypeContains, "class:Grid", "function:handleClick") == nil {
			t.Errorf("%s/%s: no class:Grid -contains-> handleClick edge", tc.ext, tc.grammar)
		}
	}
}

// JCM.2 — class-member body attribution + intra-class this.x resolution.

const jsIntraClassFixture = `import React from "react";

function apiPost(x) { return x; }

class Widget extends React.Component {
  count = 0;
  constructor(props) {
    super(props);
    this.init();
  }
  init() {
    this.count = 1;
    apiPost(this.count);
  }
  save = () => {
    apiPost("save");
    return this.count;
  };
  render() { return null; }
}

export default Widget;
`

func parseJSIntra(t *testing.T, ext, grammar string) ([]graph.Node, []graph.Edge) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "Widget"+ext)
	if err := os.WriteFile(file, []byte(jsIntraClassFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(file)
	nodes, edges, _, _ := extractJSVariables(file, "web", "javascript", grammar, src, nil)
	return nodes, edges
}

func TestJSIntraClassThisCall(t *testing.T) {
	_, edges := parseJSIntra(t, ".jsx", "tsx")
	if jsEdge(edges, graph.EdgeTypeCalls, "function:constructor", "function:init") == nil {
		t.Error("expected constructor -calls-> init (via this_member)")
	}
}

func TestJSThisFieldReadWrite(t *testing.T) {
	nodes, edges := parseJSIntra(t, ".jsx", "tsx")
	if n := jsNode(nodes, graph.NodeTypeVariable, "count"); n == nil {
		t.Fatal("expected a variable node for data field `count`")
	} else if n.Meta["class"] != "Widget" {
		t.Errorf("count field: Meta[class] = %q, want Widget", n.Meta["class"])
	}
	// The write/read edges prove the member bodies self-attribute (arrow-field
	// `save` and plain method `init`), not fall through to the module node.
	if jsEdge(edges, graph.EdgeTypeWrites, "function:init", "variable:count") == nil {
		t.Error("expected init -writes-> count")
	}
	if jsEdge(edges, graph.EdgeTypeReads, "function:save", "variable:count") == nil {
		t.Error("expected save -reads-> count")
	}
	for _, e := range edges {
		if (e.Type == graph.EdgeTypeReads || e.Type == graph.EdgeTypeWrites) &&
			contains(e.To, "variable:count") && contains(e.From, ":(module):") {
			t.Errorf("this.count access must not attribute to (module): %s", e.From)
		}
	}
}

func TestJSClassMetaFieldsPopulated(t *testing.T) {
	nodes, _ := parseJSClassMembers(t, ".jsx", "tsx")
	c := jsNode(nodes, graph.NodeTypeClass, "Grid")
	if c == nil {
		t.Fatal("no class node for Grid")
	}
	if !contains(c.Meta["fields"], "handleClick") {
		t.Errorf("class Meta[fields] = %q, want it to list handleClick", c.Meta["fields"])
	}
	if !contains(c.Meta["methods"], "render") {
		t.Errorf("class Meta[methods] = %q, want it to list render", c.Meta["methods"])
	}
}
