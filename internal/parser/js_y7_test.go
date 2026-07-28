package parser

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// y7JSXSource is a Solid/React component exercising the Y.7 event head: a JSX
// `onClick={onRefresh}` binding a bare same-file handler, plus an inline arrow
// (`onInput={() => …}`) that carries no stable node (ledgered), and a handler
// imported from elsewhere (`onClose`) that is unresolved.
const y7JSXSource = `import { onClose } from "./actions";

function onRefresh() {
  fetch("/api/graph");
}

function Toolbar() {
  return (
    <div>
      <button onClick={onRefresh}>Refresh</button>
      <input onInput={() => doThing()} />
      <button onClick={onClose}>Close</button>
    </div>
  );
}
`

// TestJSY7_JSXEvent verifies a JSX event attribute binding a bare same-file
// handler emits element→function dom_listen, that the element node is minted
// (no dangling endpoint, #10), and that unresolved handlers are ledgered (#12).
func TestJSY7_JSXEvent(t *testing.T) {
	nodes, edges, unresolved := extractJSVariables("Toolbar.tsx", "web", "typescript", "tsx", []byte(y7JSXSource))

	e := edgeFromToSub(edges, graph.EdgeTypeDOMListen, ":element:button:", ":function:onRefresh:")
	if e == nil {
		t.Fatalf("missing dom_listen button → onRefresh; edges: %+v", edges)
	}
	if e.Meta["event"] != "click" {
		t.Errorf("dom_listen event = %q, want click", e.Meta["event"])
	}
	if e.Meta["via"] != "jsx" {
		t.Errorf("dom_listen via = %q, want jsx", e.Meta["via"])
	}
	if e.Confidence != graph.ConfidenceStatic {
		t.Errorf("dom_listen confidence = %q, want static", e.Confidence)
	}

	// The element endpoint must exist (#10).
	var haveButton bool
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeElement && nodes[i].Label == "button" {
			haveButton = true
		}
	}
	if !haveButton {
		t.Errorf("no element node for <button>; nodes: %+v", nodes)
	}

	// Inline arrow handler → no dom_listen edge fabricated.
	for _, ed := range edges {
		if ed.Type == graph.EdgeTypeDOMListen && ed.Meta["event"] == "input" {
			t.Errorf("inline arrow handler should not emit dom_listen, got %+v", ed)
		}
	}

	// Cross-file handler onClose → ledgered, no edge.
	if e := edgeFromToSub(edges, graph.EdgeTypeDOMListen, ":element:button:", ":function:onClose:"); e != nil {
		t.Errorf("cross-file handler onClose should be ledgered, got edge %+v", e)
	}
	var ledgered bool
	for _, u := range unresolved {
		if u.Kind == "dom_listen_unresolved" && u.Name == "onClose" {
			ledgered = true
		}
	}
	if !ledgered {
		t.Errorf("onClose not ledgered as dom_listen_unresolved; unresolved: %+v", unresolved)
	}
}

// y7ListenerSource exercises the vanilla addEventListener path: a same-file
// handler resolved to its function node, plus a dynamic event name and an
// inline handler that must not fabricate edges.
const y7ListenerSource = `function onScroll() {
  fetch("/api/telemetry");
}

function wire(evt) {
  document.addEventListener("scroll", onScroll);
  window.addEventListener(evt, onScroll);
  window.addEventListener("resize", () => layout());
}
`

// TestJSY7_AddEventListener verifies el.addEventListener("evt", handler) emits
// element→function dom_listen for a resolvable handler, and ledgers/skips the
// dynamic-event-name and inline-handler cases (#12).
func TestJSY7_AddEventListener(t *testing.T) {
	_, edges, _ := extractJSVariables("wire.ts", "web", "typescript", "typescript", []byte(y7ListenerSource))

	e := edgeFromToSub(edges, graph.EdgeTypeDOMListen, ":element:document:", ":function:onScroll:")
	if e == nil {
		t.Fatalf("missing dom_listen document → onScroll; edges: %+v", edges)
	}
	if e.Meta["event"] != "scroll" {
		t.Errorf("dom_listen event = %q, want scroll", e.Meta["event"])
	}
	if e.Meta["via"] != "add_event_listener" {
		t.Errorf("dom_listen via = %q, want add_event_listener", e.Meta["via"])
	}

	// Dynamic event name (window.addEventListener(evt, onScroll)) — no edge.
	if e := edgeFromToSub(edges, graph.EdgeTypeDOMListen, ":element:window:", ":function:onScroll:"); e != nil {
		t.Errorf("dynamic event name should not emit dom_listen, got %+v", e)
	}
	// Inline handler (resize) — no fabricated edge.
	for _, ed := range edges {
		if ed.Type == graph.EdgeTypeDOMListen && ed.Meta["event"] == "resize" {
			t.Errorf("inline addEventListener handler should not emit dom_listen, got %+v", ed)
		}
	}
}
