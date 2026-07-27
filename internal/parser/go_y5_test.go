package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// writeY5Module writes a fixture exercising the Y.5 interface joins:
//
//	type Store interface { Get(id string) (string, error) }
//	func Use(s Store) { s.Get("x") }   // uses_type (param) + calls (invoke)
//	func New() Store { ... }           // uses_type (return)
func writeY5Module(t *testing.T) (dir string, known map[string]bool) {
	t.Helper()
	dir = t.TempDir()

	src := `package srv

type Store interface {
	Get(id string) (string, error)
}

type memStore struct{}

func (m *memStore) Get(id string) (string, error) { return "", nil }

func Use(s Store) (string, error) {
	return s.Get("x")
}

func New() Store {
	return &memStore{}
}
`
	files := map[string]string{
		"go.mod":     "module y5test\n\ngo 1.25.0\n",
		"srv/srv.go": src,
	}
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Function node IDs the pattern matcher would have emitted.
	known = map[string]bool{
		"svc:srv/srv.go:method:Get:9":    true,
		"svc:srv/srv.go:function:Use:11": true,
		"svc:srv/srv.go:function:New:15": true,
	}
	return dir, known
}

func analyzeY5(t *testing.T) SemanticResult {
	t.Helper()
	dir, known := writeY5Module(t)
	t.Chdir(dir)
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}
	return res
}

func edgeFromToSub(edges []graph.Edge, typ graph.EdgeType, fromSub, toSub string) *graph.Edge {
	for i := range edges {
		e := &edges[i]
		if e.Type == typ && strings.Contains(e.From, fromSub) && strings.Contains(e.To, toSub) {
			return e
		}
	}
	return nil
}

// TestGoY5_UsesTypeInterface verifies a function with an interface-typed
// parameter and one with an interface-typed return each emit uses_type edges
// to the Store interface node.
func TestGoY5_UsesTypeInterface(t *testing.T) {
	res := analyzeY5(t)

	if e := edgeFromToSub(res.Edges, graph.EdgeTypeUsesType, ":function:Use:", ":interface:Store:"); e == nil {
		t.Errorf("missing uses_type edge Use → Store (interface param); edges: %+v", res.Edges)
	}
	if e := edgeFromToSub(res.Edges, graph.EdgeTypeUsesType, ":function:New:", ":interface:Store:"); e == nil {
		t.Errorf("missing uses_type edge New → Store (interface return); edges: %+v", res.Edges)
	}
}

// TestGoY5_DispatchCalls verifies that s.Get(...) — a call through the Store
// interface value (SSA invoke) — emits a calls edge from Use to a synthetic
// interface-method node (Store.Get), tagged via=invoke.
func TestGoY5_DispatchCalls(t *testing.T) {
	res := analyzeY5(t)

	e := edgeFromToSub(res.Edges, graph.EdgeTypeCalls, ":function:Use:", ":interface:Store:")
	if e == nil {
		t.Fatalf("missing dispatch calls edge Use → Store.Get; edges: %+v", res.Edges)
	}
	if !strings.HasSuffix(e.To, ":m:Get") {
		t.Errorf("dispatch calls target = %s, want interface-method node …:m:Get", e.To)
	}
	if e.Meta["via"] != "invoke" {
		t.Errorf("dispatch calls via = %q, want invoke", e.Meta["via"])
	}

	// The interface-method node must exist and carry kind=interface_method.
	var mnode *graph.Node
	for i := range res.Nodes {
		if res.Nodes[i].ID == e.To {
			mnode = &res.Nodes[i]
		}
	}
	if mnode == nil {
		t.Fatalf("interface-method node %s not emitted", e.To)
	}
	if mnode.Type != graph.NodeTypeMethod {
		t.Errorf("interface-method node type = %q, want method", mnode.Type)
	}
	if mnode.Meta["kind"] != "interface_method" {
		t.Errorf("interface-method node kind = %q, want interface_method", mnode.Meta["kind"])
	}
	if mnode.Meta["interface"] == "" || !strings.Contains(mnode.Meta["interface"], ":interface:Store:") {
		t.Errorf("interface-method node meta.interface = %q, want Store interface node ID", mnode.Meta["interface"])
	}
}
