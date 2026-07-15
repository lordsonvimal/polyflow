package linker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// LinkJSImportEdges emits file→file imports edges for JS/TS files between the
// NodeTypeFile backbone nodes synthesized by LinkContainment. Must run after
// LinkContainment so the file node IDs exist in nodes.
//
// Each resolved relative import (./x, ../x) in a JS/TS file becomes one
// imports edge from the importing file node to the imported file node, with
// confidence=static (the import-map resolution already proved the link).
//
// Bare-specifier (npm package) imports are out of scope: no edge, no ledger.
// Their count is added to the importing file node's meta (external_imports=<n>)
// and those updated nodes are returned so the indexer can upsert them.
func LinkJSImportEdges(nodes []graph.Node, serviceFiles map[string][]string) (newEdges []graph.Edge, updatedNodes []graph.Node, unresolved []graph.UnresolvedRef) {
	// Index NodeTypeFile nodes: "svc\x00file" → nodeID
	fileNodeID := make(map[string]string)
	// Also index by ID so we can update meta on the node copy.
	fileNodeByID := make(map[string]graph.Node)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeFile {
			key := n.Service + "\x00" + n.File
			fileNodeID[key] = n.ID
			fileNodeByID[n.ID] = *n
		}
	}

	// Build per-service set of all indexed JS/TS file paths (for resolution).
	svcFileSet := make(map[string]map[string]bool, len(serviceFiles))
	for svcName, files := range serviceFiles {
		s := make(map[string]bool, len(files))
		for _, f := range files {
			if isJSFile(f) {
				s[f] = true
			}
		}
		svcFileSet[svcName] = s
	}

	seen := make(map[string]bool)

	for svcName, files := range serviceFiles {
		for _, file := range files {
			if !isJSFile(file) {
				continue
			}
			importingNodeID := fileNodeID[svcName+"\x00"+file]
			if importingNodeID == "" {
				continue // file node not yet present (containment not run yet)
			}

			relImports, extCount := parseJSImportSources(file)

			if extCount > 0 {
				orig := fileNodeByID[importingNodeID]
				updated := orig
				if updated.Meta == nil {
					updated.Meta = make(map[string]string)
				} else {
					// Make a copy of the meta map so we don't mutate the original.
					m := make(map[string]string, len(orig.Meta)+1)
					for k, v := range orig.Meta {
						m[k] = v
					}
					updated.Meta = m
				}
				updated.Meta["external_imports"] = strconv.Itoa(extCount)
				updatedNodes = append(updatedNodes, updated)
			}

			for _, src := range relImports {
				resolved := resolveJSImportPath(file, src, svcFileSet[svcName])
				if resolved == "" {
					continue
				}
				importedNodeID := fileNodeID[svcName+"\x00"+resolved]
				if importedNodeID == "" {
					continue
				}
				eid := fmt.Sprintf("imports:%s->%s", importingNodeID, importedNodeID)
				if !seen[eid] {
					seen[eid] = true
					newEdges = append(newEdges, graph.Edge{
						ID:         eid,
						From:       importingNodeID,
						To:         importedNodeID,
						Type:       graph.EdgeTypeImports,
						Confidence: graph.ConfidenceStatic,
						Meta:       map[string]string{"via": "import_statement"},
					})
				}
			}
		}
	}
	return
}

// parseJSImportSources extracts import source specifiers from a JS/TS file.
// Returns relative imports (starting with ./ or ../) and the count of external
// (bare-specifier / npm) imports.
func parseJSImportSources(file string) (relative []string, externalCount int) {
	src, err := os.ReadFile(file)
	if err != nil {
		return
	}
	lang := grammarLangForFile(file)
	root, err := sitter.ParseCtx(context.Background(), src, lang)
	if err != nil {
		return
	}

	q, err := sitter.NewQuery([]byte(`(import_statement source: (string) @source)`), lang)
	if err != nil {
		return
	}
	cur := sitter.NewQueryCursor()
	cur.Exec(q, root)

	seenSrc := make(map[string]bool)
	for {
		m, ok := cur.NextMatch()
		if !ok {
			break
		}
		for _, c := range m.Captures {
			raw := c.Node.Content(src)
			trimmed := strings.Trim(raw, "\"'`")
			if seenSrc[trimmed] {
				continue
			}
			seenSrc[trimmed] = true
			if strings.HasPrefix(trimmed, "./") || strings.HasPrefix(trimmed, "../") {
				relative = append(relative, trimmed)
			} else {
				externalCount++
			}
		}
	}
	return
}

// jsExtensions are tried in order when a relative import has no extension.
var jsExtensions = []string{".ts", ".tsx", ".js", ".jsx", ".mjs"}

// resolveJSImportPath resolves a relative import specifier (e.g. "./utils",
// "../lib/foo") to the absolute-or-relative file path used by the service
// indexer, by probing the indexed file set. Returns "" when not found.
func resolveJSImportPath(importingFile, specifier string, indexedFiles map[string]bool) string {
	dir := filepath.Dir(importingFile)
	base := filepath.Clean(filepath.Join(dir, specifier))

	ext := strings.ToLower(filepath.Ext(specifier))
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs":
		if indexedFiles[base] {
			return base
		}
		// TypeScript projects often use .js extensions pointing at .ts source.
		if ext == ".js" {
			ts := base[:len(base)-3] + ".ts"
			if indexedFiles[ts] {
				return ts
			}
		}
		return ""
	}

	// No recognised extension: probe common extensions, then index files.
	for _, e := range jsExtensions {
		if c := base + e; indexedFiles[c] {
			return c
		}
	}
	for _, idx := range []string{"/index.ts", "/index.tsx", "/index.js", "/index.jsx"} {
		if c := base + idx; indexedFiles[c] {
			return c
		}
	}
	return ""
}

// LinkRubyImportEdges emits file→file imports edges for Ruby files between the
// NodeTypeFile backbone nodes synthesized by LinkContainment. Must run after
// LinkContainment so the file node IDs exist in nodes.
//
// Only require_relative calls are handled: the path is resolvable statically.
// Plain require of in-service files under Rails autoload conventions is NOT
// guessed — that is L.W-style future work.
// When a require_relative target file does not exist in the indexed set, an
// import_unresolved ledger entry is emitted instead of an edge.
func LinkRubyImportEdges(nodes []graph.Node, serviceFiles map[string][]string) ([]graph.Edge, []graph.UnresolvedRef) {
	fileNodeID := make(map[string]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeFile {
			key := n.Service + "\x00" + n.File
			fileNodeID[key] = n.ID
		}
	}

	svcRubyFiles := make(map[string]map[string]bool, len(serviceFiles))
	for svcName, files := range serviceFiles {
		s := make(map[string]bool, len(files))
		for _, f := range files {
			if isRubyFile(f) {
				s[f] = true
			}
		}
		svcRubyFiles[svcName] = s
	}

	var allEdges []graph.Edge
	var allUnresolved []graph.UnresolvedRef
	seen := make(map[string]bool)

	for svcName, files := range serviceFiles {
		for _, file := range files {
			if !isRubyFile(file) {
				continue
			}
			importingNodeID := fileNodeID[svcName+"\x00"+file]
			if importingNodeID == "" {
				continue
			}

			specs := parseRubyRequireRelative(file)
			for _, spec := range specs {
				resolved, missing := resolveRubyImportPath(file, spec, svcRubyFiles[svcName])
				if missing {
					missKey := file + "\x00" + spec
					if !seen[missKey] {
						seen[missKey] = true
						allUnresolved = append(allUnresolved, graph.UnresolvedRef{
							Service: svcName, File: file,
							Name: spec, Kind: "import_unresolved",
						})
					}
					continue
				}
				if resolved == "" {
					continue
				}
				importedNodeID := fileNodeID[svcName+"\x00"+resolved]
				if importedNodeID == "" {
					continue
				}
				eid := fmt.Sprintf("imports:%s->%s", importingNodeID, importedNodeID)
				if !seen[eid] {
					seen[eid] = true
					allEdges = append(allEdges, graph.Edge{
						ID:         eid,
						From:       importingNodeID,
						To:         importedNodeID,
						Type:       graph.EdgeTypeImports,
						Confidence: graph.ConfidenceStatic,
						Meta:       map[string]string{"via": "require_relative"},
					})
				}
			}
		}
	}
	return allEdges, allUnresolved
}

// parseRubyRequireRelative extracts require_relative specifiers from a Ruby
// file by walking the tree-sitter AST for call nodes.
func parseRubyRequireRelative(file string) []string {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil
	}
	defer tree.Close()
	root := tree.RootNode()

	var paths []string
	seen := make(map[string]bool)

	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "call" {
			mn := n.ChildByFieldName("method")
			if mn != nil && mn.Content(src) == "require_relative" {
				if args := n.ChildByFieldName("arguments"); args != nil {
					for i := 0; i < int(args.NamedChildCount()); i++ {
						a := args.NamedChild(i)
						if a.Type() != "string" {
							continue
						}
						content := extractRubyStringContent(a, src)
						if content != "" && !seen[content] {
							seen[content] = true
							paths = append(paths, content)
						}
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return paths
}

// extractRubyStringContent returns the unquoted content of a Ruby string node.
// Tries the string_content child first; falls back to stripping outer quotes.
func extractRubyStringContent(n *sitter.Node, src []byte) string {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c.Type() == "string_content" {
			return c.Content(src)
		}
	}
	return stripMeta(n.Content(src))
}

// resolveRubyImportPath resolves a require_relative specifier to the indexed
// file path. Returns (path, false) on success, ("", true) when the specifier
// resolves to a path not in the indexed set (missing target → ledger), and
// ("", false) when the specifier itself is empty or not a relative path.
func resolveRubyImportPath(importingFile, spec string, indexedFiles map[string]bool) (resolved string, missing bool) {
	if spec == "" {
		return "", false
	}
	dir := filepath.Dir(importingFile)
	base := filepath.Clean(filepath.Join(dir, spec))

	ext := strings.ToLower(filepath.Ext(spec))
	if ext == ".rb" {
		if indexedFiles[base] {
			return base, false
		}
		return "", true
	}
	// No extension: try adding .rb.
	candidate := base + ".rb"
	if indexedFiles[candidate] {
		return candidate, false
	}
	// Try without extension (some files have no extension).
	if indexedFiles[base] {
		return base, false
	}
	return "", true
}
