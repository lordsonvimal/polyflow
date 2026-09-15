package pipeline_test

// FX.8.27 (2026-09-15): templ_layer — Tier FX migration of
// internal/linker/templ_layer.go's LinkTemplScripts + LinkDOMDefinitions
// (Tier K.4/DS.3), replaced by patterns/generic/templ_layer.yaml +
// rules/generic/templ_layer.dl, driven by the "templ_layer" hub provider
// (internal/factpipe/hub_templ_layer.go). Hub-only, no source files parsed
// at all — both passes are pure graph-derived logic — so these fixtures
// hand-build graph.Node the same way the retired pass's own tests did.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

func tlActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("templ_layer")
	if fw == nil {
		t.Fatal("templ_layer framework not embedded")
	}
	return []*pipeline.Framework{fw}
}

func tlRun(t *testing.T, nodes []graph.Node) pipeline.Result {
	t.Helper()
	res, err := pipeline.Run(tlActive(t), nil, graph.Snapshot{Nodes: nodes})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// tlNewNodes filters res.Nodes down to IDs not already present in the input
// — mint is "gap-fill only" at the CALLER (internal/indexer/link_passes.go),
// not inside pipeline.Run itself, so a mint spec's row still fires (and
// still appears in res.Nodes) even when its ID already names a real node;
// mirrors the real caller's own dedup before asserting "no new nodes".
func tlNewNodes(input []graph.Node, res pipeline.Result) []graph.Node {
	existing := make(map[string]bool, len(input))
	for _, n := range input {
		existing[n.ID] = true
	}
	var out []graph.Node
	for _, n := range res.Nodes {
		if !existing[n.ID] {
			out = append(out, n)
		}
	}
	return out
}

func TestTemplLayerRule_Scripts(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:room.templ:component:RoomPage:3", Type: graph.NodeTypeComponent,
			Label: "RoomPage", Service: "app", File: "room.templ", Language: "templ",
			Meta: map[string]string{"script_srcs": "js/room.js\njs/datastar.js\njs/missing.js"},
		},
		{
			ID: "app:assets/js/room.js:function:(module):0", Type: graph.NodeTypeFunction,
			Label: "(module)", Service: "app", File: "assets/js/room.js", Language: "javascript",
			Meta: map[string]string{"scope": "module"},
		},
		{
			ID: "app:assets/datastar.js:function:init:5", Type: graph.NodeTypeFunction,
			Label: "init", Service: "app", File: "assets/datastar.js", Language: "javascript", Line: 5,
		},
		{
			ID: "app:dist/js/room.ABCD.js:function:(module):0", Type: graph.NodeTypeFunction,
			Label: "(module)", Service: "app", File: "dist/js/room.ABCD.js", Language: "javascript",
			Meta: map[string]string{"scope": "module"},
		},
	}
	res := tlRun(t, nodes)
	byTarget := map[string]graph.Edge{}
	for _, e := range res.Edges {
		if e.Type != graph.EdgeTypeImports {
			continue
		}
		byTarget[e.To] = e
	}
	if len(byTarget) != 2 {
		t.Fatalf("imports edges = %d, want 2: %+v", len(byTarget), res.Edges)
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
	if len(res.Unresolved) != 1 || res.Unresolved[0].Name != "js/missing.js" {
		t.Errorf("unresolved = %+v, want one for js/missing.js", res.Unresolved)
	}
}

func TestTemplLayerRule_DOMDefinitionsTempl(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:room.templ:component:RoomPage:3", Type: graph.NodeTypeComponent,
			Label: "RoomPage", Service: "app", File: "room.templ", Language: "templ",
			Meta: map[string]string{"dom_ids": "white-clock@5\nboard-root@4"},
		},
		{
			ID: "app:assets/js/clock.js:dom_target:query_selector:10", Type: graph.NodeTypeDOMTarget,
			Service: "app", File: "assets/js/clock.js", Line: 10,
			Meta: map[string]string{"fn": "getElementById", "selector": `"white-clock"`},
		},
		{
			ID: "app:assets/js/clock.js:dom_target:query_selector:12", Type: graph.NodeTypeDOMTarget,
			Service: "app", File: "assets/js/clock.js", Line: 12,
			Meta: map[string]string{"fn": "querySelector", "selector": `"#board-root"`},
		},
		{
			ID: "app:assets/js/clock.js:dom_target:query_selector:14", Type: graph.NodeTypeDOMTarget,
			Service: "app", File: "assets/js/clock.js", Line: 14,
			Meta: map[string]string{"fn": "querySelector", "selector": `".hint"`},
		},
		{
			ID: "app:assets/js/clock.js:dom_target:query_selector:16", Type: graph.NodeTypeDOMTarget,
			Service: "app", File: "assets/js/clock.js", Line: 16,
			Meta: map[string]string{"fn": "getElementById", "selector": `"ghost"`},
		},
	}
	res := tlRun(t, nodes)
	definedIn := filterEdges(res.Edges, graph.EdgeTypeDefinedIn)
	if len(definedIn) != 2 {
		t.Fatalf("defined_in edges = %d, want 2: %+v", len(definedIn), definedIn)
	}
	if len(res.Nodes) != 2 {
		t.Fatalf("element nodes = %d, want 2: %+v", len(res.Nodes), res.Nodes)
	}
	for _, n := range res.Nodes {
		if n.Type != graph.NodeTypeElement {
			t.Errorf("node %s type = %q, want element", n.ID, n.Type)
		}
		if n.Meta["component"] != "app:room.templ:component:RoomPage:3" {
			t.Errorf("node %s missing component meta: %v", n.ID, n.Meta)
		}
	}
	for _, e := range definedIn {
		if e.Confidence != graph.ConfidenceStatic {
			t.Errorf("bad edge %+v", e)
		}
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Name != "#ghost" || res.Unresolved[0].Kind != "dom_ref" {
		t.Errorf("unresolved = %+v, want one #ghost dom_ref", res.Unresolved)
	}
}

func TestTemplLayerRule_DOMDefinitionsHTMLSource(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:views/index.html:element:#save-btn:8", Type: graph.NodeTypeElement,
			Label: "#save-btn", Service: "app", File: "views/index.html", Line: 8, Language: "html",
			Meta: map[string]string{"id": "save-btn"},
		},
		{
			ID: "app:assets/js/app.js:dom_target:jquery_selector:3", Type: graph.NodeTypeDOMTarget,
			Service: "app", File: "assets/js/app.js", Line: 3,
			Meta: map[string]string{"fn": "$", "selector": `"#save-btn"`},
		},
	}
	res := tlRun(t, nodes)
	if got := tlNewNodes(nodes, res); len(got) != 0 {
		t.Fatalf("expected no new nodes (element already exists), got %d: %+v", len(got), got)
	}
	definedIn := filterEdges(res.Edges, graph.EdgeTypeDefinedIn)
	if len(definedIn) != 1 {
		t.Fatalf("defined_in edges = %d, want 1: %+v", len(definedIn), definedIn)
	}
	if definedIn[0].To != "app:views/index.html:element:#save-btn:8" {
		t.Errorf("edge.To = %q, want the pre-existing HTML element node", definedIn[0].To)
	}
	if definedIn[0].Confidence != graph.ConfidenceStatic {
		t.Errorf("edge.Confidence = %q, want static", definedIn[0].Confidence)
	}
	if len(res.Unresolved) != 0 {
		t.Errorf("unexpected unresolved: %+v", res.Unresolved)
	}
}

func TestTemplLayerRule_ClassFanOut(t *testing.T) {
	nodes := []graph.Node{
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
		{
			ID: "app:assets/js/list.js:dom_target:dom_event_jquery_delegate:5", Type: graph.NodeTypeDOMTarget,
			Service: "app", File: "assets/js/list.js", Line: 5,
			Meta: map[string]string{"fn": "on", "selector": `".item"`},
		},
	}
	res := tlRun(t, nodes)
	if got := tlNewNodes(nodes, res); len(got) != 0 {
		t.Fatalf("expected no new nodes, got %d", len(got))
	}
	definedIn := filterEdges(res.Edges, graph.EdgeTypeDefinedIn)
	if len(definedIn) != 2 {
		t.Fatalf("defined_in edges = %d, want 2 (one per element with class item): %+v", len(definedIn), definedIn)
	}
	for _, e := range definedIn {
		if e.Confidence != graph.ConfidenceInferred {
			t.Errorf("class-selector edge confidence = %q, want inferred", e.Confidence)
		}
	}
	if len(res.Unresolved) != 0 {
		t.Errorf("unexpected unresolved (class misses should not ledger): %+v", res.Unresolved)
	}
}

func TestTemplLayerRule_TemplClassSelector(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:list.templ:component:ListPage:3", Type: graph.NodeTypeComponent,
			Label: "ListPage", Service: "app", File: "list.templ", Language: "templ",
			Meta: map[string]string{"dom_classes": "item@6\nactive@6"},
		},
		{
			ID: "app:assets/js/list.js:dom_target:query_selector:9", Type: graph.NodeTypeDOMTarget,
			Service: "app", File: "assets/js/list.js", Line: 9,
			Meta: map[string]string{"fn": "querySelector", "selector": `".item"`},
		},
	}
	res := tlRun(t, nodes)
	if len(res.Nodes) != 1 {
		t.Fatalf("element nodes = %d, want 1 (minted from templ class=): %+v", len(res.Nodes), res.Nodes)
	}
	if res.Nodes[0].Label != ".item" || res.Nodes[0].Meta["dom_class"] != "item" {
		t.Errorf("minted node = %+v, want label .item and dom_class=item", res.Nodes[0])
	}
	if res.Nodes[0].Meta["component"] != "app:list.templ:component:ListPage:3" {
		t.Errorf("minted node missing component meta: %+v", res.Nodes[0])
	}
	definedIn := filterEdges(res.Edges, graph.EdgeTypeDefinedIn)
	if len(definedIn) != 1 || definedIn[0].Confidence != graph.ConfidenceInferred {
		t.Errorf("edges = %+v, want one inferred-confidence defined_in edge", definedIn)
	}
	if len(res.Unresolved) != 0 {
		t.Errorf("unexpected unresolved: %+v", res.Unresolved)
	}
}

func TestTemplLayerRule_StylesheetClassSelector(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:assets/css/app.scss:element:.btn:8", Type: graph.NodeTypeElement,
			Label: ".btn", Service: "app", File: "assets/css/app.scss", Line: 8, Language: "scss",
			Meta: map[string]string{"pattern": "stylesheet_selector", "selector": ".btn", "selector_kind": "class"},
		},
		{
			ID: "app:assets/js/app.js:dom_target:query_selector:4", Type: graph.NodeTypeDOMTarget,
			Service: "app", File: "assets/js/app.js", Line: 4,
			Meta: map[string]string{"fn": "querySelector", "selector": `".btn"`},
		},
	}
	res := tlRun(t, nodes)
	if got := tlNewNodes(nodes, res); len(got) != 0 {
		t.Fatalf("expected no new nodes (stylesheet node already exists), got %+v", got)
	}
	definedIn := filterEdges(res.Edges, graph.EdgeTypeDefinedIn)
	if len(definedIn) != 1 {
		t.Fatalf("edges = %+v, want one defined_in edge", definedIn)
	}
	if definedIn[0].To != nodes[0].ID {
		t.Errorf("edge To = %q, want the stylesheet selector node %q", definedIn[0].To, nodes[0].ID)
	}
	if len(res.Unresolved) != 0 {
		t.Errorf("unexpected unresolved: %+v", res.Unresolved)
	}
}

const tlMaxClassFanoutTest = 20
const tlMaxFanoutTargetsListedTest = 15

func TestTemplLayerRule_ClassHighFanout(t *testing.T) {
	var nodes []graph.Node
	for i := 0; i < tlMaxClassFanoutTest+1; i++ {
		nodes = append(nodes, graph.Node{
			ID:      fmt.Sprintf("app:views/list.html:element:.item:%d", i),
			Type:    graph.NodeTypeElement,
			Label:   ".item",
			Service: "app", File: "views/list.html", Line: i, Language: "html",
			Meta: map[string]string{"class": "item"},
		})
	}
	nodes = append(nodes, graph.Node{
		ID:      "app:assets/js/list.js:dom_target:query_selector:1",
		Type:    graph.NodeTypeDOMTarget,
		Service: "app", File: "assets/js/list.js", Line: 1,
		Meta: map[string]string{"fn": "querySelector", "selector": `".item"`},
	})
	res := tlRun(t, nodes)
	if len(res.Nodes) != 0 {
		t.Fatalf("expected no new nodes, got %+v", res.Nodes)
	}
	definedIn := filterEdges(res.Edges, graph.EdgeTypeDefinedIn)
	if len(definedIn) != 0 {
		t.Fatalf("edges = %+v, want none (fan-out cap must suppress, not spray)", definedIn)
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Kind != "dom_class_high_fanout" {
		t.Fatalf("unresolved = %+v, want one dom_class_high_fanout entry", res.Unresolved)
	}
	if res.Unresolved[0].Name != ".item" {
		t.Errorf("unresolved name = %q, want .item", res.Unresolved[0].Name)
	}
	wantTargets := tlMaxFanoutTargetsListedTest + 1
	gotTargets := strings.Split(res.Unresolved[0].Targets, "\n")
	if len(gotTargets) != wantTargets {
		t.Fatalf("targets = %q (%d lines), want %d", res.Unresolved[0].Targets, len(gotTargets), wantTargets)
	}
	if !strings.HasPrefix(gotTargets[0], "views/list.html:") {
		t.Errorf("first target = %q, want a views/list.html:<line> entry", gotTargets[0])
	}
	if last := gotTargets[len(gotTargets)-1]; last != "+6 more" {
		t.Errorf("last target = %q, want the truncation marker", last)
	}
}

func TestTemplLayerRule_ComplexSelector(t *testing.T) {
	nodes := []graph.Node{
		{
			ID:      "app:assets/js/app.js:dom_target:query_selector:7",
			Type:    graph.NodeTypeDOMTarget,
			Service: "app", File: "assets/js/app.js", Line: 7,
			Meta: map[string]string{"fn": "querySelector", "selector": `"ul li.active > span"`},
		},
	}
	res := tlRun(t, nodes)
	if len(res.Nodes) != 0 || len(filterEdges(res.Edges, graph.EdgeTypeDefinedIn)) != 0 {
		t.Fatalf("expected no nodes/edges for complex selector, got %d nodes, %d edges", len(res.Nodes), len(res.Edges))
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Kind != "selector_dynamic" {
		t.Errorf("expected selector_dynamic ledger entry, got: %+v", res.Unresolved)
	}
}

func TestTemplLayerRule_Determinism(t *testing.T) {
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
			Service: "app", File: "js/a.js", Line: 3,
			Meta: map[string]string{"fn": "querySelector", "selector": `"#btn1"`},
		},
		{
			ID:      "app:js/a.js:dom_target:querySelector:4",
			Type:    graph.NodeTypeDOMTarget,
			Service: "app", File: "js/a.js", Line: 4,
			Meta: map[string]string{"fn": "querySelector", "selector": `"#btn2"`},
		},
	}
	res1 := tlRun(t, nodes)
	res2 := tlRun(t, nodes)
	ids1 := edgeIDs(filterEdges(res1.Edges, graph.EdgeTypeDefinedIn))
	ids2 := edgeIDs(filterEdges(res2.Edges, graph.EdgeTypeDefinedIn))
	for i := range ids1 {
		if i >= len(ids2) || ids1[i] != ids2[i] {
			t.Errorf("non-deterministic output: run1=%v run2=%v", ids1, ids2)
			break
		}
	}
}

func TestTemplLayerRule_ScriptsServiceScopedAndDeterministic(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "svc-a:page.templ:component:Head:12", Type: graph.NodeTypeComponent,
			Label: "Head", Service: "svc-a", File: "/repo/svc-a/page.templ", Language: "templ",
			Meta: map[string]string{"script_srcs": "/static/js/datastar.min.js"},
		},
		{
			ID: "svc-a:datastar:function:(module):0", Type: graph.NodeTypeFunction,
			Label: "(module)", Service: "svc-a", File: "/repo/svc-a/static/js/datastar.min.js",
			Language: "javascript", Meta: map[string]string{"scope": "module"},
		},
		{
			ID: "svc-b:datastar:function:(module):0", Type: graph.NodeTypeFunction,
			Label: "(module)", Service: "svc-b", File: "/repo/svc-b/static/js/datastar.min.js",
			Language: "javascript", Meta: map[string]string{"scope": "module"},
		},
	}
	for i := 0; i < 50; i++ {
		res := tlRun(t, nodes)
		imports := filterEdges(res.Edges, graph.EdgeTypeImports)
		if len(imports) != 1 {
			t.Fatalf("iteration %d: imports edges = %d, want 1: %+v", i, len(imports), imports)
		}
		if imports[0].To != "svc-a:datastar:function:(module):0" {
			t.Fatalf("iteration %d: resolved to %q, want svc-a's own copy", i, imports[0].To)
		}
	}
}

func TestTemplLayerRule_ScriptsLedgersCrossServiceOnlyMatch(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "svc-a:page.templ:component:Head:12", Type: graph.NodeTypeComponent,
			Label: "Head", Service: "svc-a", File: "/repo/svc-a/page.templ", Language: "templ",
			Meta: map[string]string{"script_srcs": "/static/js/vendor.js"},
		},
		{
			ID: "svc-b:vendor:function:(module):0", Type: graph.NodeTypeFunction,
			Label: "(module)", Service: "svc-b", File: "/repo/svc-b/static/js/vendor.js",
			Language: "javascript", Meta: map[string]string{"scope": "module"},
		},
	}
	res := tlRun(t, nodes)
	imports := filterEdges(res.Edges, graph.EdgeTypeImports)
	if len(imports) != 0 {
		t.Fatalf("imports edges = %d, want 0 (cross-service match must not link): %+v", len(imports), imports)
	}
	if len(res.Unresolved) != 1 || res.Unresolved[0].Name != "/static/js/vendor.js" {
		t.Fatalf("unresolved = %+v, want one ledger entry for /static/js/vendor.js", res.Unresolved)
	}
}

func filterEdges(edges []graph.Edge, typ graph.EdgeType) []graph.Edge {
	var out []graph.Edge
	for _, e := range edges {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func edgeIDs(edges []graph.Edge) []string {
	out := make([]string, len(edges))
	for i, e := range edges {
		out[i] = e.ID
	}
	return out
}
