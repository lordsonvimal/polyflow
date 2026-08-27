package linker

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// jsClassNode builds a class node as collectClass (internal/parser/js_variables.go)
// would emit it, including EndLine — the field LinkJSTypeRelations' new
// constructorByClass index relies on to attribute a "constructor"-labeled
// function node to the class whose body it sits inside.
func jsClassNode(svc, file, label string, line, endLine int) graph.Node {
	return graph.Node{
		ID:       fmt.Sprintf("%s:%s:class:%s:%d", svc, file, label, line),
		Type:     graph.NodeTypeClass,
		Label:    label,
		Service:  svc,
		File:     file,
		Line:     line,
		EndLine:  endLine,
		Language: "javascript",
	}
}

// jsFuncNode builds a function node as extractJSVariables emits it (JS/TS
// methods have no NodeTypeMethod split and no Meta["class"] back-reference —
// unlike Ruby, class ownership is inferred purely from File+Line falling
// inside the class node's [Line, EndLine] range).
func jsFuncNode(svc, file, label string, line int) graph.Node {
	return graph.Node{
		ID:       fmt.Sprintf("%s:%s:function:%s:%d", svc, file, label, line),
		Type:     graph.NodeTypeFunction,
		Label:    label,
		Service:  svc,
		File:     file,
		Line:     line,
		Language: "javascript",
	}
}

// TestLinkJSTypeRelations_InstantiateLinksToConstructor is the DC.3
// regression guard for JS/TS: `new ClassName()` used to bind only to the
// class node, never to the class's own `constructor()`, so a constructor
// doing real work read as dead code even when something in the same service
// instantiated it every time. The class-granularity `instantiates` edge must
// still be produced alongside the new method-granularity `calls` edge.
func TestLinkJSTypeRelations_InstantiateLinksToConstructor(t *testing.T) {
	t.Parallel()
	dir, paths := writeJSFixture(t, map[string]string{
		"widget.js": "export class Widget {\n" +
			"  constructor() {\n" +
			"    this.ready = true;\n" +
			"  }\n" +
			"}\n",
		"consumer.js": "import { Widget } from './widget';\n" +
			"\n" +
			"export function build() {\n" +
			"  return new Widget();\n" +
			"}\n",
	})
	_ = dir
	var widget, consumer string
	for _, p := range paths {
		switch filepath.Base(p) {
		case "widget.js":
			widget = p
		case "consumer.js":
			consumer = p
		}
	}

	nodes := []graph.Node{
		jsClassNode("svc", widget, "Widget", 1, 5),
		jsFuncNode("svc", widget, "constructor", 2),
		jsFuncNode("svc", consumer, "build", 3),
	}
	edges, _ := LinkJSTypeRelations(nodes, nil, map[string][]string{
		"svc": {widget, consumer},
	})

	fromID := fmt.Sprintf("svc:%s:function:build:3", consumer)
	wantClassEdge := fmt.Sprintf("instantiates:%s->svc:%s:class:Widget:1", fromID, widget)
	wantMethodEdge := fmt.Sprintf("calls:%s->svc:%s:function:constructor:2", fromID, widget)

	var gotClassEdge, gotMethodEdge bool
	for _, e := range edges {
		switch e.ID {
		case wantClassEdge:
			gotClassEdge = true
		case wantMethodEdge:
			gotMethodEdge = true
			if e.Type != graph.EdgeTypeCalls {
				t.Errorf("constructor edge has type %s; want calls", e.Type)
			}
		}
	}
	if !gotClassEdge {
		t.Errorf("missing class-granularity instantiates edge %s; got %+v", wantClassEdge, edges)
	}
	if !gotMethodEdge {
		t.Errorf("missing method-granularity calls edge to constructor %s; got %+v", wantMethodEdge, edges)
	}
}

// TestLinkJSTypeRelations_SameFileInstantiateLinksToConstructor is the fix
// for the gap DC.3's cross-file test didn't cover: a same-file `new X()` (or
// an asset-pipeline-style global class with no import/export at all) never
// went through walkNew's isImport-gated branch, so extractJSVariables'
// class-granularity `instantiates` edge was the only edge ever produced and
// the constructor stayed permanently zero-caller. LinkJSTypeRelations must
// fill in the missing calls edge for any `instantiates` edge already present
// in priorEdges, regardless of which pass produced it.
func TestLinkJSTypeRelations_SameFileInstantiateLinksToConstructor(t *testing.T) {
	t.Parallel()
	_, paths := writeJSFixture(t, map[string]string{
		"widget.js": "class Widget {\n" +
			"  constructor() {\n" +
			"    this.ready = true;\n" +
			"  }\n" +
			"}\n" +
			"function build() {\n" +
			"  return new Widget();\n" +
			"}\n",
	})
	widget := paths[0]

	nodes := []graph.Node{
		jsClassNode("svc", widget, "Widget", 1, 5),
		jsFuncNode("svc", widget, "constructor", 2),
		jsFuncNode("svc", widget, "build", 6),
	}
	buildID := fmt.Sprintf("svc:%s:function:build:6", widget)
	classID := fmt.Sprintf("svc:%s:class:Widget:1", widget)
	priorEdges := []graph.Edge{
		{ID: fmt.Sprintf("instantiates:%s->%s", buildID, classID), From: buildID, To: classID, Type: graph.EdgeTypeInstantiates},
	}

	edges, _ := LinkJSTypeRelations(nodes, priorEdges, map[string][]string{
		"svc": {widget},
	})

	wantMethodEdge := fmt.Sprintf("calls:%s->svc:%s:function:constructor:2", buildID, widget)
	var gotMethodEdge bool
	for _, e := range edges {
		if e.ID == wantMethodEdge {
			gotMethodEdge = true
			if e.Type != graph.EdgeTypeCalls {
				t.Errorf("constructor edge has type %s; want calls", e.Type)
			}
		}
	}
	if !gotMethodEdge {
		t.Errorf("missing method-granularity calls edge to constructor %s; got %+v", wantMethodEdge, edges)
	}
}

// TestLinkJSTypeRelations_ComponentImplLinksToConstructor is DC.13's fix: a
// react_rails `react_component("Name", props)` ERB mount resolves to
// EdgeTypeComponentImpl (rails_views.go's linkTemplates), not
// EdgeTypeInstantiates — the same instantiate→constructor fill-in loop must
// also walk this edge type, or a component mounted exclusively this way
// keeps a permanently zero-caller constructor exactly like the same-file
// `new X()` gap DC.9 fixed.
func TestLinkJSTypeRelations_ComponentImplLinksToConstructor(t *testing.T) {
	t.Parallel()
	_, paths := writeJSFixture(t, map[string]string{
		"widget.jsx": "class Widget {\n" +
			"  constructor() {\n" +
			"    this.ready = true;\n" +
			"  }\n" +
			"}\n",
	})
	widget := paths[0]

	nodes := []graph.Node{
		jsClassNode("svc", widget, "Widget", 1, 5),
		jsFuncNode("svc", widget, "constructor", 2),
	}
	mountID := "svc:app/views/foo/show.html.erb:element:react_component:1"
	classID := fmt.Sprintf("svc:%s:class:Widget:1", widget)
	priorEdges := []graph.Edge{
		{ID: fmt.Sprintf("component_impl:%s->%s", mountID, classID), From: mountID, To: classID, Type: graph.EdgeTypeComponentImpl},
	}

	edges, _ := LinkJSTypeRelations(nodes, priorEdges, map[string][]string{
		"svc": {widget},
	})

	wantMethodEdge := fmt.Sprintf("calls:%s->svc:%s:function:constructor:2", mountID, widget)
	var gotMethodEdge bool
	for _, e := range edges {
		if e.ID == wantMethodEdge {
			gotMethodEdge = true
			if e.Type != graph.EdgeTypeCalls {
				t.Errorf("constructor edge has type %s; want calls", e.Type)
			}
		}
	}
	if !gotMethodEdge {
		t.Errorf("missing method-granularity calls edge to constructor %s; got %+v", wantMethodEdge, edges)
	}
}

// TestLinkJSTypeRelations_InstantiateNoConstructorStaysClassOnly pins the
// other half: a class with no explicit `constructor` (JS/TS's implicit
// no-op default takes no edge) must not have a method-level edge fabricated.
func TestLinkJSTypeRelations_InstantiateNoConstructorStaysClassOnly(t *testing.T) {
	t.Parallel()
	_, paths := writeJSFixture(t, map[string]string{
		"gadget.js": "export class Gadget {\n" +
			"  spin() {}\n" +
			"}\n",
		"consumer.js": "import { Gadget } from './gadget';\n" +
			"\n" +
			"export function build() {\n" +
			"  return new Gadget();\n" +
			"}\n",
	})
	var gadget, consumer string
	for _, p := range paths {
		switch filepath.Base(p) {
		case "gadget.js":
			gadget = p
		case "consumer.js":
			consumer = p
		}
	}

	nodes := []graph.Node{
		jsClassNode("svc", gadget, "Gadget", 1, 3),
		jsFuncNode("svc", consumer, "build", 3),
	}
	edges, _ := LinkJSTypeRelations(nodes, nil, map[string][]string{
		"svc": {gadget, consumer},
	})

	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls {
			t.Errorf("fabricated a calls edge %s for a class with no explicit constructor", e.ID)
		}
	}
}
