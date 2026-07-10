package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGoModule lays out a minimal two-file Go module with a cross-file call
// (main → helper) and returns its directory.
func writeGoModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/semtest\n\ngo 1.25.0\n",
		"main.go": `package main

func main() {
	helper()
}
`,
		"util.go": `package main

func helper() {}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestGoSemanticRelativeNodePaths is the regression test for the empty call
// graph bug: the indexer stores workspace-relative file paths in node IDs
// while go/packages reports absolute positions. The analyzer must still
// resolve functions and emit the main → helper edge.
func TestGoSemanticRelativeNodePaths(t *testing.T) {
	dir := writeGoModule(t)
	t.Chdir(dir) // node IDs below are relative to the workspace root, like the indexer's

	known := map[string]bool{
		"svc:main.go:function:main:3":   true,
		"svc:util.go:function:helper:3": true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	found := false
	for _, e := range res.Edges {
		if e.From == "svc:main.go:function:main:3" && e.To == "svc:util.go:function:helper:3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected main → helper call edge, got %d edges: %+v", len(res.Edges), res.Edges)
	}
}

// TestGoSemanticGoroutineWorkerOutflow: calls inside a `go func(){…}` body
// must flow out of the worker node (when one exists at the literal's line),
// and the spawn itself must be a spawns edge deduplicating with the
// tree-sitter pattern edge — not a semantic calls edge.
func TestGoSemanticGoroutineWorkerOutflow(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/semtest\n\ngo 1.25.0\n",
		"main.go": `package main

func main() {
	go func() {
		helper()
	}()
}

func helper() {}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	workerID := "svc:main.go:worker:goroutine_anon:4"
	known := map[string]bool{
		"svc:main.go:function:main:3":   true,
		"svc:main.go:function:helper:9": true,
		workerID:                        true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	var workerCallsHelper, mainSpawnsWorker, mainCallsHelper bool
	for _, e := range res.Edges {
		if e.From == workerID && e.To == "svc:main.go:function:helper:9" && e.Type == "calls" {
			workerCallsHelper = true
		}
		if e.From == "svc:main.go:function:main:3" && e.To == workerID {
			if e.Type != "spawns" {
				t.Fatalf("main → worker edge must be spawns, got %s", e.Type)
			}
			if e.ID != "spawns:svc:main.go:function:main:3->"+workerID {
				t.Fatalf("spawns edge ID must match the pattern edge for dedup, got %s", e.ID)
			}
			mainSpawnsWorker = true
		}
		if e.From == "svc:main.go:function:main:3" && e.To == "svc:main.go:function:helper:9" {
			mainCallsHelper = true
		}
	}
	if !workerCallsHelper {
		t.Fatalf("expected worker → helper calls edge, got: %+v", res.Edges)
	}
	if !mainSpawnsWorker {
		t.Fatalf("expected main → worker spawns edge, got: %+v", res.Edges)
	}
	if mainCallsHelper {
		t.Fatalf("goroutine body call must not attribute to main, got: %+v", res.Edges)
	}
}

// TestGoSemanticClosureFallback: anonymous functions with no worker node at
// their line (plain closures) keep the old behaviour — body calls attribute
// to the parent function via name-stripping.
func TestGoSemanticClosureFallback(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/semtest\n\ngo 1.25.0\n",
		"main.go": `package main

func main() {
	f := func() {
		helper()
	}
	f()
}

func helper() {}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	known := map[string]bool{
		"svc:main.go:function:main:3":    true,
		"svc:main.go:function:helper:10": true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}

	found := false
	for _, e := range res.Edges {
		if e.From == "svc:main.go:function:main:3" && e.To == "svc:main.go:function:helper:10" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected main → helper edge via closure fallback, got: %+v", res.Edges)
	}
}

// TestGoSemanticReferenced: functions referenced without being called must be
// reported for callback classification — a function value stored in a
// package-level composite literal (the cobra RunE shape) and methods
// satisfying an external interface (sort.Interface here; templ's Visitor in
// production). A plain unreferenced function must NOT be reported.
func TestGoSemanticReferenced(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/semtest\n\ngo 1.25.0\n",
		"main.go": `package main

import "sort"

type command struct{ run func() error }

var cmd = command{run: runIndex}

func runIndex() error { return nil }

type byLen []string

func (b byLen) Len() int           { return len(b) }
func (b byLen) Less(i, j int) bool { return len(b[i]) < len(b[j]) }
func (b byLen) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }

func deadCode() {}

func main() {
	_ = cmd
	sort.Sort(byLen{"a"})
}
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)

	known := map[string]bool{
		"svc:main.go:function:main:21":     true,
		"svc:main.go:function:runIndex:9":  true,
		"svc:main.go:function:deadCode:19": true,
		"svc:main.go:method:Len:13":        true,
		"svc:main.go:method:Less:14":       true,
		"svc:main.go:method:Swap:15":       true,
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
	if !ref["svc:main.go:function:runIndex:9"] {
		t.Errorf("runIndex stored in a composite literal must be referenced; got %v", res.Referenced)
	}
	for _, m := range []string{"Len", "Less", "Swap"} {
		found := false
		for id := range ref {
			if strings.Contains(id, ":method:"+m+":") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s satisfies sort.Interface (external) and must be referenced; got %v", m, res.Referenced)
		}
	}
	if ref["svc:main.go:function:deadCode:19"] {
		t.Errorf("deadCode must not be referenced; got %v", res.Referenced)
	}
}

// TestGoSemanticZeroResolutionWarns ensures the analyzer fails loudly instead
// of silently returning an empty edge set when no function matches the node
// index (e.g. a future path-format regression).
func TestGoSemanticZeroResolutionWarns(t *testing.T) {
	dir := writeGoModule(t)

	known := map[string]bool{
		"svc:does/not/exist.go:function:main:1": true,
	}

	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning == "" {
		t.Fatalf("expected zero-resolution warning, got %d edges and no warning", len(res.Edges))
	}
}
