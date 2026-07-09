package parser

import (
	"go/token"
	"os"
	"path/filepath"
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
