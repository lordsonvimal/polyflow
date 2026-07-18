package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func TestLinkTemplScripts(t *testing.T) {
	nodes := []graph.Node{
		// A templ component loading two scripts; one resolves by path suffix,
		// one only by basename (dir remapped), one resolves to nothing.
		{
			ID: "app:room.templ:component:RoomPage:3", Type: graph.NodeTypeComponent,
			Label: "RoomPage", Service: "app", File: "room.templ", Language: "templ",
			Meta: map[string]string{"script_srcs": "js/room.js\njs/datastar.js\njs/missing.js"},
		},
		// Source JS files (module node preferred as representative).
		{
			ID: "app:assets/js/room.js:function:(module):0", Type: graph.NodeTypeFunction,
			Label: "(module)", Service: "app", File: "assets/js/room.js", Language: "javascript",
			Meta: map[string]string{"scope": "module"},
		},
		// datastar lives at assets/datastar.js — only a basename match.
		{
			ID: "app:assets/datastar.js:function:init:5", Type: graph.NodeTypeFunction,
			Label: "init", Service: "app", File: "assets/datastar.js", Language: "javascript", Line: 5,
		},
		// A hashed dist copy that must NOT win the room.js match.
		{
			ID: "app:dist/js/room.ABCD.js:function:(module):0", Type: graph.NodeTypeFunction,
			Label: "(module)", Service: "app", File: "dist/js/room.ABCD.js", Language: "javascript",
			Meta: map[string]string{"scope": "module"},
		},
	}

	edges, unresolved := LinkTemplScripts(nodes)
	byTarget := map[string]graph.Edge{}
	for _, e := range edges {
		if e.Type != graph.EdgeTypeImports {
			t.Fatalf("unexpected edge type %q", e.Type)
		}
		byTarget[e.To] = e
	}
	if len(edges) != 2 {
		t.Fatalf("imports edges = %d, want 2: %+v", len(edges), edges)
	}
	if e, ok := byTarget["app:assets/js/room.js:function:(module):0"]; !ok {
		t.Errorf("missing suffix-matched room.js import")
	} else if e.Confidence != graph.ConfidenceStatic {
		t.Errorf("room.js confidence = %q, want static", e.Confidence)
	}
	if e, ok := byTarget["app:assets/datastar.js:function:init:5"]; !ok {
		t.Errorf("missing basename-matched datastar.js import")
	} else if e.Confidence != graph.ConfidencePartial {
		t.Errorf("datastar.js confidence = %q, want partial", e.Confidence)
	}
	if len(unresolved) != 1 || unresolved[0].Name != "js/missing.js" {
		t.Errorf("unresolved = %+v, want one for js/missing.js", unresolved)
	}
}

func TestLinkDOMDefinitions_Templ(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:room.templ:component:RoomPage:3", Type: graph.NodeTypeComponent,
			Label: "RoomPage", Service: "app", File: "room.templ", Language: "templ",
			Meta: map[string]string{"dom_ids": "white-clock@5\nboard-root@4"},
		},
		// getElementById with a bare id → resolves.
		{
			ID: "app:assets/js/clock.js:dom_target:query_selector:10", Type: graph.NodeTypeDOMTarget,
			Service: "app", File: "assets/js/clock.js", Line: 10,
			Meta: map[string]string{"fn": "getElementById", "selector": `"white-clock"`},
		},
		// querySelector with #id → resolves.
		{
			ID: "app:assets/js/clock.js:dom_target:query_selector:12", Type: graph.NodeTypeDOMTarget,
			Service: "app", File: "assets/js/clock.js", Line: 12,
			Meta: map[string]string{"fn": "querySelector", "selector": `"#board-root"`},
		},
		// class selector with no HTML/JSX element → no edge, no unresolved.
		{
			ID: "app:assets/js/clock.js:dom_target:query_selector:14", Type: graph.NodeTypeDOMTarget,
			Service: "app", File: "assets/js/clock.js", Line: 14,
			Meta: map[string]string{"fn": "querySelector", "selector": `".hint"`},
		},
		// id with no templ definition → unresolved, surfaced.
		{
			ID: "app:assets/js/clock.js:dom_target:query_selector:16", Type: graph.NodeTypeDOMTarget,
			Service: "app", File: "assets/js/clock.js", Line: 16,
			Meta: map[string]string{"fn": "getElementById", "selector": `"ghost"`},
		},
	}

	newNodes, edges, unresolved := LinkDOMDefinitions(nodes)
	if len(edges) != 2 {
		t.Fatalf("defined_in edges = %d, want 2: %+v", len(edges), edges)
	}
	if len(newNodes) != 2 {
		t.Fatalf("element nodes = %d, want 2: %+v", len(newNodes), newNodes)
	}
	for _, n := range newNodes {
		// L.W2: new minting uses NodeTypeElement ("element")
		if n.Type != graph.NodeTypeElement {
			t.Errorf("node %s type = %q, want element", n.ID, n.Type)
		}
		if n.Meta["component"] != "app:room.templ:component:RoomPage:3" {
			t.Errorf("node %s missing component meta: %v", n.ID, n.Meta)
		}
	}
	for _, e := range edges {
		if e.Type != graph.EdgeTypeDefinedIn || e.Confidence != graph.ConfidenceStatic {
			t.Errorf("bad edge %+v", e)
		}
	}
	if len(unresolved) != 1 || unresolved[0].Name != "#ghost" || unresolved[0].Kind != "dom_ref" {
		t.Errorf("unresolved = %+v, want one #ghost dom_ref", unresolved)
	}
}

// TestLinkDOMDefinitions_HTMLSource verifies that HTML element nodes (NodeTypeElement
// with meta["id"]) are found by the generalized index and linked via defined_in edges
// without minting new nodes.
func TestLinkDOMDefinitions_HTMLSource(t *testing.T) {
	nodes := []graph.Node{
		// HTML element node produced by html_element_id pattern.
		{
			ID:       "app:views/index.html:element:#save-btn:8",
			Type:     graph.NodeTypeElement,
			Label:    "#save-btn",
			Service:  "app",
			File:     "views/index.html",
			Line:     8,
			Language: "html",
			Meta:     map[string]string{"id": "save-btn"},
		},
		// jQuery selector targeting the same element.
		{
			ID:      "app:assets/js/app.js:dom_target:jquery_selector:3",
			Type:    graph.NodeTypeDOMTarget,
			Service: "app",
			File:    "assets/js/app.js",
			Line:    3,
			Meta:    map[string]string{"fn": "$", "selector": `"#save-btn"`},
		},
	}

	newNodes, edges, unresolved := LinkDOMDefinitions(nodes)
	if len(newNodes) != 0 {
		t.Fatalf("expected no new nodes (element already exists), got %d: %+v", len(newNodes), newNodes)
	}
	if len(edges) != 1 {
		t.Fatalf("defined_in edges = %d, want 1: %+v", len(edges), edges)
	}
	if edges[0].To != "app:views/index.html:element:#save-btn:8" {
		t.Errorf("edge.To = %q, want the pre-existing HTML element node", edges[0].To)
	}
	if edges[0].Confidence != graph.ConfidenceStatic {
		t.Errorf("edge.Confidence = %q, want static", edges[0].Confidence)
	}
	if len(unresolved) != 0 {
		t.Errorf("unexpected unresolved: %+v", unresolved)
	}
}

// TestLinkDOMDefinitions_ClassFanOut verifies that a .class selector emits
// defined_in edges to ALL elements with that class (N edges for N matches,
// rule 1 fan-out) with inferred confidence.
func TestLinkDOMDefinitions_ClassFanOut(t *testing.T) {
	nodes := []graph.Node{
		// Two HTML elements with the same class.
		{
			ID: "app:views/list.html:element:.item:10", Type: graph.NodeTypeElement,
			Label: ".item", Service: "app", File: "views/list.html", Line: 10, Language: "html",
			Meta: map[string]string{"class": "item"},
		},
		{
			ID: "app:views/list.html:element:.item:20", Type: graph.NodeTypeElement,
			Label: ".item", Service: "app", File: "views/list.html", Line: 20, Language: "html",
			Meta: map[string]string{"class": "item active"},
		},
		// jQuery delegation targeting .item.
		{
			ID:      "app:assets/js/list.js:dom_target:dom_event_jquery_delegate:5",
			Type:    graph.NodeTypeDOMTarget,
			Service: "app",
			File:    "assets/js/list.js",
			Line:    5,
			Meta:    map[string]string{"fn": "on", "selector": `".item"`},
		},
	}

	newNodes, edges, unresolved := LinkDOMDefinitions(nodes)
	if len(newNodes) != 0 {
		t.Fatalf("expected no new nodes, got %d", len(newNodes))
	}
	if len(edges) != 2 {
		t.Fatalf("defined_in edges = %d, want 2 (one per element with class item): %+v", len(edges), edges)
	}
	for _, e := range edges {
		if e.Confidence != graph.ConfidenceInferred {
			t.Errorf("class-selector edge confidence = %q, want inferred", e.Confidence)
		}
	}
	if len(unresolved) != 0 {
		t.Errorf("unexpected unresolved (class misses should not ledger): %+v", unresolved)
	}
}

// TestLinkDOMDefinitions_ComplexSelector verifies that complex CSS selectors
// (descendant, pseudo, attribute, etc.) produce a selector_dynamic ledger entry
// and no edges.
func TestLinkDOMDefinitions_ComplexSelector(t *testing.T) {
	nodes := []graph.Node{
		{
			ID:      "app:assets/js/app.js:dom_target:query_selector:7",
			Type:    graph.NodeTypeDOMTarget,
			Service: "app",
			File:    "assets/js/app.js",
			Line:    7,
			Meta:    map[string]string{"fn": "querySelector", "selector": `"ul li.active > span"`},
		},
	}

	newNodes, edges, unresolved := LinkDOMDefinitions(nodes)
	if len(newNodes) != 0 || len(edges) != 0 {
		t.Fatalf("expected no nodes/edges for complex selector, got %d nodes, %d edges", len(newNodes), len(edges))
	}
	if len(unresolved) != 1 || unresolved[0].Kind != "selector_dynamic" {
		t.Errorf("expected selector_dynamic ledger entry, got: %+v", unresolved)
	}
}

// TestLinkDOMDefinitions_Determinism verifies that two runs on the same input
// produce byte-identical edge ordering (bug-class rule 2).
func TestLinkDOMDefinitions_Determinism(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:views/page.html:element:#btn1:5", Type: graph.NodeTypeElement,
			Service: "app", File: "views/page.html", Line: 5, Language: "html",
			Meta: map[string]string{"id": "btn1"},
		},
		{
			ID: "app:views/page.html:element:#btn2:10", Type: graph.NodeTypeElement,
			Service: "app", File: "views/page.html", Line: 10, Language: "html",
			Meta: map[string]string{"id": "btn2"},
		},
		{
			ID:      "app:js/a.js:dom_target:querySelector:3",
			Type:    graph.NodeTypeDOMTarget,
			Service: "app",
			File:    "js/a.js",
			Line:    3,
			Meta:    map[string]string{"fn": "querySelector", "selector": `"#btn1"`},
		},
		{
			ID:      "app:js/a.js:dom_target:querySelector:4",
			Type:    graph.NodeTypeDOMTarget,
			Service: "app",
			File:    "js/a.js",
			Line:    4,
			Meta:    map[string]string{"fn": "querySelector", "selector": `"#btn2"`},
		},
	}

	_, edges1, _ := LinkDOMDefinitions(nodes)
	_, edges2, _ := LinkDOMDefinitions(nodes)

	ids1 := make([]string, len(edges1))
	ids2 := make([]string, len(edges2))
	for i, e := range edges1 {
		ids1[i] = e.ID
	}
	for i, e := range edges2 {
		ids2[i] = e.ID
	}
	for i := range ids1 {
		if i >= len(ids2) || ids1[i] != ids2[i] {
			t.Errorf("non-deterministic output: run1=%v run2=%v", ids1, ids2)
			break
		}
	}
}
