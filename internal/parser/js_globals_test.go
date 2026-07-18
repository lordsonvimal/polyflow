package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func parseJS(t *testing.T, src string) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "app.js")
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))
	return extractJSVariables(file, "svc", "javascript", "javascript", []byte(src))
}

// TestStampGlobalSymbols_NonModuleFunctionDecl: top-level function declarations
// in a non-module script (no import/export) get global_symbol stamped.
func TestStampGlobalSymbols_NonModuleFunctionDecl(t *testing.T) {
	src := `function save() { return 1; }
function load() { return 2; }
`
	nodes, _, _ := parseJS(t, src)

	var saveFn, loadFn *graph.Node
	for i := range nodes {
		switch nodes[i].Label {
		case "save":
			saveFn = &nodes[i]
		case "load":
			loadFn = &nodes[i]
		}
	}
	require.NotNil(t, saveFn, "save function node must exist")
	require.NotNil(t, loadFn, "load function node must exist")
	assert.Equal(t, "save", saveFn.Meta["global_symbol"])
	assert.Equal(t, "load", loadFn.Meta["global_symbol"])
}

// TestStampGlobalSymbols_ModuleFilesExcluded: top-level functions in a module
// (has import/export) do NOT get global_symbol.
func TestStampGlobalSymbols_ModuleFilesExcluded(t *testing.T) {
	src := `import { x } from "./x";
function save() { return 1; }
`
	nodes, _, _ := parseJS(t, src)

	for i := range nodes {
		if nodes[i].Label == "save" {
			assert.Empty(t, nodes[i].Meta["global_symbol"],
				"module-file function must not get global_symbol")
		}
	}
}

// TestStampGlobalSymbols_WindowAssignFunction: window.save = function() {}
// creates a function node with global_symbol=save.
func TestStampGlobalSymbols_WindowAssignFunction(t *testing.T) {
	src := `window.save = function() { return 1; }
`
	nodes, _, _ := parseJS(t, src)

	var found *graph.Node
	for i := range nodes {
		if nodes[i].Meta["global_symbol"] == "save" {
			found = &nodes[i]
			break
		}
	}
	require.NotNil(t, found, "node with global_symbol=save must exist")
	assert.Equal(t, "save", found.Label)
	assert.Equal(t, graph.NodeTypeFunction, found.Type)
}

// TestStampGlobalSymbols_WindowAssignObject: window.App = {...} creates a
// variable node with global_symbol=App.
func TestStampGlobalSymbols_WindowAssignObject(t *testing.T) {
	src := `window.App = { submit: function() {} }
`
	nodes, _, _ := parseJS(t, src)

	var found *graph.Node
	for i := range nodes {
		if nodes[i].Meta["global_symbol"] == "App" {
			found = &nodes[i]
			break
		}
	}
	require.NotNil(t, found, "node with global_symbol=App must exist")
	assert.Equal(t, "App", found.Label)
	assert.Equal(t, graph.NodeTypeVariable, found.Type)
}

// TestStampGlobalSymbols_Negative_NonWindow: assignment to non-window object
// does NOT produce a global_symbol node.
func TestStampGlobalSymbols_Negative_NonWindow(t *testing.T) {
	src := `document.title = "hello";
`
	nodes, _, _ := parseJS(t, src)

	for i := range nodes {
		assert.Empty(t, nodes[i].Meta["global_symbol"],
			"non-window assignment must not produce global_symbol")
	}
}
