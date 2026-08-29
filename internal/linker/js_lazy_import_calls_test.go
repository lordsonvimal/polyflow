package linker

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// TestLinkJSLazyImportCalls_ResolvesModuleLevelRegistration is gitnexus's
// confirmed live shape: every CLI subcommand is registered via
// `.action(createLazyAction(() => import('./serve.js'), 'serveCommand'))` at
// module top level (no enclosing function) — the dynamic import awaits a
// module object, then indexes it by the string literal at runtime, a shape
// no static call resolution can see without recognizing the pattern
// explicitly. The call site here has no enclosing function, so attribution
// must fall back to the importing file's own NodeTypeFile node.
func TestLinkJSLazyImportCalls_ResolvesModuleLevelRegistration(t *testing.T) {
	t.Parallel()
	dir, paths := writeJSFixture(t, map[string]string{
		"serve.ts": "export const serveCommand = async (opts) => {\n" +
			"  console.log(opts);\n" +
			"};\n",
		"index.ts": "import { createLazyAction } from './lazy-action.js';\n" +
			"\n" +
			"program\n" +
			"  .command('serve')\n" +
			"  .action(createLazyAction(() => import('./serve.js'), 'serveCommand'));\n",
	})
	_ = dir
	var serve, index string
	for _, p := range paths {
		switch filepath.Base(p) {
		case "serve.ts":
			serve = p
		case "index.ts":
			index = p
		}
	}

	fileNode := graph.Node{
		ID:      fmt.Sprintf("svc:%s:file", index),
		Type:    graph.NodeTypeFile,
		Service: "svc",
		File:    index,
	}
	serveFn := jsFuncNode("svc", serve, "serveCommand", 1)

	nodes := []graph.Node{fileNode, serveFn}
	edges, unresolved := LinkJSLazyImportCalls(nodes, map[string][]string{
		"svc": {serve, index},
	})

	wantID := fmt.Sprintf("calls:%s->%s", fileNode.ID, serveFn.ID)
	var got bool
	for _, e := range edges {
		if e.ID == wantID {
			got = true
			if e.Meta["via"] != "lazy_import_export" {
				t.Errorf("edge meta via = %q, want lazy_import_export", e.Meta["via"])
			}
		}
	}
	if !got {
		t.Errorf("missing lazy-import calls edge %s; got %+v, unresolved %+v", wantID, edges, unresolved)
	}
}

// TestLinkJSLazyImportCalls_UnknownExportIsLedgered proves a specifier that
// resolves to a real file, but names an export that file does not declare,
// is ledgered rather than silently dropped or fabricated.
func TestLinkJSLazyImportCalls_UnknownExportIsLedgered(t *testing.T) {
	t.Parallel()
	dir, paths := writeJSFixture(t, map[string]string{
		"serve.ts": "export const serveCommand = async () => {};\n",
		"index.ts": "program.action(createLazyAction(() => import('./serve.js'), 'nope'));\n",
	})
	_ = dir
	var serve, index string
	for _, p := range paths {
		switch filepath.Base(p) {
		case "serve.ts":
			serve = p
		case "index.ts":
			index = p
		}
	}

	fileNode := graph.Node{ID: fmt.Sprintf("svc:%s:file", index), Type: graph.NodeTypeFile, Service: "svc", File: index}
	serveFn := jsFuncNode("svc", serve, "serveCommand", 1)

	edges, unresolved := LinkJSLazyImportCalls([]graph.Node{fileNode, serveFn}, map[string][]string{
		"svc": {serve, index},
	})
	if len(edges) != 0 {
		t.Errorf("expected no edges for an unknown export name; got %+v", edges)
	}
	var found bool
	for _, u := range unresolved {
		if u.Kind == "lazy_import_export_unresolved" && u.Name == "nope" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'nope' ledgered as lazy_import_export_unresolved; got %+v", unresolved)
	}
}

// TestLinkJSLazyImportCalls_OrdinaryCallNeverMatches guards against false
// positives: a plain call with an arrow function argument and an unrelated
// string argument (no dynamic import inside the arrow) must never be
// mistaken for this shape.
func TestLinkJSLazyImportCalls_OrdinaryCallNeverMatches(t *testing.T) {
	t.Parallel()
	dir, paths := writeJSFixture(t, map[string]string{
		"index.ts": "registerHook(() => doSomething(), 'not-an-export');\n",
	})
	_ = dir
	edges, unresolved := LinkJSLazyImportCalls(nil, map[string][]string{
		"svc": paths,
	})
	if len(edges) != 0 || len(unresolved) != 0 {
		t.Errorf("ordinary call must never match the lazy-import shape; edges=%+v unresolved=%+v", edges, unresolved)
	}
}
