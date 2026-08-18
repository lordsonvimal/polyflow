package parser

import (
	"strings"
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
	t.Parallel()
	nodes, edges, unresolved, _ := extractJSVariables("Toolbar.tsx", "web", "typescript", "tsx", []byte(y7JSXSource))

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

// y7InlineSource exercises inline-arrow handler linking: the arrow calls a
// same-file function (`save`, resolvable → edge) and a store method
// (`store.refresh()`, member call → no edge), plus an arrow with no resolvable
// call (`() => noop` — ledgered).
const y7InlineSource = `import { store } from "./store";

function save() {
  fetch("/api/save");
}

function Form() {
  return (
    <div>
      <button onClick={() => save()}>Save</button>
      <button onClick={() => store.refresh()}>Refresh</button>
      <button onClick={() => { const x = 1; return x; }}>Noop</button>
    </div>
  );
}
`

// TestJSY7_InlineHandler verifies an inline arrow handler binds the element to
// each same-file function it invokes (via:jsx, handler:inline, inferred), while
// member/store calls and no-op arrows are ledgered (#12).
func TestJSY7_InlineHandler(t *testing.T) {
	t.Parallel()
	_, edges, unresolved, _ := extractJSVariables("Form.tsx", "web", "typescript", "tsx", []byte(y7InlineSource))

	e := edgeFromToSub(edges, graph.EdgeTypeDOMListen, ":element:button:", ":function:save:")
	if e == nil {
		t.Fatalf("missing inline dom_listen button → save; edges: %+v", edges)
	}
	if e.Meta["handler"] != "inline" {
		t.Errorf("inline dom_listen handler = %q, want inline", e.Meta["handler"])
	}
	if e.Meta["event"] != "click" || e.Meta["via"] != "jsx" {
		t.Errorf("inline dom_listen meta = %+v, want event=click via=jsx", e.Meta)
	}
	if e.Confidence != graph.ConfidenceInferred {
		t.Errorf("inline dom_listen confidence = %q, want inferred", e.Confidence)
	}
	// store.refresh() is a member call — no same-file function node, no edge.
	for _, ed := range edges {
		if ed.Type == graph.EdgeTypeDOMListen && strings.Contains(ed.To, ":function:refresh:") {
			t.Errorf("member call store.refresh() should not emit dom_listen, got %+v", ed)
		}
	}
	// The no-op arrow yields no resolvable call → ledgered.
	var inlineLedgered bool
	for _, u := range unresolved {
		if u.Kind == "dom_listen_unresolved" && u.Name == "click:inline" {
			inlineLedgered = true
		}
	}
	if !inlineLedgered {
		t.Errorf("no-op inline arrow not ledgered; unresolved: %+v", unresolved)
	}
}

// y7TypeUseSource exercises same-file uses_type: an interface field references
// another interface, and a const's type annotation references a third.
const y7TypeUseSource = `interface Ref {
  id: string;
}

interface Detail {
  refs: Ref[];
}

const styles: Ref[] = [];
`

// TestJSY7_TypeUses verifies same-file type references emit uses_type edges so a
// declared-but-never-instantiated TS type is not left dangling.
func TestJSY7_TypeUses(t *testing.T) {
	t.Parallel()
	_, edges, _, _ := extractJSVariables("types.ts", "web", "typescript", "typescript", []byte(y7TypeUseSource))

	// Detail interface references Ref in a member type.
	if e := edgeFromToSub(edges, graph.EdgeTypeUsesType, ":interface:Detail:", ":interface:Ref:"); e == nil {
		t.Errorf("missing uses_type Detail → Ref (member type); edges: %+v", edges)
	}
	// const styles: Ref[] references Ref in its annotation.
	if e := edgeFromToSub(edges, graph.EdgeTypeUsesType, ":variable:styles:", ":interface:Ref:"); e == nil {
		t.Errorf("missing uses_type styles → Ref (annotation); edges: %+v", edges)
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
	t.Parallel()
	_, edges, _, _ := extractJSVariables("wire.ts", "web", "typescript", "typescript", []byte(y7ListenerSource))

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
