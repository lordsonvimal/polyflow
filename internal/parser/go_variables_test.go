package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// writeVarModule lays out a Go module exercising every variable-tracking
// rule: package globals and consts, a struct with tagged fields, global
// mutation and read, a closure capturing and mutating a local, and a global
// slice passed by reference at a call site.
func writeVarModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/vartest\n\ngo 1.25.0\n",
		"main.go": `package main

var counter int
var registry = map[string]string{}

const limit = 10

type User struct {
	Name string ` + "`json:\"name\"`" + `
	Age  int
}

func main() {
	counter = 1
	registry["a"] = "b"
	process(items)
	makeCounter()
}

func process(list []string) int {
	return counter
}

func save(u *User) {}
`,
		"closure.go": `package main

var items = []string{"x"}

func makeCounter() func() int {
	local := 0
	return func() int {
		local++
		return local
	}
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func analyzeVars(t *testing.T) SemanticResult {
	t.Helper()
	dir := writeVarModule(t)
	t.Chdir(dir)

	known := map[string]bool{
		"svc:main.go:function:main:13":           true,
		"svc:main.go:function:process:20":        true,
		"svc:main.go:function:save:24":           true,
		"svc:closure.go:function:makeCounter:5": true,
	}
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}
	return res
}

func findNode(res SemanticResult, typ graph.NodeType, label string) *graph.Node {
	for i := range res.Nodes {
		if res.Nodes[i].Type == typ && res.Nodes[i].Label == label {
			return &res.Nodes[i]
		}
	}
	return nil
}

func hasEdge(res SemanticResult, typ graph.EdgeType, fromSub, toSub string) *graph.Edge {
	for i := range res.Edges {
		e := &res.Edges[i]
		if e.Type == typ && contains(e.From, fromSub) && contains(e.To, toSub) {
			return e
		}
	}
	return nil
}

func contains(s, sub string) bool {
	return sub == "" || (len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestGoVariables_PackageGlobalsAndConsts(t *testing.T) {
	res := analyzeVars(t)

	counter := findNode(res, graph.NodeTypeVariable, "counter")
	if counter == nil {
		t.Fatalf("missing variable node for counter; nodes: %+v", res.Nodes)
	}
	if counter.Meta["data_type"] != "int" || counter.Meta["scope"] != "package" || counter.Meta["mutable"] != "true" {
		t.Errorf("counter meta wrong: %+v", counter.Meta)
	}
	if counter.File != "main.go" {
		t.Errorf("counter file should be workspace-relative, got %q", counter.File)
	}

	lim := findNode(res, graph.NodeTypeVariable, "limit")
	if lim == nil {
		t.Fatal("missing const node for limit")
	}
	if lim.Meta["kind"] != "const" || lim.Meta["mutable"] != "false" {
		t.Errorf("limit meta wrong: %+v", lim.Meta)
	}
}

func TestGoVariables_StructWithFields(t *testing.T) {
	res := analyzeVars(t)

	user := findNode(res, graph.NodeTypeStruct, "User")
	if user == nil {
		t.Fatalf("missing struct node for User; nodes: %+v", res.Nodes)
	}
	if user.Meta["field_count"] != "2" {
		t.Errorf("want 2 fields, got %s", user.Meta["field_count"])
	}
	if !contains(user.Meta["fields"], `"name":"Name"`) || !contains(user.Meta["fields"], `json:\"name\"`) {
		t.Errorf("fields JSON missing name/tag: %s", user.Meta["fields"])
	}

	if hasEdge(res, graph.EdgeTypeUsesType, "function:save", "struct:User") == nil {
		t.Errorf("missing uses_type edge save -> User; edges: %+v", res.Edges)
	}
}

func TestGoVariables_WritesAndReads(t *testing.T) {
	res := analyzeVars(t)

	if e := hasEdge(res, graph.EdgeTypeWrites, "function:main", "variable:counter"); e == nil {
		t.Errorf("missing writes edge main -> counter")
	} else if e.Confidence != graph.ConfidenceStatic {
		t.Errorf("semantic edges must be static confidence, got %q", e.Confidence)
	}
	if hasEdge(res, graph.EdgeTypeWrites, "function:main", "variable:registry") == nil {
		t.Errorf("missing map_update writes edge main -> registry")
	}
	if hasEdge(res, graph.EdgeTypeReads, "function:process", "variable:counter") == nil {
		t.Errorf("missing reads edge process -> counter")
	}
}

func TestGoVariables_ClosureCapture(t *testing.T) {
	res := analyzeVars(t)

	local := findNode(res, graph.NodeTypeVariable, "local")
	if local == nil {
		t.Fatalf("missing captured variable node for local; nodes: %+v", res.Nodes)
	}
	if local.Meta["scope"] != "captured" {
		t.Errorf("local scope should be captured, got %s", local.Meta["scope"])
	}

	cap := hasEdge(res, graph.EdgeTypeCaptures, "function:makeCounter", "variable:local")
	if cap == nil {
		t.Fatalf("missing captures edge makeCounter -> local")
	}
	if cap.Meta["by"] != "ref" {
		t.Errorf("Go closures capture by reference, got %s", cap.Meta["by"])
	}
	if hasEdge(res, graph.EdgeTypeWrites, "function:makeCounter", "variable:local") == nil {
		t.Errorf("missing closure-mutation writes edge makeCounter -> local")
	}
}

func TestGoVariables_FlowsToByRef(t *testing.T) {
	res := analyzeVars(t)

	flow := hasEdge(res, graph.EdgeTypeFlowsTo, "variable:items", "function:process")
	if flow == nil {
		t.Fatalf("missing flows_to edge items -> process; edges: %+v", res.Edges)
	}
	if flow.Meta["mode"] != "ref" {
		t.Errorf("slice argument should flow by ref, got %s", flow.Meta["mode"])
	}
	if flow.Meta["data_type"] != "[]string" {
		t.Errorf("want data_type []string, got %s", flow.Meta["data_type"])
	}
}
