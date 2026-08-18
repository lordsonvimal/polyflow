package parser

import (
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// y6Source is a Solid component exercising the Y.6 render tail: a createResource
// accessor (`d`) driven by a loader fn, a plain createSignal accessor (`n`), and
// JSX interpolations reading both — text `{d().label}` and `{n()}`.
const y6Source = `import { createResource, createSignal } from "solid-js";

async function loadNode(): Promise<{ label: string }> {
  const res = await fetch("/api/node");
  return res.json();
}

function Detail() {
  const [d] = createResource(loadNode);
  const [n, setN] = createSignal(0);
  setN(1);
  return (
    <div>
      <span>{d().label}</span>
      <b>{n()}</b>
    </div>
  );
}
`

// TestJSY6_ResourceMeta verifies the createResource accessor node is stamped
// reactive=resource and carries the loader fn name for the linker join.
func TestJSY6_ResourceMeta(t *testing.T) {
	t.Parallel()
	nodes, _, _, _ := extractJSVariables("Detail.tsx", "web", "typescript", "tsx", []byte(y6Source))

	var acc *graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeVariable && nodes[i].Label == "d" {
			acc = &nodes[i]
		}
	}
	if acc == nil {
		t.Fatalf("no accessor variable node for createResource binding d; nodes: %+v", nodes)
	}
	if acc.Meta["reactive"] != "resource" {
		t.Errorf("accessor reactive = %q, want resource", acc.Meta["reactive"])
	}
	if acc.Meta["resource_fn"] != "loadNode" {
		t.Errorf("accessor resource_fn = %q, want loadNode", acc.Meta["resource_fn"])
	}
}

// TestJSY6_DomWrite verifies JSX interpolations reading a signal accessor emit
// signal→element dom_write edges to a minted element node.
func TestJSY6_DomWrite(t *testing.T) {
	t.Parallel()
	nodes, edges, _, _ := extractJSVariables("Detail.tsx", "web", "typescript", "tsx", []byte(y6Source))

	// d() → <span> dom_write
	if e := edgeFromToSub(edges, graph.EdgeTypeDOMWrite, ":variable:d:", ":element:span:"); e == nil {
		t.Errorf("missing dom_write d → span; edges: %+v", edges)
	} else if e.Meta["via"] != "jsx" {
		t.Errorf("dom_write via = %q, want jsx", e.Meta["via"])
	}
	// n() → <b> dom_write
	if e := edgeFromToSub(edges, graph.EdgeTypeDOMWrite, ":variable:n:", ":element:b:"); e == nil {
		t.Errorf("missing dom_write n → b; edges: %+v", edges)
	}

	// The element nodes must exist (no dangling dom_write endpoint, #10).
	var haveSpan bool
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeElement && nodes[i].Label == "span" {
			haveSpan = true
			if nodes[i].Meta["tag"] != "span" {
				t.Errorf("element meta.tag = %q, want span", nodes[i].Meta["tag"])
			}
		}
	}
	if !haveSpan {
		t.Errorf("no element node for <span>; nodes: %+v", nodes)
	}

	// The setter setN must not be treated as a DOM-writing accessor.
	if e := edgeFromToSub(edges, graph.EdgeTypeDOMWrite, ":variable:setN:", ":element:"); e != nil {
		t.Errorf("setter setN must not source a dom_write, got %+v", e)
	}

	// The accessor node label should be a bare signal name, not a call.
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeElement && strings.Contains(nodes[i].ID, ":element::") {
			t.Errorf("element node with empty tag: %+v", nodes[i])
		}
	}
}
