package parser

import (
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// TestJSY4_Consumes verifies the client-side response-type capture: a typed
// `await res.json()` (annotated or asserted) emits a consumes edge to the
// same-file interface, an array decode records container=slice, and an untyped
// decode emits nothing (ledgered, #12).
func TestJSY4_Consumes(t *testing.T) {
	t.Parallel()
	src := []byte(`interface NodeDetail { node: string; edges: string[] }

async function load() {
  const res = await fetch("/api/node");
  const d: NodeDetail = await res.json();
  return d;
}

async function loadList() {
  const res = await fetch("/api/nodes");
  const items = (await res.json()) as NodeDetail[];
  return items;
}

async function loadUntyped() {
  const res = await fetch("/x");
  const data = await res.json();
  return data;
}
`)
	_, edges, _, _ := extractJSVariables("x.ts", "web", "typescript", "typescript", src)

	consumes := map[string]graph.Edge{} // fn substring → edge
	for _, e := range edges {
		if e.Type != graph.EdgeTypeConsumes {
			continue
		}
		if !strings.Contains(e.To, ":interface:NodeDetail:") {
			t.Errorf("consumes target = %s, want NodeDetail interface", e.To)
		}
		switch {
		case strings.Contains(e.From, ":function:load:"):
			consumes["load"] = e
		case strings.Contains(e.From, ":function:loadList:"):
			consumes["loadList"] = e
		case strings.Contains(e.From, ":function:loadUntyped:"):
			consumes["untyped"] = e
		}
	}

	if _, ok := consumes["load"]; !ok {
		t.Error("missing consumes edge: load → NodeDetail (annotated decode)")
	}
	if e, ok := consumes["loadList"]; !ok {
		t.Error("missing consumes edge: loadList → NodeDetail (as-assertion)")
	} else if e.Meta["container"] != "slice" {
		t.Errorf("loadList container = %q, want slice", e.Meta["container"])
	}
	if _, ok := consumes["untyped"]; ok {
		t.Error("untyped decode must be ledgered (no consumes edge)")
	}
}
