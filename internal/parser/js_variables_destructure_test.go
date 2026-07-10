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

// Calling a signal accessor or setter reads the module binding.
func TestJSVariables_SignalCallsAreReads(t *testing.T) {
	nodes, edges := parseJSSignalFixture(t)
	_ = nodes

	if e := jsEdge(edges, graph.EdgeTypeReads, "clearNotification", "setNotification"); e == nil {
		t.Error("expected reads edge clearNotification → setNotification")
	}
	if e := jsEdge(edges, graph.EdgeTypeReads, "showNotification", "notification"); e == nil {
		t.Error("expected reads edge showNotification → notification (accessor call)")
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

// Calls to declared functions must still not read variables.
func TestJSVariables_FunctionCallsStillNotReads(t *testing.T) {
	_, edges := parseJSSignalFixture(t)

	if e := jsEdge(edges, graph.EdgeTypeReads, "showNotification", "clearNotification"); e != nil {
		t.Error("function-to-function call must not produce a reads edge")
	}
}
