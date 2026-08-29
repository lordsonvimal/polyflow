package linker

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// LinkJSLazyImportCalls resolves a "lazy dynamic import + string export
// name" dispatch shape:
//
//	someCall(() => import('./serve.js'), 'serveCommand')
//
// Confirmed live on gitnexus's src/cli/index.ts: every CLI subcommand is
// wired via createLazyAction(() => import(path), exportName), which awaits
// the dynamic import and indexes the resulting module object by the string
// (module[exportName]) — a runtime property lookup no static call-graph pass
// can see, so 16 real command handlers (serveCommand, cleanCommand, ...)
// read as permanently zero-caller dead code. This is deliberately not
// hardcoded to gitnexus's own `createLazyAction` helper name: the shape
// (an arrow function whose body is a bare dynamic `import(...)` of a
// literal specifier, plus a sibling string-literal argument in the same
// call) is a generic lazy-route/plugin-loader pattern, not gitnexus-specific
// vocabulary — the outer call's own name is never inspected.
func LinkJSLazyImportCalls(nodes []graph.Node, serviceFiles map[string][]string) (newEdges []graph.Edge, unresolved []graph.UnresolvedRef) {
	// fileExportIndex: file (cwd-relative) → top-level declaration label →
	// nodeID. A dynamically-imported, string-keyed export can only be a
	// top-level function or variable declaration (see js_variables.go's
	// collectTopLevel) — there is no runtime property lookup shape that
	// could reach a class method or block-scoped local.
	fileExportIndex := make(map[string]map[string]string)
	// fileNodeID: "service\x00file" → NodeTypeFile ID, for module-level call
	// sites with no enclosing function to attribute to. Looked up, never
	// minted: this pass runs after ensure_scanned_files, which already
	// guarantees a file node for every scanned file.
	fileNodeID := make(map[string]string)
	declsByFile := make(map[string][]lineNode)
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeMethod, graph.NodeTypeVariable,
			graph.NodeTypeInterface, graph.NodeTypeClass:
			if n.Label != "(module)" {
				declsByFile[n.File] = append(declsByFile[n.File], lineNode{line: n.Line, id: n.ID})
			}
		case graph.NodeTypeFile:
			fileNodeID[n.Service+"\x00"+n.File] = n.ID
		}
		if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeVariable {
			continue
		}
		if fileExportIndex[n.File] == nil {
			fileExportIndex[n.File] = make(map[string]string)
		}
		if _, ok := fileExportIndex[n.File][n.Label]; !ok {
			fileExportIndex[n.File][n.Label] = n.ID
		}
	}
	for f := range declsByFile {
		sort.Slice(declsByFile[f], func(i, j int) bool {
			return declsByFile[f][i].line < declsByFile[f][j].line
		})
	}

	seen := make(map[string]bool)

	for svcName, files := range serviceFiles {
		svcFileSet := make(map[string]bool, len(files))
		for _, f := range files {
			if isJSFile(f) {
				svcFileSet[f] = true
			}
		}
		for _, file := range files {
			if !isJSFile(file) {
				continue
			}
			src, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			lang := grammarLangForFile(file)
			root, err := sitter.ParseCtx(context.Background(), src, lang)
			if err != nil {
				continue
			}
			relFile := patterns.RelativizeToCwd(file)

			var walk func(n *sitter.Node)
			walk = func(n *sitter.Node) {
				if n.Type() == "call_expression" {
					if spec, exportName, line, ok := lazyImportShape(n, src); ok {
						resolved := resolveJSImportPath(file, spec, svcFileSet)
						if resolved == "" {
							unresolved = append(unresolved, graph.UnresolvedRef{
								Service: svcName, File: relFile, Line: line,
								Name: exportName, Kind: "lazy_import_export_unresolved",
							})
						} else if toID, found := fileExportIndex[patterns.RelativizeToCwd(resolved)][exportName]; found {
							fromID := nearestDecl(declsByFile[relFile], line)
							if fromID == "" {
								fromID = fileNodeID[svcName+"\x00"+relFile]
							}
							if fromID != "" && fromID != toID {
								eid := fmt.Sprintf("calls:%s->%s", fromID, toID)
								if !seen[eid] {
									seen[eid] = true
									newEdges = append(newEdges, graph.Edge{
										ID: eid, From: fromID, To: toID,
										Type: graph.EdgeTypeCalls, Confidence: graph.ConfidenceInferred,
										Meta: map[string]string{"via": "lazy_import_export"},
									})
								}
							}
						} else {
							unresolved = append(unresolved, graph.UnresolvedRef{
								Service: svcName, File: relFile, Line: line,
								Name: exportName, Kind: "lazy_import_export_unresolved",
							})
						}
					}
				}
				for i := 0; i < int(n.NamedChildCount()); i++ {
					walk(n.NamedChild(i))
				}
			}
			walk(root)
		}
	}
	return newEdges, unresolved
}

// lazyImportShape matches a call expression carrying, among its arguments:
//   - an arrow_function (or function_expression) whose body is a bare
//     dynamic import() of a single string-literal specifier, and
//   - a sibling string-literal argument naming the export to pull off the
//     resolved module.
//
// Returns the import specifier, the export name, and the call's own line
// (for attribution/ledgering) — order-independent, since both known call
// sites in the wild (createLazyAction(loader, name) and the reverse) are
// plausible and neither is privileged over the other.
func lazyImportShape(call *sitter.Node, src []byte) (specifier, exportName string, line int, ok bool) {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return "", "", 0, false
	}
	var foundSpec, foundName bool
	for i := 0; i < int(args.NamedChildCount()); i++ {
		arg := args.NamedChild(i)
		switch arg.Type() {
		case "arrow_function", "function_expression":
			if s, ok := dynamicImportSpecifier(arg, src); ok {
				specifier = s
				foundSpec = true
			}
		case "string", "template_string":
			foundName = true
			exportName = stringLiteralValue(arg, src)
		}
	}
	if foundSpec && foundName && exportName != "" {
		return specifier, exportName, int(call.StartPoint().Row) + 1, true
	}
	return "", "", 0, false
}

// dynamicImportSpecifier returns the literal specifier of a lazy loader
// arrow/function whose body is exactly `import('<specifier>')` — either as
// the expression body (`() => import('./x')`) or a single-statement
// `{ return import('./x'); }` block.
func dynamicImportSpecifier(fn *sitter.Node, src []byte) (string, bool) {
	body := fn.ChildByFieldName("body")
	if body == nil {
		return "", false
	}
	if body.Type() == "statement_block" {
		if body.NamedChildCount() != 1 {
			return "", false
		}
		stmt := body.NamedChild(0)
		if stmt.Type() != "return_statement" || stmt.NamedChildCount() != 1 {
			return "", false
		}
		body = stmt.NamedChild(0)
	}
	if body.Type() != "call_expression" {
		return "", false
	}
	fnField := body.ChildByFieldName("function")
	if fnField == nil || fnField.Type() != "import" {
		return "", false
	}
	callArgs := body.ChildByFieldName("arguments")
	if callArgs == nil || callArgs.NamedChildCount() != 1 {
		return "", false
	}
	specNode := callArgs.NamedChild(0)
	if specNode.Type() != "string" && specNode.Type() != "template_string" {
		return "", false
	}
	return stringLiteralValue(specNode, src), true
}

// stringLiteralValue strips the surrounding quote characters from a `string`
// (or unescaped `template_string`) node's raw content.
func stringLiteralValue(n *sitter.Node, src []byte) string {
	return strings.Trim(n.Content(src), "\"'`")
}
