package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

const jsSignalFixture = `import { createSignal } from "solid-js";

const [notification, setNotification] = createSignal(null);
const { host, port } = loadConfig();

function clearNotification() {
  setNotification(null);
}

function showNotification(msg) {
  setNotification(msg);
  return notification();
}

const Panel = () => {
  const [open, setOpen] = createSignal(false);
  const toggle = () => setOpen(!open());
  return toggle;
};
`

func parseJSSignalFixture(t *testing.T) ([]graph.Node, []graph.Edge) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "signals.ts")
	if err := os.WriteFile(file, []byte(jsSignalFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(file)
	return extractJSVariables(file, "web", "typescript", "typescript", src)
}

// Destructured module bindings become variable nodes (Solid signals were
// previously invisible: "destructuring patterns are not tracked in v1").
func TestJSVariables_DestructuredModuleBindings(t *testing.T) {
	nodes, _ := parseJSSignalFixture(t)

	for _, label := range []string{"notification", "setNotification", "host", "port"} {
		n := jsNode(nodes, graph.NodeTypeVariable, label)
		if n == nil {
			t.Fatalf("expected variable node for destructured binding %q", label)
		}
		if n.Meta["destructured"] != "true" || n.Meta["scope"] != "module" {
			t.Errorf("%s meta = %v, want destructured module binding", label, n.Meta)
		}
	}
	if n := jsNode(nodes, graph.NodeTypeVariable, "notification"); n.Meta["init"] != "createSignal" {
		t.Errorf("notification init = %q, want createSignal", n.Meta["init"])
	}
}

// Calling a signal accessor reads the binding; calling its setter writes it —
// the read/write split is what makes variable impact queries answer "who
// mutates this state" vs "who depends on it".
func TestJSVariables_SignalCallSemantics(t *testing.T) {
	nodes, edges := parseJSSignalFixture(t)

	if e := jsEdge(edges, graph.EdgeTypeWrites, "clearNotification", "setNotification"); e == nil {
		t.Error("expected writes edge clearNotification → setNotification (setter call mutates)")
	}
	if e := jsEdge(edges, graph.EdgeTypeReads, "showNotification", "notification"); e == nil {
		t.Error("expected reads edge showNotification → notification (accessor call)")
	}
	if n := jsNode(nodes, graph.NodeTypeVariable, "setNotification"); n == nil || n.Meta["setter"] != "true" {
		t.Error("setNotification must carry setter meta for linker retyping")
	}
	if n := jsNode(nodes, graph.NodeTypeVariable, "notification"); n != nil && n.Meta["setter"] == "true" {
		t.Error("accessor must not be marked as setter")
	}
}

// Destructured locals are capturable: a closure using a component-local
// signal produces captures edges on the binding.
func TestJSVariables_DestructuredLocalCapture(t *testing.T) {
	_, edges := parseJSSignalFixture(t)

	if e := jsEdge(edges, graph.EdgeTypeCaptures, "toggle", "open"); e == nil {
		t.Error("expected captures edge toggle → open (destructured local signal)")
	}
}

const jsReactiveFixture = `import { createSignal, createMemo, createEffect } from "solid-js";

const [items, setItems] = createSignal([]);

export const filtered = createMemo(() => items().filter(Boolean));

createEffect(() => {
  setItems([]);
});
`

// Module-level reactive derivations (const x = createMemo(() => …)) must
// attribute their reads to the declared variable — this is the edge that was
// silently missing for every store-derivation file (regression: derived.ts
// had zero outgoing edges, making variable impact queries wrong).
func TestJSVariables_ModuleLevelReactiveAttribution(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "derived.ts")
	if err := os.WriteFile(file, []byte(jsReactiveFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(file)
	nodes, edges := extractJSVariables(file, "web", "typescript", "typescript", src)

	if e := jsEdge(edges, graph.EdgeTypeReads, "variable:filtered", "variable:items"); e == nil {
		t.Fatalf("expected reads edge filtered → items (memo body); edges: %+v", edges)
	}
	// Bare module-level effect: attribution falls back to the synthetic
	// (module) node, and calling the setter is a write.
	if e := jsEdge(edges, graph.EdgeTypeWrites, "(module)", "variable:setItems"); e == nil {
		t.Fatalf("expected writes edge (module) → setItems; edges: %+v", edges)
	}
	if n := jsNode(nodes, graph.NodeTypeFunction, "(module)"); n == nil {
		t.Error("expected synthetic (module) node to be materialised")
	}
}

const jsDefaultParamFixture = `export const MAX_DIM = 8000;

export function scale(cy: unknown, maxDim = MAX_DIM) {
  return maxDim;
}
`

// A module constant used as a parameter default is a read, not a parameter
// binding (regression: MAX_EXPORT_DIM was shadowed by its own default-value
// position and stayed isolated).
func TestJSVariables_DefaultParamReadsModuleConst(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "export.ts")
	if err := os.WriteFile(file, []byte(jsDefaultParamFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(file)
	_, edges := extractJSVariables(file, "web", "typescript", "typescript", src)

	if e := jsEdge(edges, graph.EdgeTypeReads, "function:scale", "variable:MAX_DIM"); e == nil {
		t.Fatalf("expected reads edge scale → MAX_DIM (default param value); edges: %+v", edges)
	}
}

// Calls to declared functions must still not read variables.
func TestJSVariables_FunctionCallsStillNotReads(t *testing.T) {
	_, edges := parseJSSignalFixture(t)

	if e := jsEdge(edges, graph.EdgeTypeReads, "showNotification", "clearNotification"); e != nil {
		t.Error("function-to-function call must not produce a reads edge")
	}
}
