package linker

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// TestLinkDOMContracts_PrefixMatch mirrors the real chessleap promotion-color
// repro (IA.5 worked example): a templ component's data-testid value is
// built from a Go string-literal prefix + a dynamic expression
// (`"promotion-button-" + strings.ToLower(...)`), and the JS consumer reads
// it via a compound querySelector containing a template-literal attribute
// selector with a matching ${…} prefix embedded in an unrelated compound
// selector — the real board.js shape.
func TestLinkDOMContracts_PrefixMatch(t *testing.T) {
	nodes := []graph.Node{
		{
			ID:   "chessleap:promotionbutton.templ:component:PromotionButtonForColor:18",
			Type: graph.NodeTypeComponent, Label: "PromotionButtonForColor",
			Service: "chessleap", File: "ui/components/promotionbutton.templ", Line: 18, Language: "templ",
			Meta: map[string]string{"dom_data_attrs": "data-testid=promotion-button-*@25"},
		},
		{
			ID:   "chessleap:assets/js/board.js:dom_target:query_selector:322",
			Type: graph.NodeTypeDOMTarget, Service: "chessleap", File: "assets/js/board.js", Line: 322,
			Meta: map[string]string{
				"fn": "querySelector",
				"selector": "`[data-color=\"${movingPiece.color}\"] " +
					"[data-testid=\"promotion-button-${move.promotion}\"] span[role=\"img\"]`",
			},
		},
	}

	newNodes, edges, unresolved := LinkDOMContracts(nodes)
	if len(edges) != 1 {
		t.Fatalf("dom_contract edges = %d, want 1: %+v", len(edges), edges)
	}
	e := edges[0]
	if e.Type != graph.EdgeTypeDOMContract {
		t.Errorf("edge type = %q, want dom_contract", e.Type)
	}
	if e.Confidence != graph.ConfidencePartial {
		t.Errorf("edge confidence = %q, want partial (prefix match)", e.Confidence)
	}
	if e.To != "chessleap:assets/js/board.js:dom_target:query_selector:322" {
		t.Errorf("edge To = %q, want the board.js dom_target", e.To)
	}
	if e.From != nodes[0].ID {
		t.Errorf("edge From = %q, want the component itself %q (no intermediate node)", e.From, nodes[0].ID)
	}
	if len(newNodes) != 0 {
		t.Errorf("newNodes = %+v, want none — the edge attributes directly to the component", newNodes)
	}
	// The data-color token has no producer and no static prefix at all
	// (fully ${…}) — it must not block the data-testid token from resolving.
	if len(unresolved) != 0 {
		t.Errorf("unresolved = %+v, want none (data-testid token resolved the selector)", unresolved)
	}
}

// TestLinkDOMContracts_ExactStaticMatch covers a fully static attribute on
// both sides: data-testid="promotion-overlay" (ConstantAttribute, no
// interpolation) matched by a literal (no ${…}) JS selector.
func TestLinkDOMContracts_ExactStaticMatch(t *testing.T) {
	nodes := []graph.Node{
		{
			ID:   "app:overlay.templ:component:PromotionOverlay:10",
			Type: graph.NodeTypeComponent, Label: "PromotionOverlay",
			Service: "app", File: "overlay.templ", Line: 10, Language: "templ",
			Meta: map[string]string{"dom_data_attrs": "data-testid=promotion-overlay@28"},
		},
		{
			ID:   "app:assets/js/overlay.js:dom_target:query_selector:5",
			Type: graph.NodeTypeDOMTarget, Service: "app", File: "assets/js/overlay.js", Line: 5,
			Meta: map[string]string{"fn": "querySelector", "selector": `'[data-testid="promotion-overlay"]'`},
		},
	}

	_, edges, unresolved := LinkDOMContracts(nodes)
	if len(edges) != 1 || edges[0].Confidence != graph.ConfidenceStatic {
		t.Fatalf("edges = %+v, want one static-confidence edge", edges)
	}
	if edges[0].From != nodes[0].ID {
		t.Errorf("edge From = %q, want the component %q", edges[0].From, nodes[0].ID)
	}
	if len(unresolved) != 0 {
		t.Errorf("unresolved = %+v, want none", unresolved)
	}
}

// TestLinkDOMContracts_NoProducer_SurfacesUnresolved: a selector token with a
// real static prefix that matches no known producer is surfaced, not dropped
// (the bug this feature fixes: dynamic-interpolated selectors used to vanish
// silently because they failed both the resolve path and the "complex
// selector" unresolved-ledger condition in LinkDOMDefinitions).
func TestLinkDOMContracts_NoProducer_SurfacesUnresolved(t *testing.T) {
	nodes := []graph.Node{
		{
			ID:   "app:assets/js/x.js:dom_target:query_selector:1",
			Type: graph.NodeTypeDOMTarget, Service: "app", File: "assets/js/x.js", Line: 1,
			Meta: map[string]string{"fn": "querySelector", "selector": "`[data-testid=\"ghost-${id}\"]`"},
		},
	}
	_, edges, unresolved := LinkDOMContracts(nodes)
	if len(edges) != 0 {
		t.Fatalf("edges = %+v, want none", edges)
	}
	if len(unresolved) != 1 || unresolved[0].Kind != "dom_contract_ref" {
		t.Fatalf("unresolved = %+v, want one dom_contract_ref", unresolved)
	}
}

// TestLinkDOMContracts_FullyDynamicToken_SkippedNotMatched: a token with no
// static prefix at all (`[data-color="${c}"]`) must not fan out to every
// producer for that attribute — it carries no signal.
func TestLinkDOMContracts_FullyDynamicToken_SkippedNotMatched(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:x.templ:component:X:1", Type: graph.NodeTypeComponent,
			Service: "app", File: "x.templ", Line: 1, Language: "templ",
			Meta: map[string]string{"dom_data_attrs": "data-color=red@2\ndata-color=blue@3"},
		},
		{
			ID:   "app:assets/js/x.js:dom_target:query_selector:1",
			Type: graph.NodeTypeDOMTarget, Service: "app", File: "assets/js/x.js", Line: 1,
			Meta: map[string]string{"fn": "querySelector", "selector": "`[data-color=\"${c}\"]`"},
		},
	}
	_, edges, unresolved := LinkDOMContracts(nodes)
	if len(edges) != 0 {
		t.Fatalf("edges = %+v, want none (fully dynamic token has no static signal)", edges)
	}
	if len(unresolved) != 1 {
		t.Fatalf("unresolved = %+v, want one (the selector still went nowhere)", unresolved)
	}
}

// TestLinkDOMContracts_IDAttribute reuses the existing dom_ids capture
// (defined_in's producer index) as a dom_contract producer too, so
// `[id="…"]`-shaped attribute selectors resolve without a separate capture.
func TestLinkDOMContracts_IDAttribute(t *testing.T) {
	nodes := []graph.Node{
		{
			ID: "app:room.templ:component:RoomPage:3", Type: graph.NodeTypeComponent,
			Service: "app", File: "room.templ", Line: 3, Language: "templ",
			Meta: map[string]string{"dom_ids": "board-root@4"},
		},
		{
			ID:   "app:assets/js/clock.js:dom_target:query_selector:12",
			Type: graph.NodeTypeDOMTarget, Service: "app", File: "assets/js/clock.js", Line: 12,
			Meta: map[string]string{"fn": "querySelector", "selector": `'[id="board-root"] .inner'`},
		},
	}
	_, edges, _ := LinkDOMContracts(nodes)
	if len(edges) != 1 || edges[0].Confidence != graph.ConfidenceStatic {
		t.Fatalf("edges = %+v, want one static id-attribute match", edges)
	}
	if edges[0].From != nodes[0].ID {
		t.Errorf("edge From = %q, want the component %q", edges[0].From, nodes[0].ID)
	}
}

// TestLinkDOMContracts_NoAttributeSelector_Ignored: selectors with no
// `[attr="…"]` token (plain #id/.class or non-attribute compound selectors)
// are LinkDOMDefinitions' job, not this pass's — no edge, no unresolved.
func TestLinkDOMContracts_NoAttributeSelector_Ignored(t *testing.T) {
	nodes := []graph.Node{
		{
			ID:   "app:assets/js/x.js:dom_target:query_selector:1",
			Type: graph.NodeTypeDOMTarget, Service: "app", File: "assets/js/x.js", Line: 1,
			Meta: map[string]string{"fn": "querySelector", "selector": `"#board-root"`},
		},
	}
	_, edges, unresolved := LinkDOMContracts(nodes)
	if len(edges) != 0 || len(unresolved) != 0 {
		t.Fatalf("edges=%+v unresolved=%+v, want both empty", edges, unresolved)
	}
}
