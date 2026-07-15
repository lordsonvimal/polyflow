package parser

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// helper: find an edge in edges matching type, and substrings of from/to.
func jsEdgeI2(edges []graph.Edge, typ graph.EdgeType, fromSub, toSub string) *graph.Edge {
	for i := range edges {
		e := &edges[i]
		if e.Type == typ && contains(e.From, fromSub) && contains(e.To, toSub) {
			return e
		}
	}
	return nil
}

// helper: find a node by type and label.
func jsNodeI2(nodes []graph.Node, typ graph.NodeType, label string) *graph.Node {
	for i := range nodes {
		if nodes[i].Type == typ && nodes[i].Label == label {
			return &nodes[i]
		}
	}
	return nil
}

// TestJSI2_SameFileInherits: class Admin extends User in the same file
// produces a static inherits edge.
func TestJSI2_SameFileInherits(t *testing.T) {
	src := []byte(`
class User {
  greet() {}
}
class Admin extends User {
  ban() {}
}
`)
	nodes, edges, _ := extractJSVariables("svc.js", "svc", "javascript", "javascript", src)

	user := jsNodeI2(nodes, graph.NodeTypeClass, "User")
	if user == nil {
		t.Fatalf("missing class node User; nodes: %+v", nodes)
	}
	admin := jsNodeI2(nodes, graph.NodeTypeClass, "Admin")
	if admin == nil {
		t.Fatalf("missing class node Admin; nodes: %+v", nodes)
	}
	e := jsEdgeI2(edges, graph.EdgeTypeInherits, "class:Admin", "class:User")
	if e == nil {
		t.Fatalf("missing inherits edge Admin → User; edges: %+v", edges)
	}
	if e.Confidence != graph.ConfidenceStatic {
		t.Errorf("same-file inherits must be static, got %q", e.Confidence)
	}
	if e.Meta["via"] != "extends" {
		t.Errorf("inherits edge via should be 'extends', got %q", e.Meta["via"])
	}
}

// TestJSI2_UnresolvedExpressionSuperclass: class X extends mixin(Base) goes
// to the inherits_unresolved ledger, never emitted as an edge.
func TestJSI2_UnresolvedExpressionSuperclass(t *testing.T) {
	src := []byte(`
class X extends mixin(Base) {}
`)
	nodes, edges, unresolved := extractJSVariables("svc.js", "svc", "javascript", "javascript", src)
	_ = nodes

	// No inherits edge should exist.
	for _, e := range edges {
		if e.Type == graph.EdgeTypeInherits {
			t.Errorf("expression superclass must not produce an inherits edge, got: %+v", e)
		}
	}
	found := false
	for _, u := range unresolved {
		if u.Kind == "inherits_unresolved" {
			found = true
		}
	}
	if !found {
		t.Errorf("expression superclass must produce an inherits_unresolved ref; got: %+v", unresolved)
	}
}

// TestJSI2_TSImplements: a TS class with an implements clause produces an
// implements edge with nominal=true.
func TestJSI2_TSImplements(t *testing.T) {
	src := []byte(`
interface Greeter {
  greet(): void;
}
class English implements Greeter {
  greet() { return "hello"; }
}
`)
	nodes, edges, _ := extractJSVariables("svc.ts", "svc", "typescript", "typescript", src)

	iface := jsNodeI2(nodes, graph.NodeTypeInterface, "Greeter")
	if iface == nil {
		t.Fatalf("missing NodeTypeInterface node for Greeter; nodes: %+v", nodes)
	}
	e := jsEdgeI2(edges, graph.EdgeTypeImplements, "class:English", "interface:Greeter")
	if e == nil {
		t.Fatalf("missing implements edge English → Greeter; edges: %+v", edges)
	}
	if e.Confidence != graph.ConfidenceStatic {
		t.Errorf("same-file implements must be static, got %q", e.Confidence)
	}
	if e.Meta["nominal"] != "true" {
		t.Errorf("TS implements must be nominal=true, got %q", e.Meta["nominal"])
	}
}

// TestJSI2_InterfaceExtendsNoCallsEdge: interface_extends must NOT produce a
// calls edge between interfaces (regression guard for the old wrong mapping).
func TestJSI2_InterfaceExtendsNoCallsEdge(t *testing.T) {
	src := []byte(`
interface Base {
  id(): string;
}
interface Extended extends Base {
  name(): string;
}
`)
	nodes, edges, _ := extractJSVariables("svc.ts", "svc", "typescript", "typescript", src)

	// No calls edge between interface nodes.
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls {
			if contains(e.From, "interface:") || contains(e.To, "interface:") {
				t.Errorf("calls edge must not exist between interface nodes; got: %+v", e)
			}
		}
	}

	// Must have an inherits edge Extended → Base.
	e := jsEdgeI2(edges, graph.EdgeTypeInherits, "interface:Extended", "interface:Base")
	if e == nil {
		t.Fatalf("missing inherits edge Extended → Base; edges: %+v", edges)
	}

	// Both interface nodes must exist.
	if jsNodeI2(nodes, graph.NodeTypeInterface, "Base") == nil {
		t.Error("NodeTypeInterface node for Base missing")
	}
	if jsNodeI2(nodes, graph.NodeTypeInterface, "Extended") == nil {
		t.Error("NodeTypeInterface node for Extended missing")
	}
}

// TestJSI2_InterfaceMethodsMeta: interface nodes carry a methods meta field.
func TestJSI2_InterfaceMethodsMeta(t *testing.T) {
	src := []byte(`
interface Shape {
  area(): number;
  perimeter(): number;
}
`)
	nodes, _, _ := extractJSVariables("svc.ts", "svc", "typescript", "typescript", src)

	n := jsNodeI2(nodes, graph.NodeTypeInterface, "Shape")
	if n == nil {
		t.Fatalf("missing NodeTypeInterface node for Shape; nodes: %+v", nodes)
	}
	if !contains(n.Meta["methods"], "area") {
		t.Errorf("interface methods meta missing 'area': %q", n.Meta["methods"])
	}
	if !contains(n.Meta["methods"], "perimeter") {
		t.Errorf("interface methods meta missing 'perimeter': %q", n.Meta["methods"])
	}
}

// TestJSI2_SameFileInstantiates: new Foo() inside a function produces a
// static instantiates edge from the enclosing function to the class.
func TestJSI2_SameFileInstantiates(t *testing.T) {
	src := []byte(`
class Store {}
function createStore() {
  return new Store();
}
`)
	nodes, edges, _ := extractJSVariables("svc.js", "svc", "javascript", "javascript", src)

	cls := jsNodeI2(nodes, graph.NodeTypeClass, "Store")
	if cls == nil {
		t.Fatalf("missing class node Store; nodes: %+v", nodes)
	}
	e := jsEdgeI2(edges, graph.EdgeTypeInstantiates, "function:createStore", "class:Store")
	if e == nil {
		t.Fatalf("missing instantiates edge createStore → Store; edges: %+v", edges)
	}
	if e.Confidence != graph.ConfidenceStatic {
		t.Errorf("same-file instantiates must be static, got %q", e.Confidence)
	}
	if e.Meta["count"] == "" {
		t.Errorf("instantiates edge must carry count meta")
	}
}

// TestJSI2_UnresolvableConstructorSilent: new UnknownExternal() produces
// no edge and no ledger entry (cross-file; linker handles it, not a blind spot).
func TestJSI2_UnresolvableConstructorSilent(t *testing.T) {
	src := []byte(`
function makeService() {
  return new ExternalService();
}
`)
	_, edges, unresolved := extractJSVariables("svc.js", "svc", "javascript", "javascript", src)

	for _, e := range edges {
		if e.Type == graph.EdgeTypeInstantiates {
			t.Errorf("unresolvable constructor must not produce an instantiates edge; got: %+v", e)
		}
	}
	for _, u := range unresolved {
		if u.Kind == "inherits_unresolved" || u.Kind == "implements_unresolved" {
			t.Errorf("unresolvable constructor must not produce unresolved ref; got: %+v", u)
		}
	}
}

// TestJSI2_CrossFileInheritsUnresolved: class Admin extends User where User is
// not in the same file produces an inherits_unresolved ref in the extractor
// (the linker resolves it later if User was imported).
func TestJSI2_CrossFileInheritsUnresolved(t *testing.T) {
	src := []byte(`
class Admin extends User {
  ban() {}
}
`)
	_, edges, unresolved := extractJSVariables("admin.js", "svc", "javascript", "javascript", src)

	// No inherits edge (cross-file, not resolved here).
	for _, e := range edges {
		if e.Type == graph.EdgeTypeInherits {
			t.Errorf("cross-file parent must not be resolved in file pass; got: %+v", e)
		}
	}
	// Should have an inherits_unresolved ref.
	found := false
	for _, u := range unresolved {
		if u.Kind == "inherits_unresolved" && u.Name == "User" {
			found = true
		}
	}
	if !found {
		t.Errorf("cross-file parent must produce inherits_unresolved ref; got: %+v", unresolved)
	}
}
