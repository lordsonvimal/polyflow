package parser

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func rbNode(nodes []graph.Node, typ graph.NodeType, label string) *graph.Node {
	for i := range nodes {
		if nodes[i].Type == typ && nodes[i].Label == label {
			return &nodes[i]
		}
	}
	return nil
}

func rbEdge(edges []graph.Edge, typ graph.EdgeType, fromSub, toSub string) *graph.Edge {
	for i := range edges {
		e := &edges[i]
		if e.Type == typ && contains(e.From, fromSub) && contains(e.To, toSub) {
			return e
		}
	}
	return nil
}

// TestRubyI2_SameFileSuperclass: class Admin < User in the same file produces
// an inferred inherits edge with via=superclass.
func TestRubyI2_SameFileSuperclass(t *testing.T) {
	src := []byte(`
class User
  def greet; end
end

class Admin < User
  def ban; end
end
`)
	nodes, edges, _ := extractRubyVariables("models.rb", "svc", src)

	if rbNode(nodes, graph.NodeTypeClass, "User") == nil {
		t.Fatalf("missing class node User; nodes: %+v", nodes)
	}
	if rbNode(nodes, graph.NodeTypeClass, "Admin") == nil {
		t.Fatalf("missing class node Admin; nodes: %+v", nodes)
	}
	e := rbEdge(edges, graph.EdgeTypeInherits, "class:Admin", "class:User")
	if e == nil {
		t.Fatalf("missing inherits edge Admin → User; edges: %+v", edges)
	}
	if e.Meta["via"] != "superclass" {
		t.Errorf("inherits edge via should be 'superclass', got %q", e.Meta["via"])
	}
}

// TestRubyI2_Mixins: include/extend/prepend in a class body produce inherits
// edges with via=mixin and mixin=include|extend|prepend.
func TestRubyI2_Mixins(t *testing.T) {
	src := []byte(`
module Serializable; end
module ClassMethods; end
module Logging; end

class Document
  include Serializable
  extend ClassMethods
  prepend Logging

  def save; end
end
`)
	nodes, edges, _ := extractRubyVariables("doc.rb", "svc", src)

	if rbNode(nodes, graph.NodeTypeClass, "Document") == nil {
		t.Fatalf("missing class node Document; nodes: %+v", nodes)
	}

	cases := []struct {
		mod   string
		mixin string
	}{
		{"Serializable", "include"},
		{"ClassMethods", "extend"},
		{"Logging", "prepend"},
	}
	for _, tc := range cases {
		e := rbEdge(edges, graph.EdgeTypeInherits, "class:Document", "class:"+tc.mod)
		if e == nil {
			t.Errorf("missing inherits edge Document → %s (mixin=%s); edges: %+v", tc.mod, tc.mixin, edges)
			continue
		}
		if e.Meta["via"] != "mixin" {
			t.Errorf("%s mixin edge via should be 'mixin', got %q", tc.mod, e.Meta["via"])
		}
		if e.Meta["mixin"] != tc.mixin {
			t.Errorf("%s mixin edge mixin should be '%s', got %q", tc.mod, tc.mixin, e.Meta["mixin"])
		}
	}
}

// TestRubyI2_Instantiates: Foo.new inside a method produces an instantiates edge.
func TestRubyI2_Instantiates(t *testing.T) {
	src := []byte(`
class Widget
  def render; end
end

class Factory
  def build
    Widget.new
  end
end
`)
	nodes, edges, _ := extractRubyVariables("factory.rb", "svc", src)

	if rbNode(nodes, graph.NodeTypeClass, "Widget") == nil {
		t.Fatalf("missing class node Widget; nodes: %+v", nodes)
	}
	e := rbEdge(edges, graph.EdgeTypeInstantiates, "function:build", "class:Widget")
	if e == nil {
		t.Fatalf("missing instantiates edge build → Widget; edges: %+v", edges)
	}
	if e.Meta["count"] == "" {
		t.Errorf("instantiates edge must carry count meta")
	}
}

// TestRubyI2_AmbiguousConstant: if a constant name appears in an include but
// is not in the same file, it produces an inherits_unresolved ledger entry.
func TestRubyI2_UnknownMixinLedger(t *testing.T) {
	src := []byte(`
class MyClass
  include SomeExternalModule
end
`)
	_, edges, unresolved := extractRubyVariables("my.rb", "svc", src)

	for _, e := range edges {
		if e.Type == graph.EdgeTypeInherits {
			t.Errorf("unknown mixin must not produce inherits edge; got: %+v", e)
		}
	}
	found := false
	for _, u := range unresolved {
		if u.Kind == "inherits_unresolved" && u.Name == "SomeExternalModule" {
			found = true
		}
	}
	if !found {
		t.Errorf("unknown mixin must produce inherits_unresolved ref; got: %+v", unresolved)
	}
}

// TestRubyI2_NegativeClassLevelMixinOnlyInClassBody: include inside a method
// body does NOT produce an inherits edge (it's a runtime call, not a class-level mixin).
func TestRubyI2_NegativeMixinInsideMethod(t *testing.T) {
	src := []byte(`
module Mod; end

class Foo
  def configure
    include Mod  # this is inside a method, not a class-level include
  end
end
`)
	_, edges, _ := extractRubyVariables("foo.rb", "svc", src)

	for _, e := range edges {
		if e.Type == graph.EdgeTypeInherits {
			t.Errorf("include inside method must not produce class-level inherits edge; got: %+v", e)
		}
	}
}
