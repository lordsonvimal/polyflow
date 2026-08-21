package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// writeClosureParamModule mirrors the real-world watchDB/onChange gap: a
// function (watchDB) takes a func()-typed parameter and invokes it from
// inside a goroutine it spawns, rather than from its own body directly. The
// caller (runServe) hands watchDB a closure literal that calls a third
// function (reloadDB). Nothing but the closure-param pass links the
// goroutine's onChange() call to reloadDB — B.1 alone drops the edge because
// the closure literal collapses onto runServe's own node (bug-class rule 6 —
// no hand-built nodes, real SSA pass path is exercised).
func writeClosureParamModule(t *testing.T) (dir string, known map[string]bool) {
	t.Helper()
	dir = t.TempDir()
	src := `package closuretest

func watchDB(onChange func()) {
	go func() {
		onChange()
	}()
}

func runServe() {
	watchDB(func() { reloadDB() })
}

func reloadDB() {}
`
	files := map[string]string{
		"go.mod":  "module example.com/closuretest\n\ngo 1.25.0\n",
		"main.go": src,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Line numbers from the source above (1-indexed):
	//  3: func watchDB(onChange func())
	//  4: go func() {          <- worker (goroutine) node starts here
	//  9: func runServe()
	// 13: func reloadDB()
	known = map[string]bool{
		"svc:main.go:function:watchDB:3":      true,
		"svc:main.go:worker:goroutine_anon:4": true,
		"svc:main.go:function:runServe:9":     true,
		"svc:main.go:function:reloadDB:13":    true,
	}
	return dir, known
}

func analyzeClosureParam(t *testing.T) SemanticResult {
	t.Helper()
	dir, known := writeClosureParamModule(t)
	t.Chdir(dir)
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}
	return res
}

// TestGoClosureParam_GoroutineInvocation verifies that a func()-typed
// parameter invoked from inside a goroutine spawned by its owning function
// links to whatever was passed in at the call site — completing a static
// path through to reloadDB. The closure literal `func(){ reloadDB() }`
// passed at the call site collapses onto its enclosing function (runServe)
// via resolveFunc's name-stripping fallback (it isn't itself a `go`-spawned
// literal, so it gets no distinct worker node), the same way B.1 resolves a
// non-goroutine func-arg — so the closure-param edge correctly lands on
// runServe, not reloadDB directly, and runServe's own instruction walk
// (unrelated to this pass) already supplies the runServe -> reloadDB leg.
func TestGoClosureParam_GoroutineInvocation(t *testing.T) {
	res := analyzeClosureParam(t)

	want := "svc:main.go:worker:goroutine_anon:4->svc:main.go:function:runServe:9"
	found := false
	var runServeToReloadDB bool
	for _, e := range res.Edges {
		if e.From+"->"+e.To == "svc:main.go:function:runServe:9->svc:main.go:function:reloadDB:13" && e.Type == graph.EdgeTypeCalls {
			runServeToReloadDB = true
		}
		if e.Type != graph.EdgeTypeCalls {
			continue
		}
		if e.Meta["via"] != "closure_param" {
			continue
		}
		if e.From+"->"+e.To == want {
			found = true
		}
	}
	if !found {
		t.Errorf("missing closure_param calls edge: %s", want)
	}
	if !runServeToReloadDB {
		t.Errorf("expected runServe -> reloadDB calls edge to complete the path (unrelated to this pass, but required for the goroutine to reach reloadDB statically)")
	}
}

// TestGoClosureParam_Determinism runs the SSA pass twice on the same
// fixture and asserts byte-identical edge sets (bug-class rule 2).
func TestGoClosureParam_Determinism(t *testing.T) {
	dir, known := writeClosureParamModule(t)
	t.Chdir(dir)
	a := &GoSemanticAnalyzer{}
	fset := token.NewFileSet()

	run := func() []string {
		res := a.AnalyzeService(dir, "svc", fset, known)
		var ids []string
		for _, e := range res.Edges {
			if e.Meta["via"] == "closure_param" {
				ids = append(ids, e.ID)
			}
		}
		return ids
	}
	first := run()
	second := run()

	if len(first) == 0 {
		t.Fatal("expected at least one closure_param edge")
	}
	if len(first) != len(second) {
		t.Fatalf("non-deterministic: run1=%d closure_param edges, run2=%d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("edge[%d] differs: %q vs %q", i, first[i], second[i])
		}
	}
}
