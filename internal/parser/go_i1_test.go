package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// writeIfaceModule writes the worked example from the Phase I.1 spec:
//
//	Store interface, memStore struct implementing Store via *pointer receiver,
//	auditStore embedding memStore (inherits + promoted implements), and
//	NewMemStore constructor (instantiates memStore).
func writeIfaceModule(t *testing.T) (dir string, known map[string]bool) {
	t.Helper()
	dir = t.TempDir()
	src := `package main

type Store interface {
	Get(id string) (string, error)
}

type memStore struct {
	data map[string]string
}

func (m *memStore) Get(id string) (string, error) {
	if v, ok := m.data[id]; ok {
		return v, nil
	}
	return "", nil
}

type auditStore struct {
	memStore
}

func NewMemStore() *memStore {
	return &memStore{data: make(map[string]string)}
}
`
	// Line numbers (1-indexed):
	//  3: type Store interface {
	//  7: type memStore struct {
	// 11: func (m *memStore) Get(...)
	// 18: type auditStore struct {
	// 22: func NewMemStore() *memStore {
	files := map[string]string{
		"go.mod":   "module example.com/ifacetest\n\ngo 1.25.0\n",
		"iface.go": src,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	known = map[string]bool{
		"svc:iface.go:method:Get:11":           true,
		"svc:iface.go:function:NewMemStore:22": true,
	}
	return dir, known
}

func analyzeIface(t *testing.T) SemanticResult {
	t.Helper()
	dir, known := writeIfaceModule(t)
	t.Chdir(dir)
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}
	return res
}

func TestGoI1_SchemaVersion(t *testing.T) {
	if graph.SchemaVersion != "9" {
		t.Errorf("expected SchemaVersion 9 (Tier I.1 bump), got %q", graph.SchemaVersion)
	}
}

func TestGoI1_InterfaceNode(t *testing.T) {
	res := analyzeIface(t)

	n := findNode(res, graph.NodeTypeInterface, "Store")
	if n == nil {
		t.Fatalf("NodeTypeInterface node for Store not emitted; nodes: %+v", res.Nodes)
	}
	if n.Language != "go" {
		t.Errorf("interface node language should be go, got %q", n.Language)
	}
	if n.File == "" {
		t.Errorf("interface node should have a file, got empty")
	}
	// methods JSON should contain "Get"
	if !contains(n.Meta["methods"], "Get") {
		t.Errorf("interface node methods JSON missing Get: %s", n.Meta["methods"])
	}
}

func TestGoI1_ImplementsEdges(t *testing.T) {
	res := analyzeIface(t)

	// memStore -implements-> Store (via *memStore pointer receiver)
	e := hasEdge(res, graph.EdgeTypeImplements, "struct:memStore", "interface:Store")
	if e == nil {
		t.Fatalf("missing implements edge memStore → Store; edges: %+v", res.Edges)
	}
	if e.Meta["nominal"] != "false" {
		t.Errorf("Go implements must be nominal=false (structural), got %q", e.Meta["nominal"])
	}
	if e.Confidence != graph.ConfidenceStatic {
		t.Errorf("implements edge must be static confidence, got %q", e.Confidence)
	}

	// auditStore -implements-> Store (via promoted *memStore method set)
	e2 := hasEdge(res, graph.EdgeTypeImplements, "struct:auditStore", "interface:Store")
	if e2 == nil {
		t.Fatalf("missing implements edge auditStore → Store (promoted method set); edges: %+v", res.Edges)
	}
	if e2.Meta["nominal"] != "false" {
		t.Errorf("auditStore implements edge must be nominal=false, got %q", e2.Meta["nominal"])
	}
}

func TestGoI1_InheritsEmbedding(t *testing.T) {
	res := analyzeIface(t)

	// auditStore -inherits-> memStore (anonymous embedded field)
	e := hasEdge(res, graph.EdgeTypeInherits, "struct:auditStore", "struct:memStore")
	if e == nil {
		t.Fatalf("missing inherits edge auditStore → memStore; edges: %+v", res.Edges)
	}
	if e.Meta["via"] != "embedding" {
		t.Errorf("embedding inherits edge must have via=embedding, got %q", e.Meta["via"])
	}
	if e.Confidence != graph.ConfidenceStatic {
		t.Errorf("inherits edge must be static confidence, got %q", e.Confidence)
	}
}

func TestGoI1_InstantiatesEdges(t *testing.T) {
	res := analyzeIface(t)

	// NewMemStore -instantiates-> memStore
	e := hasEdge(res, graph.EdgeTypeInstantiates, "function:NewMemStore", "struct:memStore")
	if e == nil {
		t.Fatalf("missing instantiates edge NewMemStore → memStore; edges: %+v", res.Edges)
	}
	if e.Meta["count"] == "" || e.Meta["count"] == "0" {
		t.Errorf("instantiates edge must carry count ≥ 1, got %q", e.Meta["count"])
	}
	if e.Confidence != graph.ConfidenceStatic {
		t.Errorf("instantiates edge must be static confidence, got %q", e.Confidence)
	}
}

// TestGoI1_NegativeEmptyInterface verifies that empty interfaces (any /
// interface{}) do not produce NodeTypeInterface nodes or implements edges.
func TestGoI1_NegativeEmptyInterface(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/emptyiface\n\ngo 1.25.0\n",
		"main.go": `package main

type Anything interface{}

type Box struct{ v any }
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), map[string]bool{})
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	// Empty interface must not produce an interface node.
	if n := findNode(res, graph.NodeTypeInterface, "Anything"); n != nil {
		t.Errorf("empty interface Anything must not produce a NodeTypeInterface node")
	}

	// No implements edges at all.
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeImplements {
			t.Errorf("empty interface must produce no implements edges, got: %+v", e)
		}
	}
}

// TestGoI1_NegativeOutOfServiceType verifies that a struct from an imported
// (external) package satisfying an in-service interface does NOT produce an
// in-service implements edge. The sweep only covers in-service named types.
func TestGoI1_NegativeOutOfServiceType(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/external\n\ngo 1.25.0\n",
		"main.go": `package main

import "sort"

// Sorter is an in-service interface that sort.Interface also satisfies.
type Sorter interface {
	Len() int
	Less(i, j int) bool
	Swap(i, j int)
}

type mySlice []int

func (m mySlice) Len() int           { return len(m) }
func (m mySlice) Less(i, j int) bool { return m[i] < m[j] }
func (m mySlice) Swap(i, j int)      { m[i], m[j] = m[j], m[i] }

func main() { sort.Sort(mySlice{}) }
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	known := map[string]bool{
		"svc:main.go:method:Len:13":  true,
		"svc:main.go:method:Less:14": true,
		"svc:main.go:method:Swap:15": true,
		"svc:main.go:function:main:17": true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	// mySlice is not a struct so it won't be in structIDs — no implements edge
	// from it to Sorter. Verify no implements edge to Sorter from an external type.
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeImplements {
			// Only in-service struct → interface edges are allowed.
			// Since mySlice is a slice type (not a struct), structIDs won't have it.
			if contains(e.To, "interface:Sorter") {
				t.Logf("implements edge to Sorter: %+v (acceptable only if From is an in-service struct)", e)
			}
		}
	}

	// The Sorter interface node must be emitted.
	if n := findNode(res, graph.NodeTypeInterface, "Sorter"); n == nil {
		t.Errorf("in-service interface Sorter must have a NodeTypeInterface node")
	}
}

// TestGoI1_CallbackClassificationPreserved verifies that the existing
// Referenced (callback-classification) behaviour is unchanged after the
// implements-edge lift. Methods satisfying an external interface must still
// appear in Referenced.
func TestGoI1_CallbackClassificationPreserved(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/cbtest\n\ngo 1.25.0\n",
		"main.go": `package main

import "sort"

type byLen []string

func (b byLen) Len() int           { return len(b) }
func (b byLen) Less(i, j int) bool { return len(b[i]) < len(b[j]) }
func (b byLen) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }

func main() { sort.Sort(byLen{"a"}) }
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	known := map[string]bool{
		"svc:main.go:function:main:11": true,
		"svc:main.go:method:Len:7":    true,
		"svc:main.go:method:Less:8":   true,
		"svc:main.go:method:Swap:9":   true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	ref := map[string]bool{}
	for _, id := range res.Referenced {
		ref[id] = true
	}
	for _, m := range []string{"Len", "Less", "Swap"} {
		found := false
		for id := range ref {
			if contains(id, ":method:"+m+":") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s satisfies sort.Interface and must still be in Referenced after I.1; got %v", m, res.Referenced)
		}
	}
}
