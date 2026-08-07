package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
)

// TestJSB1_SameFileFuncArg verifies that a bare function identifier passed as
// an argument to any call expression produces a calls edge with via=func_arg
// and confidence static (same-file resolution).
func TestJSB1_SameFileFuncArg(t *testing.T) {
	src := []byte(`
function save() {}
function setup() {}

function register(fn, opts) {}

function init() {
  register(save, {once: true});
  setup();
}
`)
	_, edges, _, _ := extractJSVariables("app.js", "web", "javascript", "javascript", src)

	found := false
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.Meta["via"] == "func_arg" &&
			strings.Contains(e.From, ":init:") && strings.Contains(e.To, ":save:") {
			found = true
			if e.Confidence != graph.ConfidenceStatic {
				t.Errorf("same-file func_arg edge must be static, got %q", e.Confidence)
			}
		}
	}
	if !found {
		t.Errorf("missing func_arg calls edge init → save; edges: %+v", edges)
	}
}

// TestJSB1_SameFileFuncArgFanOut verifies that multiple function args to one
// call each get an edge (fan-out, not first-match — bug-class rule 1).
func TestJSB1_SameFileFuncArgFanOut(t *testing.T) {
	src := []byte(`
function alpha() {}
function beta() {}
function multi(a, b) {}

function run() {
  multi(alpha, beta);
}
`)
	_, edges, _, _ := extractJSVariables("app.js", "web", "javascript", "javascript", src)

	var funcArgEdges []graph.Edge
	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.Meta["via"] == "func_arg" &&
			strings.Contains(e.From, ":run:") {
			funcArgEdges = append(funcArgEdges, e)
		}
	}
	if len(funcArgEdges) < 2 {
		t.Errorf("expected ≥2 func_arg edges from run (alpha + beta), got %d; edges: %+v",
			len(funcArgEdges), edges)
	}
}

// TestJSB1_SameFileDeterminism runs the JS parser twice and asserts byte-identical
// func_arg edges (bug-class rule 2).
func TestJSB1_SameFileDeterminism(t *testing.T) {
	src := []byte(`
function alpha() {}
function beta() {}
function run() { someReg(alpha, beta); }
function someReg(a, b) {}
`)
	collect := func() []string {
		_, edges, _, _ := extractJSVariables("app.js", "web", "javascript", "javascript", src)
		var ids []string
		for _, e := range edges {
			if e.Meta["via"] == "func_arg" {
				ids = append(ids, e.ID)
			}
		}
		return ids
	}
	first, second := collect(), collect()
	if len(first) != len(second) {
		t.Fatalf("non-deterministic: run1=%d func_arg edges, run2=%d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("edge[%d] differs: %q vs %q", i, first[i], second[i])
		}
	}
}

// TestJSB1_NonFunctionArgNoEdge verifies that a string literal or variable that
// happens to share a function's name does NOT produce a func_arg edge.
func TestJSB1_NonFunctionArgNoEdge(t *testing.T) {
	src := []byte(`
function handler() {}
var handler2 = 42;

function dispatch(name) {}
function run() {
  dispatch("handler");   // string — no edge
  dispatch(handler2);    // not a function — no edge
}
`)
	_, edges, _, _ := extractJSVariables("app.js", "web", "javascript", "javascript", src)

	for _, e := range edges {
		if e.Meta["via"] == "func_arg" && strings.Contains(e.To, ":handler:") {
			t.Errorf("unexpected func_arg edge to handler from string arg: %s", e.ID)
		}
	}
}

// TestJSB1_JSXNoDuplicateEdge verifies that JSX event props (onClick={fn})
// and call-expression func-arg refs don't both fire for the same reference
// (no double edges — spec clause 4). Uses a TSX fixture.
func TestJSB1_JSXNoDuplicateEdge(t *testing.T) {
	// JSX onclick prop: handled by jsxEventPropQuery in the linker, not here.
	// In the same-file extractor, JSX attributes are not call_expressions, so
	// no func_arg edge is emitted. This test confirms zero func_arg edges for
	// a file that only references the function via JSX.
	src := []byte(`
function save() {}
function Form() {
  return <button onClick={save}>Save</button>;
}
`)
	_, edges, _, _ := extractJSVariables("form.tsx", "web", "tsx", "tsx", src)

	for _, e := range edges {
		if e.Type == graph.EdgeTypeCalls && e.Meta["via"] == "func_arg" &&
			strings.Contains(e.To, ":save:") {
			t.Errorf("unexpected func_arg edge for JSX event prop (should be handled by linker): %s", e.ID)
		}
	}
}

// TestJSB1_CrossFileFuncArg verifies that a function passed as an argument to
// a call expression, where the function is imported from another file, produces
// a calls edge with via=func_arg and confidence inferred (cross-file).
func TestJSB1_CrossFileFuncArg(t *testing.T) {
	dir := t.TempDir()

	// lib.js exports a save function
	libSrc := `export function save() { return true; }`
	// main.js imports save and passes it as an argument to register
	mainSrc := `import { save } from "./lib.js";
function register(fn) {}
function init() {
  register(save);
}`

	for name, content := range map[string]string{
		"lib.js":  libSrc,
		"main.js": mainSrc,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Parse both files to get nodes.
	libFile := filepath.Join(dir, "lib.js")
	mainFile := filepath.Join(dir, "main.js")

	libSrcB, _ := os.ReadFile(libFile)
	mainSrcB, _ := os.ReadFile(mainFile)

	libNodes, libEdges, _, _ := extractJSVariables(libFile, "web", "javascript", "javascript", libSrcB)
	mainNodes, mainEdges, _, _ := extractJSVariables(mainFile, "web", "javascript", "javascript", mainSrcB)

	allNodes := append(libNodes, mainNodes...)
	allEdges := append(libEdges, mainEdges...)

	serviceFiles := map[string][]string{"web": {libFile, mainFile}}
	l := linker.NewJSLinker()
	newEdges, _, _, _ := l.LinkJS(allNodes, allEdges, serviceFiles)

	// Look for a calls edge with via=func_arg from init to save.
	found := false
	for _, e := range newEdges {
		if e.Type == graph.EdgeTypeCalls && e.Meta["via"] == "func_arg" &&
			strings.Contains(e.From, ":init:") && strings.Contains(e.To, ":save:") {
			found = true
			if e.Confidence != graph.ConfidenceInferred {
				t.Errorf("cross-file func_arg edge must be inferred, got %q", e.Confidence)
			}
		}
	}
	if !found {
		t.Errorf("missing cross-file func_arg edge init → save; linker edges: %+v", newEdges)
	}
}

// TestJSB1_CrossFileDeterminism runs the linker twice on the same two-file
// fixture and asserts byte-identical func_arg edge IDs (bug-class rule 2).
func TestJSB1_CrossFileDeterminism(t *testing.T) {
	dir := t.TempDir()
	libSrc := `export function alpha() {}`
	mainSrc := `import { alpha } from "./lib.js";
function reg(fn) {}
function run() { reg(alpha); }`

	for name, content := range map[string]string{
		"lib.js":  libSrc,
		"main.js": mainSrc,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	libFile := filepath.Join(dir, "lib.js")
	mainFile := filepath.Join(dir, "main.js")
	libSrcB, _ := os.ReadFile(libFile)
	mainSrcB, _ := os.ReadFile(mainFile)
	libNodes, libEdges, _, _ := extractJSVariables(libFile, "web", "javascript", "javascript", libSrcB)
	mainNodes, mainEdges, _, _ := extractJSVariables(mainFile, "web", "javascript", "javascript", mainSrcB)
	allNodes := append(libNodes, mainNodes...)
	allEdges := append(libEdges, mainEdges...)
	serviceFiles := map[string][]string{"web": {libFile, mainFile}}

	collect := func() []string {
		l := linker.NewJSLinker()
		newEdges, _, _, _ := l.LinkJS(allNodes, allEdges, serviceFiles)
		var ids []string
		for _, e := range newEdges {
			if e.Meta["via"] == "func_arg" {
				ids = append(ids, e.ID)
			}
		}
		return ids
	}
	first, second := collect(), collect()
	if len(first) != len(second) {
		t.Fatalf("non-deterministic: run1=%d func_arg edges, run2=%d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("edge[%d] differs: %q vs %q", i, first[i], second[i])
		}
	}
}
