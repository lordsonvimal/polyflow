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

func TestLinkDOMDefinitions(t *testing.T) {
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
		// class selector → out of scope, no edge, no unresolved.
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
		t.Fatalf("templ_element nodes = %d, want 2: %+v", len(newNodes), newNodes)
	}
	for _, n := range newNodes {
		if n.Type != graph.NodeTypeTemplElement {
			t.Errorf("node %s type = %q, want templ_element", n.ID, n.Type)
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
