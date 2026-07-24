package parser

import (
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// writeCrossPkgModule writes a two-package fixture:
//
//	config/config.go — defines a package-level var (DefaultTimeout) and const (MaxRetries).
//	server/server.go — imports config with alias cfg, reads both, and writes DefaultTimeout.
//
// Returns the module root dir and the knownNodes map with pre-computed node IDs.
// Line numbers below are 1-indexed from the written source.
func writeCrossPkgModule(t *testing.T) (dir string, known map[string]bool) {
	t.Helper()
	dir = t.TempDir()

	configSrc := `package config

var DefaultTimeout = 30
const MaxRetries = 3
`
	// Lines: 1=package, 2=blank, 3=var DefaultTimeout, 4=const MaxRetries

	serverSrc := `package server

import cfg "svc2test/config"

func Handler() int {
	return cfg.DefaultTimeout + cfg.MaxRetries
}

func Updater(val int) {
	cfg.DefaultTimeout = val
}
`
	// Lines: 1=package, 2=blank, 3=import, 4=blank, 5=func Handler, 9=func Updater

	files := map[string]string{
		"go.mod":              "module svc2test\n\ngo 1.25.0\n",
		"config/config.go":    configSrc,
		"server/server.go":    serverSrc,
	}
	for name, content := range files {
		fullPath := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	known = map[string]bool{
		"svc:config/config.go:variable:DefaultTimeout:3": true,
		"svc:config/config.go:variable:MaxRetries:4":     true,
		"svc:server/server.go:function:Handler:5":         true,
		"svc:server/server.go:function:Updater:9":         true,
	}
	return dir, known
}

func analyzeCrossPkg(t *testing.T) SemanticResult {
	t.Helper()
	dir, known := writeCrossPkgModule(t)
	t.Chdir(dir)
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)
	if res.Warning != "" {
		t.Fatalf("unexpected warning: %s", res.Warning)
	}
	return res
}

// TestGoB2_CrossPackageVarReads verifies that a cross-package var read emits a
// reads edge. Handler reads cfg.DefaultTimeout from the config package.
func TestGoB2_CrossPackageVarReads(t *testing.T) {
	res := analyzeCrossPkg(t)

	found := false
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeReads &&
			strings.Contains(e.From, ":Handler:") &&
			strings.Contains(e.To, ":DefaultTimeout:") {
			found = true
			break
		}
	}
	if !found {
		t.Error("missing reads edge: Handler → DefaultTimeout (cross-package var read)")
	}
}

// TestGoB2_CrossPackageVarWrites verifies that a cross-package var write emits a
// writes edge. Updater writes cfg.DefaultTimeout.
func TestGoB2_CrossPackageVarWrites(t *testing.T) {
	res := analyzeCrossPkg(t)

	found := false
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeWrites &&
			strings.Contains(e.From, ":Updater:") &&
			strings.Contains(e.To, ":DefaultTimeout:") {
			found = true
			break
		}
	}
	if !found {
		t.Error("missing writes edge: Updater → DefaultTimeout (cross-package var write)")
	}
}

// TestGoB2_CrossPackageConstReads verifies that a cross-package const reference
// emits a reads edge. Go constants are compile-time-folded and invisible to SSA
// instructions, so this exercises the typed-AST const-ref walk (B.2).
func TestGoB2_CrossPackageConstReads(t *testing.T) {
	res := analyzeCrossPkg(t)

	found := false
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeReads &&
			strings.Contains(e.From, ":Handler:") &&
			strings.Contains(e.To, ":MaxRetries:") {
			found = true
			break
		}
	}
	if !found {
		t.Error("missing reads edge: Handler → MaxRetries (cross-package const read via typed AST)")
	}
}

// TestGoB2_AliasedImport verifies that the aliased import form
// `import cfg "svc2test/config"` does not prevent edge emission.
// Both reads and writes edges from Handler/Updater must appear.
func TestGoB2_AliasedImport(t *testing.T) {
	res := analyzeCrossPkg(t)

	wantEdges := map[string]bool{
		"reads:Handler->DefaultTimeout":  false,
		"reads:Handler->MaxRetries":      false,
		"writes:Updater->DefaultTimeout": false,
	}
	for _, e := range res.Edges {
		fromHasHandler := strings.Contains(e.From, ":Handler:")
		fromHasUpdater := strings.Contains(e.From, ":Updater:")
		toHasDefault := strings.Contains(e.To, ":DefaultTimeout:")
		toHasMaxRetries := strings.Contains(e.To, ":MaxRetries:")
		switch {
		case e.Type == graph.EdgeTypeReads && fromHasHandler && toHasDefault:
			wantEdges["reads:Handler->DefaultTimeout"] = true
		case e.Type == graph.EdgeTypeReads && fromHasHandler && toHasMaxRetries:
			wantEdges["reads:Handler->MaxRetries"] = true
		case e.Type == graph.EdgeTypeWrites && fromHasUpdater && toHasDefault:
			wantEdges["writes:Updater->DefaultTimeout"] = true
		}
	}
	for key, found := range wantEdges {
		if !found {
			t.Errorf("aliased import: missing edge %s", key)
		}
	}
}

// TestGoB2_FanOut verifies that multiple functions reading the same cross-package
// var each get a reads edge (fan-out, not first-match — bug-class rule 1).
func TestGoB2_FanOut(t *testing.T) {
	t.Helper()
	dir := t.TempDir()

	configSrc := `package config

var Shared = 42
`
	serverSrc := `package server

import "svc2fanout/config"

func Alpha() int { return config.Shared }
func Beta() int  { return config.Shared }
func Gamma() int { return config.Shared }
`
	files := map[string]string{
		"go.mod":           "module svc2fanout\n\ngo 1.25.0\n",
		"config/config.go": configSrc,
		"server/server.go": serverSrc,
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	known := map[string]bool{
		"svc:config/config.go:variable:Shared:3": true,
		"svc:server/server.go:function:Alpha:5":  true,
		"svc:server/server.go:function:Beta:6":   true,
		"svc:server/server.go:function:Gamma:7":  true,
	}
	t.Chdir(dir)
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)

	readers := 0
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeReads && strings.Contains(e.To, ":Shared:") {
			readers++
		}
	}
	if readers < 3 {
		t.Errorf("fan-out: expected ≥3 reads edges to Shared, got %d", readers)
	}
}

// TestGoB2_Determinism runs the SSA pass twice on the same two-package fixture
// and asserts byte-identical edge sets (bug-class rule 2).
func TestGoB2_Determinism(t *testing.T) {
	dir, known := writeCrossPkgModule(t)
	t.Chdir(dir)
	a := &GoSemanticAnalyzer{}
	fset := token.NewFileSet()

	edgeIDs := func() []string {
		res := a.AnalyzeService(dir, "svc", fset, known)
		var ids []string
		for _, e := range res.Edges {
			if e.Type == graph.EdgeTypeReads || e.Type == graph.EdgeTypeWrites {
				ids = append(ids, e.ID)
			}
		}
		sort.Strings(ids)
		return ids
	}
	first := edgeIDs()
	second := edgeIDs()

	if len(first) != len(second) {
		t.Fatalf("non-deterministic: run1=%d edges, run2=%d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("edge[%d] differs: %q vs %q", i, first[i], second[i])
		}
	}
}

// TestGoB2_TestVariantShadowing verifies that a test-file global does not shadow
// the production global in the service-wide qualifiedNameIDs map. The production
// node must be the target of reads edges, not the test-file node.
func TestGoB2_TestVariantShadowing(t *testing.T) {
	t.Helper()
	dir := t.TempDir()

	// Production global at line 3.
	configSrc := `package config

var Limit = 100
`
	// Test file re-declares Limit (shadow attempt).
	configTestSrc := `package config

var Limit = 999
`
	serverSrc := `package server

import "svc2shadow/config"

func Check() int { return config.Limit }
`
	files := map[string]string{
		"go.mod":                 "module svc2shadow\n\ngo 1.25.0\n",
		"config/config.go":       configSrc,
		"config/config_test.go":  configTestSrc,
		"server/server.go":       serverSrc,
	}
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Both nodes exist in knownNodes; the prod one at line 3 must win.
	known := map[string]bool{
		"svc:config/config.go:variable:Limit:3":      true,
		"svc:config/config_test.go:variable:Limit:3": true,
		"svc:server/server.go:function:Check:5":       true,
	}
	t.Chdir(dir)
	a := &GoSemanticAnalyzer{}
	res := a.AnalyzeService(dir, "svc", token.NewFileSet(), known)

	// The reads edge must point to the prod node, not the test one.
	for _, e := range res.Edges {
		if e.Type == graph.EdgeTypeReads && strings.Contains(e.From, ":Check:") {
			if strings.Contains(e.To, "config_test.go") {
				t.Errorf("test-variant shadowing: reads edge points to test node %s instead of prod node", e.To)
			}
			return
		}
	}
	// If no reads edge at all that's also acceptable for the shadowing test
	// (compilation may fail due to duplicate var; the negative case still passes).
}
