package pipeline_test

// FX.8 (2026-09-15): js_lazy_import_calls — internal/linker/
// js_lazy_import_calls.go's retired LinkJSLazyImportCalls, migrated onto
// patterns/javascript/js_lazy_import_calls.yaml + rules/javascript/
// js_lazy_import_calls.dl. Real-parse tests (temp-dir fixture files, gitnexus's
// confirmed live shape), porting the retired Go test's fixtures verbatim.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/factpipe/pipeline"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/parser"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

func licActive(t *testing.T) []*pipeline.Framework {
	t.Helper()
	reg, err := pipeline.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	fw := reg.ByName("js_lazy_import_calls")
	if fw == nil {
		t.Fatal("js_lazy_import_calls framework not embedded")
	}
	return []*pipeline.Framework{fw}
}

// licFixtureNodes writes each fixture file to a real temp directory (so
// go-tree-sitter's parser has something to read) but relabels every node's
// File — and the ParsedFile.Path fed to extraction — back to the repo-style
// relative path (rel). graph.Node.File and Snapshot.Files are always
// repo-relative in production (never an OS-absolute filesystem path); the
// resolve_path primitive's candidate matching (internal/factpipe/resolve.go)
// depends on that convention (its leading-"/" trim assumes File values carry
// no meaningful leading slash), so a test using the raw absolute temp path
// verbatim as File would silently fail to resolve anything.
func licFixtureNodes(t *testing.T, files map[string]string) ([]graph.Node, []pipeline.ParsedFile, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	reg, err := patterns.EmbeddedRegistry()
	if err != nil {
		t.Fatalf("EmbeddedRegistry: %v", err)
	}
	m := patterns.NewTreeSitterMatcher(reg)

	var nodes []graph.Node
	var parsed []pipeline.ParsedFile
	paths := make(map[string]string, len(files))
	for rel, src := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
		p := parser.ForFile(abs)
		if p == nil {
			t.Fatalf("no parser for %s", abs)
		}
		ns, _, _, err := p.Parse(abs, "svc", m, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", abs, err)
		}
		for i := range ns {
			ns[i].ID = relabelID(ns[i].ID, abs, rel)
			ns[i].File = rel
		}
		nodes = append(nodes, ns...)
		nodes = append(nodes, graph.Node{
			ID: fmt.Sprintf("svc:%s:file", rel), Type: graph.NodeTypeFile,
			Service: "svc", File: rel,
		})
		parsed = append(parsed, pipeline.ParsedFile{Path: rel, Language: "javascript", Grammar: "typescript", Src: []byte(src)})
		paths[rel] = rel
	}
	return nodes, parsed, paths
}

// relabelID swaps the absolute-path segment a node ID embeds (fnNodeID's
// "<svc>:<file>:<type>:<name>:<line>" convention) for the repo-relative path,
// mirroring the File relabel above.
func relabelID(id, abs, rel string) string {
	return strings.Replace(id, abs, rel, 1)
}

// TestJSLazyImportCallsRule_ResolvesModuleLevelRegistration is gitnexus's
// confirmed live shape: every CLI subcommand is registered via
// `.action(createLazyAction(() => import('./serve.js'), 'serveCommand'))` at
// module top level (no enclosing function) — the call site here has no
// enclosing declaration, so attribution must fall back to the importing
// file's own NodeTypeFile node.
func TestJSLazyImportCallsRule_ResolvesModuleLevelRegistration(t *testing.T) {
	nodes, parsed, paths := licFixtureNodes(t, map[string]string{
		"serve.ts": "export const serveCommand = async (opts) => {\n" +
			"  console.log(opts);\n" +
			"};\n",
		"index.ts": "import { createLazyAction } from './lazy-action.js';\n" +
			"\n" +
			"program\n" +
			"  .command('serve')\n" +
			"  .action(createLazyAction(() => import('./serve.js'), 'serveCommand'));\n",
	})
	index, serve := paths["index.ts"], paths["serve.ts"]

	res, err := pipeline.Run(licActive(t), parsed, graph.Snapshot{
		Nodes: nodes, Files: []string{index, serve},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	fileID := fmt.Sprintf("svc:%s:file", index)
	serveFnID := fmt.Sprintf("svc:%s:function:serveCommand:1", serve)

	var got bool
	for _, e := range res.Edges {
		if e.From == fileID && e.To == serveFnID {
			got = true
			if e.Meta["via"] != "lazy_import_export" {
				t.Errorf("edge meta via = %q, want lazy_import_export", e.Meta["via"])
			}
		}
	}
	if !got {
		t.Errorf("missing lazy-import calls edge %s->%s; got %+v, unresolved %+v", fileID, serveFnID, res.Edges, res.Unresolved)
	}
}

// TestJSLazyImportCallsRule_UnknownExportIsLedgered proves a specifier that
// resolves to a real file, but names an export that file does not declare,
// is ledgered rather than silently dropped or fabricated.
func TestJSLazyImportCallsRule_UnknownExportIsLedgered(t *testing.T) {
	nodes, parsed, paths := licFixtureNodes(t, map[string]string{
		"serve.ts": "export const serveCommand = async () => {};\n",
		"index.ts": "program.action(createLazyAction(() => import('./serve.js'), 'nope'));\n",
	})
	index, serve := paths["index.ts"], paths["serve.ts"]

	res, err := pipeline.Run(licActive(t), parsed, graph.Snapshot{
		Nodes: nodes, Files: []string{index, serve},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Edges) != 0 {
		t.Errorf("expected no edges for an unknown export name; got %+v", res.Edges)
	}
	var found bool
	for _, u := range res.Unresolved {
		if u.Kind == "lazy_import_export_unresolved" && u.Name == "nope" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'nope' ledgered as lazy_import_export_unresolved; got %+v", res.Unresolved)
	}
}

// TestJSLazyImportCallsRule_OrdinaryCallNeverMatches guards against false
// positives: a plain call with an arrow function argument and an unrelated
// string argument (no dynamic import inside the arrow) must never be
// mistaken for this shape.
func TestJSLazyImportCallsRule_OrdinaryCallNeverMatches(t *testing.T) {
	nodes, parsed, paths := licFixtureNodes(t, map[string]string{
		"index.ts": "registerHook(() => doSomething(), 'not-an-export');\n",
	})
	index := paths["index.ts"]

	res, err := pipeline.Run(licActive(t), parsed, graph.Snapshot{
		Nodes: nodes, Files: []string{index},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Edges) != 0 || len(res.Unresolved) != 0 {
		t.Errorf("ordinary call must never match the lazy-import shape; edges=%+v unresolved=%+v", res.Edges, res.Unresolved)
	}
}
