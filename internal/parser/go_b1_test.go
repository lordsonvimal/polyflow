package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// writeFuncArgModule writes a two-function fixture where routes() passes
// viewLogin and processQueue as function-value arguments (the B.1 worked
// example). The real SSA pass path is exercised (rule 6 — no hand-built nodes).
func writeFuncArgModule(t *testing.T) (dir string, known map[string]bool) {
	t.Helper()
	dir = t.TempDir()
	// Source mirrors the spec's worked example, adapted so webHandler is in-service
	// (avoids the interface-wrapping that would hide *ssa.Function in MakeInterface).
	src := `package funcargtest

import "net/http"

func routes() {
	webHandler(viewLogin, 0)
	go worker(processQueue)
}

func webHandler(fn func(http.ResponseWriter, *http.Request), level int) {}
func worker(fn func()) { fn() }
func viewLogin(w http.ResponseWriter, r *http.Request) {}
func processQueue() {}
`
	files := map[string]string{
		"go.mod":    "module example.com/funcargtest\n\ngo 1.25.0\n",
		"routes.go": src,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Line numbers from the source above (1-indexed):
	//  5: func routes()
	//  9: func webHandler(...)
	// 10: func worker(...)
	// 11: func viewLogin(...)
	// 12: func processQueue()
	known = map[string]bool{
		"svc:routes.go:function:routes:5":       true,
		"svc:routes.go:function:webHandler:9":   true,
		"svc:routes.go:function:worker:10":      true,
		"svc:routes.go:function:viewLogin:11":   true,
		"svc:routes.go:function:processQueue:12": true,
	}
	return dir, known
}

func analyzeFuncArg(t *testing.T) SemanticResult {
	t.Helper()
	dir, known := writeFuncArgModule(t)
	t.Chdir(dir)
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}
	return res
}

// TestGoB1_FuncArgEdges verifies that function values passed as call arguments
// produce calls edges with via=func_arg meta. Runs through the real SSA pass
// (rule 6 — no hand-built nodes).
func TestGoB1_FuncArgEdges(t *testing.T) {
	res := analyzeFuncArg(t)

	wantEdges := map[string]bool{
		"svc:routes.go:function:routes:5->svc:routes.go:function:viewLogin:11":    false,
		"svc:routes.go:function:routes:5->svc:routes.go:function:processQueue:12": false,
	}
	for _, e := range res.Edges {
		if e.Type != graph.EdgeTypeCalls {
			continue
		}
		if e.Meta["via"] != "func_arg" {
			continue
		}
		key := e.From + "->" + e.To
		if _, want := wantEdges[key]; want {
			wantEdges[key] = true
		}
	}
	for key, found := range wantEdges {
		if !found {
			t.Errorf("missing func_arg calls edge: %s", key)
		}
	}
}

// TestGoB1_FuncArgFanOut verifies that when two function values are passed to the
// same call, both get edges (fan-out, not first-match — bug-class rule 1).
func TestGoB1_FuncArgFanOut(t *testing.T) {
	res := analyzeFuncArg(t)

	fromRoutes := 0
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeCalls && e.Meta["via"] == "func_arg" &&
			strings.Contains(e.From, ":routes:") {
			fromRoutes++
		}
	}
	// routes passes viewLogin (via webHandler) and processQueue (via worker) — both must appear.
	if fromRoutes < 2 {
		t.Errorf("expected ≥2 func_arg edges from routes, got %d", fromRoutes)
	}
}

// TestGoB1_FuncArgDeterminism runs the SSA pass twice on the same fixture
// and asserts byte-identical edge sets (bug-class rule 2).
func TestGoB1_FuncArgDeterminism(t *testing.T) {
	dir, known := writeFuncArgModule(t)
	t.Chdir(dir)
	a := &GoSemanticAnalyzer{}
	fset := token.NewFileSet()

	run := func() []string {
		res := a.AnalyzeService(dir, "svc", fset, known)
		var ids []string
		for _, e := range res.Edges {
			if e.Meta["via"] == "func_arg" {
				ids = append(ids, e.ID)
			}
		}
		return ids
	}
	first := run()
	second := run()

	if len(first) != len(second) {
		t.Fatalf("non-deterministic: run1=%d func_arg edges, run2=%d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("edge[%d] differs: %q vs %q", i, first[i], second[i])
		}
	}
}

// TestGoB1_StringArgNoEdge verifies that a string argument whose value matches
// a function name does not produce a func_arg edge (rule 6 — no false positives
// from coincidental name matches).
func TestGoB1_StringArgNoEdge(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	src := `package strtest

func dispatch(name string) {}
func handler() {}

func run() {
	dispatch("handler") // string literal — must NOT produce a func_arg edge to handler
}
`
	files := map[string]string{
		"go.mod":  "module example.com/strtest\n\ngo 1.25.0\n",
		"main.go": src,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	known := map[string]bool{
		"svc:main.go:function:dispatch:3": true,
		"svc:main.go:function:handler:4":  true,
		"svc:main.go:function:run:6":      true,
	}
	t.Chdir(dir)
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	for _, e := range res.Edges {
		if e.Meta["via"] == "func_arg" && strings.Contains(e.To, ":handler:") {
			t.Errorf("unexpected func_arg edge to handler from string literal arg: %s", e.ID)
		}
	}
}

// TestGoB1_ReferencedUnchanged verifies that the Referenced population is
// unchanged by the B.1 lift — callback classification depends on it.
func TestGoB1_ReferencedUnchanged(t *testing.T) {
	res := analyzeFuncArg(t)
	// In the fixture, viewLogin and processQueue are passed as arguments — they
	// should appear in Referenced (existing behavior, collected by collectReferenced).
	refSet := make(map[string]bool, len(res.Referenced))
	for _, id := range res.Referenced {
		refSet[id] = true
	}
	for _, wantSuffix := range []string{":viewLogin:", ":processQueue:"} {
		found := false
		for id := range refSet {
			if strings.Contains(id, wantSuffix) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Referenced missing function with suffix %q (callback classification broken)", wantSuffix)
		}
	}
}
