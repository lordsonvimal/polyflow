package valuegraphfacts

import (
	"os"
	"path/filepath"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/jsast"
	"github.com/lordsonvimal/polyflow/internal/valuegraph"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func jsSpec(t *testing.T) *valuegraph.Spec {
	t.Helper()
	s, err := valuegraph.EmbeddedSpec("javascript")
	if err != nil {
		t.Fatalf("EmbeddedSpec: %v", err)
	}
	return s
}

// findCall returns the first call_expression's first argument in root.
func findArg(root *sitter.Node, calleeName string) *sitter.Node {
	var found *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != nil || n == nil {
			return
		}
		if n.Type() == "call_expression" {
			if callee := n.ChildByFieldName("function"); callee != nil && callee.Type() == "identifier" {
				if args := n.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() > 0 {
					found = args.NamedChild(0)
					return
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return found
}

// TestResolve_LocalReassignment proves Resolve reproduces
// valuegraph.Engine's own local-reassignment resolution byte-for-byte —
// this is RC.1's parity gate (docs/js-declarative-composition-cluster-plan.md).
func TestResolve_LocalReassignment(t *testing.T) {
	jsast.EnableCache()
	defer jsast.DisableCache()

	dir := t.TempDir()
	file := writeFile(t, dir, "a.js", `
function run() {
  const url = "/api/widgets";
  fetch(url);
}
`)
	_, root, _, ok := jsast.Parse(file)
	if !ok {
		t.Fatal("parse failed")
	}
	arg := findArg(root, "fetch")
	if arg == nil {
		t.Fatal("no call argument found")
	}

	spec := jsSpec(t)
	results := Resolve(spec, []string{file}, nil, []Site{{File: file, Expr: arg}}, valuegraph.Options{})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	v := results[0].Value
	if v.Kind != valuegraph.KindLiteral || v.Text != "/api/widgets" {
		t.Fatalf("got %+v, want literal /api/widgets", v)
	}
}

// TestResolve_JSXPropCrossing proves the ComponentIndex-backed CrossSource
// lets Resolve follow a value across a JSX prop boundary, the same
// crossing internal/linker/valuegraph_adapter.go's jsPropFileSource enables
// today — the case RC.3/RC.5 build on.
func TestResolve_JSXPropCrossing(t *testing.T) {
	jsast.EnableCache()
	defer jsast.DisableCache()

	dir := t.TempDir()
	parentFile := writeFile(t, dir, "Parent.jsx", `
function Parent() {
  return <Child url={"/api/widgets"} />;
}
`)
	childFile := writeFile(t, dir, "Child.jsx", `
function Child(props) {
  fetch(props.url);
}
`)
	_, childRoot, _, ok := jsast.Parse(childFile)
	if !ok {
		t.Fatal("parse failed")
	}
	arg := findArg(childRoot, "fetch")
	if arg == nil {
		t.Fatal("no call argument found")
	}

	nodes := []graph.Node{
		{ID: "n1", Service: "web", File: parentFile, Type: graph.NodeTypeFunction, Label: "Parent"},
		{ID: "n2", Service: "web", File: childFile, Type: graph.NodeTypeFunction, Label: "Child"},
	}
	cross := BuildComponentIndex(nodes, "web")

	spec := jsSpec(t)
	results := Resolve(spec, []string{parentFile, childFile}, cross,
		[]Site{{File: childFile, Expr: arg, Owner: "Child"}}, valuegraph.Options{})
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	v := results[0].Value
	if v.Kind != valuegraph.KindLiteral || v.Text != "/api/widgets" {
		t.Fatalf("got %+v, want literal /api/widgets (crossed from Parent.jsx)", v)
	}
}

func TestBuildComponentIndex_CapitalizedFallback(t *testing.T) {
	nodes := []graph.Node{
		{ID: "n1", Service: "web", File: "Widget.jsx", Type: graph.NodeTypeFunction, Label: "Widget"},
		{ID: "n2", Service: "web", File: "helpers.js", Type: graph.NodeTypeFunction, Label: "helperFn"},
	}
	ci := BuildComponentIndex(nodes, "web")
	if got := ci.FilesForOwner("Widget"); len(got) != 1 || got[0] != "Widget.jsx" {
		t.Fatalf("FilesForOwner(Widget) = %v, want [Widget.jsx]", got)
	}
	if got := ci.FilesForOwner("helperFn"); got != nil {
		t.Fatalf("FilesForOwner(helperFn) = %v, want nil (lowercase, not a component)", got)
	}
}
