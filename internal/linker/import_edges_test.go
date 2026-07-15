package linker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// fileNodeID returns the NodeTypeFile node ID for (service, file) using the
// same format as LinkContainment.
func fileNodeID(service, file string) string {
	return service + ":" + file + ":file"
}

// makeFileNode builds a synthetic NodeTypeFile node (as LinkContainment would).
func makeFileNode(service, file string) graph.Node {
	return graph.Node{
		ID:      fileNodeID(service, file),
		Type:    graph.NodeTypeFile,
		Label:   file,
		Service: service,
		File:    file,
		Meta:    map[string]string{"basename": filepath.Base(file)},
	}
}

// ── JS/TS import-map → file→file edges ──────────────────────────────────────

// TestLinkJSImportEdges_RelativeImport: a TS file that imports from a relative
// module produces a static file→file imports edge between the file nodes.
func TestLinkJSImportEdges_RelativeImport(t *testing.T) {
	dir := t.TempDir()

	// Create utils.ts
	utilsFile := filepath.Join(dir, "utils.ts")
	if err := os.WriteFile(utilsFile, []byte(`export function helper() {}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create app.ts that imports from utils
	appFile := filepath.Join(dir, "app.ts")
	if err := os.WriteFile(appFile, []byte(`import { helper } from "./utils";
helper();
`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate what LinkContainment would produce for these files.
	appFileNode := makeFileNode("svc", appFile)
	utilsFileNode := makeFileNode("svc", utilsFile)

	nodes := []graph.Node{appFileNode, utilsFileNode}
	svcFiles := map[string][]string{"svc": {appFile, utilsFile}}

	edges, updatedNodes, unresolved := LinkJSImportEdges(nodes, svcFiles)

	// Should have exactly one imports edge: app.ts → utils.ts
	if len(edges) != 1 {
		t.Fatalf("expected 1 imports edge, got %d: %+v", len(edges), edges)
	}
	e := edges[0]
	if e.Type != graph.EdgeTypeImports {
		t.Errorf("edge type = %q, want %q", e.Type, graph.EdgeTypeImports)
	}
	if e.From != fileNodeID("svc", appFile) {
		t.Errorf("edge.From = %q, want %q", e.From, fileNodeID("svc", appFile))
	}
	if e.To != fileNodeID("svc", utilsFile) {
		t.Errorf("edge.To = %q, want %q", e.To, fileNodeID("svc", utilsFile))
	}
	if e.Confidence != graph.ConfidenceStatic {
		t.Errorf("confidence = %q, want static", e.Confidence)
	}

	// No unresolved refs for resolved imports.
	if len(unresolved) != 0 {
		t.Errorf("unexpected unresolved: %+v", unresolved)
	}

	// No updated nodes (no npm imports in this file).
	_ = updatedNodes
}

// TestLinkJSImportEdges_CrossDir: a relative import using ../ resolves
// correctly when the importing and imported files are in different directories.
func TestLinkJSImportEdges_CrossDir(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}

	baseFile := filepath.Join(lib, "base.ts")
	if err := os.WriteFile(baseFile, []byte(`export class Base {}`), 0o644); err != nil {
		t.Fatal(err)
	}
	componentFile := filepath.Join(dir, "components", "Widget.tsx")
	if err := os.MkdirAll(filepath.Dir(componentFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(componentFile, []byte(`import { Base } from "../lib/base";
export class Widget extends Base {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	nodes := []graph.Node{
		makeFileNode("svc", componentFile),
		makeFileNode("svc", baseFile),
	}
	svcFiles := map[string][]string{"svc": {componentFile, baseFile}}

	edges, _, unresolved := LinkJSImportEdges(nodes, svcFiles)

	if len(edges) != 1 {
		t.Fatalf("expected 1 imports edge, got %d: %+v", len(edges), edges)
	}
	if edges[0].From != fileNodeID("svc", componentFile) || edges[0].To != fileNodeID("svc", baseFile) {
		t.Errorf("wrong edge: from=%q to=%q", edges[0].From, edges[0].To)
	}
	if len(unresolved) != 0 {
		t.Errorf("unexpected unresolved: %+v", unresolved)
	}
}

// TestLinkJSImportEdges_ExtImportsNotEdge: npm bare-specifier imports produce
// no edges and no unresolved refs; their count goes to file-node meta.
func TestLinkJSImportEdges_ExtImportsNotEdge(t *testing.T) {
	dir := t.TempDir()
	appFile := filepath.Join(dir, "app.ts")
	if err := os.WriteFile(appFile, []byte(`import React from "react";
import { useState } from "react";
import { createStore } from "redux";
`), 0o644); err != nil {
		t.Fatal(err)
	}

	nodes := []graph.Node{makeFileNode("svc", appFile)}
	svcFiles := map[string][]string{"svc": {appFile}}

	edges, updatedNodes, unresolved := LinkJSImportEdges(nodes, svcFiles)

	if len(edges) != 0 {
		t.Errorf("npm imports must not produce edges, got: %+v", edges)
	}
	if len(unresolved) != 0 {
		t.Errorf("npm imports must not produce unresolved refs, got: %+v", unresolved)
	}
	// The file node should be updated with external_imports count.
	if len(updatedNodes) != 1 {
		t.Fatalf("expected 1 updated file node, got %d", len(updatedNodes))
	}
	// Two unique npm sources: "react" and "redux" ("react" appears twice in two
	// import statements but is one unique package).
	if v := updatedNodes[0].Meta["external_imports"]; v != "2" {
		t.Errorf("external_imports = %q, want %q (unique npm packages)", v, "2")
	}
}

// TestLinkJSImportEdges_NoCallEdges_FileHopProves: a fixture where no
// call edges exist between files — only import edges — so an impact on the
// imported file lists the importer via the file→file edge. This proves the
// edge carries information that call edges don't.
func TestLinkJSImportEdges_NoCallEdges_FileHopProves(t *testing.T) {
	dir := t.TempDir()

	// types.ts has only type definitions (no callable functions).
	typesFile := filepath.Join(dir, "types.ts")
	if err := os.WriteFile(typesFile, []byte(`export type Foo = { x: number };`), 0o644); err != nil {
		t.Fatal(err)
	}
	// consumer.ts imports the type — no calls, no call edges.
	consumerFile := filepath.Join(dir, "consumer.ts")
	if err := os.WriteFile(consumerFile, []byte(`import type { Foo } from "./types";
const f: Foo = { x: 1 };
`), 0o644); err != nil {
		t.Fatal(err)
	}

	typesNode := makeFileNode("svc", typesFile)
	consumerNode := makeFileNode("svc", consumerFile)
	nodes := []graph.Node{typesNode, consumerNode}
	svcFiles := map[string][]string{"svc": {consumerFile, typesFile}}

	edges, _, _ := LinkJSImportEdges(nodes, svcFiles)

	var found bool
	for _, e := range edges {
		if e.From == fileNodeID("svc", consumerFile) && e.To == fileNodeID("svc", typesFile) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected imports edge consumer→types; edges: %+v", edges)
	}
}

// TestLinkJSImportEdges_GoFileNoEdge: Go files are not JS/TS and must not
// produce any import edges (Go is deliberately descoped in I.3).
func TestLinkJSImportEdges_GoFileNoEdge(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte(`package main
import "fmt"
func main() { fmt.Println("hi") }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	nodes := []graph.Node{makeFileNode("svc", goFile)}
	svcFiles := map[string][]string{"svc": {goFile}}

	edges, updatedNodes, unresolved := LinkJSImportEdges(nodes, svcFiles)
	if len(edges) != 0 {
		t.Errorf("Go file must produce no import edges, got: %+v", edges)
	}
	if len(updatedNodes) != 0 {
		t.Errorf("Go file must not update nodes, got: %+v", updatedNodes)
	}
	if len(unresolved) != 0 {
		t.Errorf("Go file must produce no unresolved refs, got: %+v", unresolved)
	}
}

// ── Ruby require_relative edges ───────────────────────────────────────────────

// TestLinkRubyImportEdges_RequireRelative: require_relative 'path' produces a
// static file→file imports edge.
func TestLinkRubyImportEdges_RequireRelative(t *testing.T) {
	dir := t.TempDir()

	helperFile := filepath.Join(dir, "helper.rb")
	if err := os.WriteFile(helperFile, []byte(`module Helper; end`), 0o644); err != nil {
		t.Fatal(err)
	}
	mainFile := filepath.Join(dir, "main.rb")
	if err := os.WriteFile(mainFile, []byte(`require_relative 'helper'
include Helper
`), 0o644); err != nil {
		t.Fatal(err)
	}

	nodes := []graph.Node{
		makeFileNode("svc", mainFile),
		makeFileNode("svc", helperFile),
	}
	svcFiles := map[string][]string{"svc": {mainFile, helperFile}}

	edges, unresolved := LinkRubyImportEdges(nodes, svcFiles)

	if len(edges) != 1 {
		t.Fatalf("expected 1 imports edge, got %d: %+v", len(edges), edges)
	}
	e := edges[0]
	if e.Type != graph.EdgeTypeImports {
		t.Errorf("edge type = %q, want imports", e.Type)
	}
	if e.From != fileNodeID("svc", mainFile) || e.To != fileNodeID("svc", helperFile) {
		t.Errorf("wrong edge: from=%q to=%q", e.From, e.To)
	}
	if e.Confidence != graph.ConfidenceStatic {
		t.Errorf("confidence = %q, want static", e.Confidence)
	}
	if e.Meta["via"] != "require_relative" {
		t.Errorf("meta via = %q, want require_relative", e.Meta["via"])
	}
	if len(unresolved) != 0 {
		t.Errorf("unexpected unresolved: %+v", unresolved)
	}
}

// TestLinkRubyImportEdges_MissingTarget: require_relative to a file that
// doesn't exist in the indexed set goes to the import_unresolved ledger.
func TestLinkRubyImportEdges_MissingTarget(t *testing.T) {
	dir := t.TempDir()
	mainFile := filepath.Join(dir, "main.rb")
	if err := os.WriteFile(mainFile, []byte(`require_relative 'nonexistent'
`), 0o644); err != nil {
		t.Fatal(err)
	}

	nodes := []graph.Node{makeFileNode("svc", mainFile)}
	svcFiles := map[string][]string{"svc": {mainFile}}

	edges, unresolved := LinkRubyImportEdges(nodes, svcFiles)

	if len(edges) != 0 {
		t.Errorf("missing target must produce no edge, got: %+v", edges)
	}
	if len(unresolved) != 1 {
		t.Fatalf("expected 1 unresolved entry, got %d: %+v", len(unresolved), unresolved)
	}
	u := unresolved[0]
	if u.Kind != "import_unresolved" {
		t.Errorf("unresolved.Kind = %q, want import_unresolved", u.Kind)
	}
	if u.Service != "svc" {
		t.Errorf("unresolved.Service = %q, want svc", u.Service)
	}
}

// TestLinkRubyImportEdges_RequireRelativeSubdir: require_relative 'models/user'
// resolves a file in a subdirectory.
func TestLinkRubyImportEdges_RequireRelativeSubdir(t *testing.T) {
	dir := t.TempDir()
	models := filepath.Join(dir, "models")
	if err := os.MkdirAll(models, 0o755); err != nil {
		t.Fatal(err)
	}
	userFile := filepath.Join(models, "user.rb")
	if err := os.WriteFile(userFile, []byte(`class User; end`), 0o644); err != nil {
		t.Fatal(err)
	}
	controllerFile := filepath.Join(dir, "app_controller.rb")
	if err := os.WriteFile(controllerFile, []byte(`require_relative 'models/user'
`), 0o644); err != nil {
		t.Fatal(err)
	}

	nodes := []graph.Node{
		makeFileNode("svc", controllerFile),
		makeFileNode("svc", userFile),
	}
	svcFiles := map[string][]string{"svc": {controllerFile, userFile}}

	edges, unresolved := LinkRubyImportEdges(nodes, svcFiles)

	if len(edges) != 1 {
		t.Fatalf("expected 1 imports edge, got %d: %+v", len(edges), edges)
	}
	if edges[0].To != fileNodeID("svc", userFile) {
		t.Errorf("edge.To = %q, want %q", edges[0].To, fileNodeID("svc", userFile))
	}
	if len(unresolved) != 0 {
		t.Errorf("unexpected unresolved: %+v", unresolved)
	}
}

// TestLinkRubyImportEdges_GoNegative: Go files in the service file list must
// not produce any edges (Ruby pass is Ruby-only).
func TestLinkRubyImportEdges_GoNegative(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte(`package main`), 0o644); err != nil {
		t.Fatal(err)
	}
	nodes := []graph.Node{makeFileNode("svc", goFile)}
	svcFiles := map[string][]string{"svc": {goFile}}

	edges, unresolved := LinkRubyImportEdges(nodes, svcFiles)
	if len(edges) != 0 || len(unresolved) != 0 {
		t.Errorf("Go files must produce no edges/unresolved; edges=%v unresolved=%v", edges, unresolved)
	}
}
