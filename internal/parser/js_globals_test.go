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
	n, e, u, _ := extractJSVariables(file, "svc", "javascript", "javascript", []byte(src), nil)
	return n, e, u
}

// TestStampGlobalSymbols_NonModuleFunctionDecl: top-level function declarations
// in a non-module script (no import/export) get global_symbol stamped.
func TestStampGlobalSymbols_NonModuleFunctionDecl(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestStampGlobalSymbols_NestedWindowAssign: window.maple.save = function(){}
// mints a function node labelled save with global_path=window.maple.save.
func TestStampGlobalSymbols_NestedWindowAssign(t *testing.T) {
	t.Parallel()
	src := `window.maple = window.maple || {};
window.maple.save = function() { return 1; }
`
	nodes, _, _ := parseJS(t, src)

	var found *graph.Node
	for i := range nodes {
		if nodes[i].Meta["global_path"] == "window.maple.save" {
			found = &nodes[i]
			break
		}
	}
	require.NotNil(t, found, "node with global_path=window.maple.save must exist")
	assert.Equal(t, "save", found.Label)
	assert.Equal(t, "save", found.Meta["global_symbol"])
	assert.Equal(t, graph.NodeTypeFunction, found.Type)
}

// TestStampGlobalSymbols_WrappedInIIFE: a namespaced registration inside an
// IIFE still resolves via the recursive assignment walk.
func TestStampGlobalSymbols_WrappedInIIFE(t *testing.T) {
	t.Parallel()
	src := `(function () {
  window.maple.closeVulnerabilityModal = function () { return 1; };
})();
`
	nodes, _, _ := parseJS(t, src)

	var found *graph.Node
	for i := range nodes {
		if nodes[i].Meta["global_path"] == "window.maple.closeVulnerabilityModal" {
			found = &nodes[i]
			break
		}
	}
	require.NotNil(t, found, "IIFE-wrapped namespaced global must be minted")
	assert.Equal(t, "closeVulnerabilityModal", found.Label)
	assert.Equal(t, "closeVulnerabilityModal", found.Meta["global_symbol"])
	assert.Equal(t, graph.NodeTypeFunction, found.Type)
}

// TestStampGlobalSymbols_WindowAssignClass: window.X = X, where X is a class
// already declared in this file, stamps global_symbol/global_path onto the
// CLASS node itself rather than minting a phantom variable node — the DC.15
// self-registration shape (orion's pusher_client.es6:
// `window.PusherClient = PusherClient`) a cross-file `new window.X(...)`
// resolver depends on to find the real constructor.
func TestStampGlobalSymbols_WindowAssignClass(t *testing.T) {
	t.Parallel()
	src := `class PusherClient {
  constructor() {}
}
window.PusherClient = PusherClient;
`
	nodes, _, _ := parseJS(t, src)

	cls := jsNodeI2(nodes, graph.NodeTypeClass, "PusherClient")
	require.NotNil(t, cls, "PusherClient class node must exist")
	assert.Equal(t, "PusherClient", cls.Meta["global_symbol"])
	assert.Equal(t, "window.PusherClient", cls.Meta["global_path"])

	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeVariable && nodes[i].Label == "PusherClient" {
			t.Errorf("window.X = X (class) must not also mint a phantom variable node; got %+v", nodes[i])
		}
	}
}

// TestStampGlobalSymbols_Negative_NonWindow: assignment to non-window object
// does NOT produce a global_symbol node.
func TestStampGlobalSymbols_Negative_NonWindow(t *testing.T) {
	t.Parallel()
	src := `document.title = "hello";
`
	nodes, _, _ := parseJS(t, src)

	for i := range nodes {
		assert.Empty(t, nodes[i].Meta["global_symbol"],
			"non-window assignment must not produce global_symbol")
	}
}
