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

// LinkJSTypeRelations resolves cross-file inherits, implements, and
// instantiates edges for JavaScript and TypeScript. It re-parses each JS/TS
// file to find class_heritage (extends/implements) and new_expression nodes,
// then resolves the referenced class/interface names through the file's import
// bindings. Same-file edges (confidence=static) are already emitted by
// extractJSVariables; this pass only adds cross-file inferred edges.
func LinkJSTypeRelations(nodes []graph.Node, serviceFiles map[string][]string) ([]graph.Edge, []graph.UnresolvedRef) {
	// Build service-level class/interface table: name → nodeID (first wins).
	classTable := make(map[string]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeClass || n.Type == graph.NodeTypeInterface {
			if _, exists := classTable[n.Label]; !exists {
				classTable[n.Label] = n.ID
			}
		}
	}
	if len(classTable) == 0 {
		return nil, nil
	}

	// constructorByClass: classID → its own explicit constructor() method
	// node ID, when it declares one. A class without an explicit constructor
	// has an implicit no-op one — no node exists for that, so no edge is
	// fabricated. Built from EndLine, which collectClass already stamps on
	// every JS/TS class node: a "constructor"-labeled function node whose
	// Line falls inside [class.Line, class.EndLine] belongs to that class.
	// This reuses information the parser already recorded rather than
	// re-parsing every file a second time to answer the same question.
	ctorsByFile := make(map[string][]lineNode)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeFunction && n.Label == "constructor" {
			ctorsByFile[n.File] = append(ctorsByFile[n.File], lineNode{line: n.Line, id: n.ID})
		}
	}
	constructorByClass := make(map[string]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeClass {
			continue
		}
		for _, c := range ctorsByFile[n.File] {
			if c.line >= n.Line && c.line <= n.EndLine {
				constructorByClass[n.ID] = c.id
				break
			}
		}
	}

	// Build set of already-emitted inherits/implements/instantiates edge IDs
	// so we don't re-emit what the per-file extractor already produced.
	existingEdges := make(map[string]bool)
	for i := range nodes {
		// nodes don't contain edges; we'll dedup by ID in the seen map below.
		_ = nodes[i]
	}

	// Per-file declaration index (function/method/variable/interface/class),
	// sorted by line, so a cross-file type reference can attribute its uses_type
	// edge to the nearest enclosing declaration (nearest preceding decl — no
	// end_line is stored on JS nodes).
	declsByFile := make(map[string][]lineNode)
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeMethod, graph.NodeTypeVariable,
			graph.NodeTypeInterface, graph.NodeTypeClass:
			if n.Label == "(module)" {
				continue
			}
			declsByFile[n.File] = append(declsByFile[n.File], lineNode{line: n.Line, id: n.ID})
		}
	}
	for f := range declsByFile {
		sort.Slice(declsByFile[f], func(i, j int) bool {
			return declsByFile[f][i].line < declsByFile[f][j].line
		})
	}

	var allEdges []graph.Edge
	var allUnresolved []graph.UnresolvedRef
	seen := make(map[string]bool)

	for svcName, files := range serviceFiles {
		// Build a per-service class nodeID-by-label (same as classTable but
		// scoped to service, for unresolved miss detection).
		svcClassByLabel := make(map[string]string)
		for i := range nodes {
			n := &nodes[i]
			if n.Service != svcName {
				continue
			}
			if n.Type == graph.NodeTypeClass || n.Type == graph.NodeTypeInterface {
				if _, ex := svcClassByLabel[n.Label]; !ex {
					svcClassByLabel[n.Label] = n.ID
				}
			}
		}

		for _, file := range files {
			if !isJSFile(file) {
				continue
			}
			// declsByFile is keyed by n.File, which is already cwd-relative
			// (real written nodes) — file here is still the raw absolute
			// walked path, so the lookup must use the same relativized form
			// or it always misses (fileDecls silently empty on every call,
			// meaning walkTypeRefs's nearestDecl below could never attribute
			// a cross-file uses_type edge to anything).
			edges, unresolved := resolveJSTypeRelations(file, svcName, svcClassByLabel, declsByFile[patterns.RelativizeToCwd(file)], constructorByClass, existingEdges, seen)
			allEdges = append(allEdges, edges...)
			allUnresolved = append(allUnresolved, unresolved...)
		}
	}
	return allEdges, allUnresolved
}

func resolveJSTypeRelations(file, svcName string, classTable map[string]string, fileDecls []lineNode, constructorByClass map[string]string, existingEdges, seen map[string]bool) ([]graph.Edge, []graph.UnresolvedRef) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil
	}
	lang := grammarLangForFile(file)
	// file arrives absolute (serviceFiles carries the raw walked paths,
	// never relativized — unlike the main per-file parse path, which applies
	// this same conversion in javascript.go). Every node/ref ID this pass
	// mints must use the cwd-relative form to match already-written nodes,
	// or a cross-file edge like `instantiates` points at an ID nothing
	// wrote — a FOREIGN KEY constraint failure at write time (confirmed
	// indexing GitNexus's own repo: every cross-file new_expression hit
	// this). os.ReadFile/grammarLangForFile above still need the real path.
	relFile := patterns.RelativizeToCwd(file)
	root, err := sitter.ParseCtx(context.Background(), src, lang)
	if err != nil {
		return nil, nil
	}

	// Extract import bindings: localName → exportedName (same as resolveImportCalls).
	type importBinding struct {
		localName    string
		exportedName string
		relative     bool
	}
	var bindings []importBinding

	isRelative := func(raw string) bool {
		t := strings.Trim(raw, "\"'`")
		return strings.HasPrefix(t, "./") || strings.HasPrefix(t, "../")
	}

	namedQ, _ := compiledQuery(`
(import_statement
  (import_clause
    (named_imports
      (import_specifier
        name: (identifier) @exported
        alias: (identifier) @local)))
  source: (string) @source)`, lang)
	sameAliasQ, _ := compiledQuery(`
(import_statement
  (import_clause
    (named_imports
      (import_specifier
        name: (identifier) @name)))
  source: (string) @source)`, lang)

	for _, q := range []*sitter.Query{namedQ, sameAliasQ} {
		if q == nil {
			continue
		}
		cur := sitter.NewQueryCursor()
		cur.Exec(q, root)
		for {
			m, ok := cur.NextMatch()
			if !ok {
				break
			}
			caps := make(map[string]string)
			for _, c := range m.Captures {
				caps[q.CaptureNameForId(c.Index)] = c.Node.Content(src)
			}
			if exp, ok1 := caps["exported"]; ok1 {
				if loc, ok2 := caps["local"]; ok2 {
					bindings = append(bindings, importBinding{localName: loc, exportedName: exp, relative: isRelative(caps["source"])})
				}
			} else if name, ok := caps["name"]; ok {
				bindings = append(bindings, importBinding{localName: name, exportedName: name, relative: isRelative(caps["source"])})
			}
		}
	}

	plainImport := make(map[string]string) // localName → exportedName
	relativeNames := make(map[string]bool)
	for _, b := range bindings {
		expName := b.exportedName
		if expName == "" {
			expName = b.localName
		}
		plainImport[b.localName] = expName
		if b.relative {
			relativeNames[b.localName] = true
		}
	}

	if len(plainImport) == 0 {
		return nil, nil
	}

	// Build per-file node lookup for enclosing class — we need a "from" node ID.
	// For class heritage, "from" = the class node that extends/implements.
	// We identify class nodes by label in classTable.

	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef

	// Walk the AST to find class declarations with heritage.
	var walkNode func(n *sitter.Node)
	walkNode = func(n *sitter.Node) {
		t := n.Type()
		if t == "export_statement" {
			if decl := n.ChildByFieldName("declaration"); decl != nil {
				walkNode(decl)
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if c != n.ChildByFieldName("declaration") {
					walkNode(c)
				}
			}
			return
		}
		if t == "class_declaration" {
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				goto children
			}
			className := nameNode.Content(src)
			fromID := classTable[className]
			if fromID == "" {
				goto children
			}

			// Find class_heritage.
			for i := 0; i < int(n.NamedChildCount()); i++ {
				heritage := n.NamedChild(i)
				if heritage.Type() != "class_heritage" {
					continue
				}
				for j := 0; j < int(heritage.NamedChildCount()); j++ {
					clause := heritage.NamedChild(j)
					switch clause.Type() {
					case "extends_clause":
						val := clause.ChildByFieldName("value")
						if val == nil {
							continue
						}
						parentLocal := ""
						switch val.Type() {
						case "identifier", "type_identifier":
							parentLocal = val.Content(src)
						default:
							continue // expression super → already in ledger from extractor
						}
						exportedName, isImport := plainImport[parentLocal]
						if !isImport {
							continue // not an import; same-file handled by extractor
						}
						targetID, found := classTable[exportedName]
						if !found {
							if relativeNames[parentLocal] {
								missKey := fmt.Sprintf("%s:%s:inherits_unresolved", relFile, parentLocal)
								if !seen[missKey] {
									seen[missKey] = true
									unresolved = append(unresolved, graph.UnresolvedRef{
										Service: svcName, File: relFile,
										Line: int(val.StartPoint().Row) + 1,
										Name: exportedName, Kind: "inherits_unresolved",
									})
								}
							}
							continue
						}
						eid := fmt.Sprintf("inherits:%s->%s", fromID, targetID)
						if !seen[eid] {
							seen[eid] = true
							edges = append(edges, graph.Edge{
								ID: eid, From: fromID, To: targetID,
								Type: graph.EdgeTypeInherits, Confidence: graph.ConfidenceInferred,
								Meta: map[string]string{"via": "extends"},
							})
						}
					case "implements_clause":
						for k := 0; k < int(clause.NamedChildCount()); k++ {
							ti := clause.NamedChild(k)
							ifaceLocal := ""
							switch ti.Type() {
							case "type_identifier":
								ifaceLocal = ti.Content(src)
							case "generic_type":
								if base := ti.ChildByFieldName("name"); base != nil {
									ifaceLocal = base.Content(src)
								}
							}
							if ifaceLocal == "" {
								continue
							}
							exportedName, isImport := plainImport[ifaceLocal]
							if !isImport {
								continue
							}
							targetID, found := classTable[exportedName]
							if !found {
								if relativeNames[ifaceLocal] {
									missKey := fmt.Sprintf("%s:%s:implements_unresolved", relFile, ifaceLocal)
									if !seen[missKey] {
										seen[missKey] = true
										unresolved = append(unresolved, graph.UnresolvedRef{
											Service: svcName, File: relFile,
											Line: int(ti.StartPoint().Row) + 1,
											Name: exportedName, Kind: "implements_unresolved",
										})
									}
								}
								continue
							}
							eid := fmt.Sprintf("implements:%s->%s", fromID, targetID)
							if !seen[eid] {
								seen[eid] = true
								edges = append(edges, graph.Edge{
									ID: eid, From: fromID, To: targetID,
									Type: graph.EdgeTypeImplements, Confidence: graph.ConfidenceInferred,
									Meta: map[string]string{"nominal": "true"},
								})
							}
						}
					}
				}
			}
		}
		if t == "interface_declaration" {
			// TS interface extends cross-file.
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				goto children
			}
			ifaceName := nameNode.Content(src)
			fromID := classTable[ifaceName]
			if fromID == "" {
				goto children
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if c.Type() != "extends_type_clause" {
					continue
				}
				for j := 0; j < int(c.NamedChildCount()); j++ {
					parent := c.NamedChild(j)
					parentLocal := ""
					switch parent.Type() {
					case "type_identifier":
						parentLocal = parent.Content(src)
					case "generic_type":
						if base := parent.ChildByFieldName("name"); base != nil {
							parentLocal = base.Content(src)
						}
					}
					if parentLocal == "" {
						continue
					}
					exportedName, isImport := plainImport[parentLocal]
					if !isImport {
						continue
					}
					targetID, found := classTable[exportedName]
					if !found {
						if relativeNames[parentLocal] {
							missKey := fmt.Sprintf("%s:%s:inherits_unresolved_iface", relFile, parentLocal)
							if !seen[missKey] {
								seen[missKey] = true
								unresolved = append(unresolved, graph.UnresolvedRef{
									Service: svcName, File: relFile,
									Line: int(parent.StartPoint().Row) + 1,
									Name: exportedName, Kind: "inherits_unresolved",
								})
							}
						}
						continue
					}
					eid := fmt.Sprintf("inherits:%s->%s", fromID, targetID)
					if !seen[eid] {
						seen[eid] = true
						edges = append(edges, graph.Edge{
							ID: eid, From: fromID, To: targetID,
							Type: graph.EdgeTypeInherits, Confidence: graph.ConfidenceInferred,
							Meta: map[string]string{"via": "extends"},
						})
					}
				}
			}
		}

	children:
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walkNode(n.NamedChild(i))
		}
	}

	// Also handle new_expression cross-file instantiates.
	var walkNew func(n *sitter.Node, enclosingFnID string)
	walkNew = func(n *sitter.Node, enclosingFnID string) {
		t := n.Type()
		if t == "new_expression" {
			ctor := n.ChildByFieldName("constructor")
			if ctor != nil && (ctor.Type() == "identifier" || ctor.Type() == "type_identifier") {
				localName := ctor.Content(src)
				exportedName, isImport := plainImport[localName]
				if isImport {
					targetID, found := classTable[exportedName]
					if found && enclosingFnID != "" {
						eid := fmt.Sprintf("instantiates:%s->%s", enclosingFnID, targetID)
						if !seen[eid] {
							seen[eid] = true
							edges = append(edges, graph.Edge{
								ID: eid, From: enclosingFnID, To: targetID,
								Type: graph.EdgeTypeInstantiates, Confidence: graph.ConfidenceInferred,
								Meta: map[string]string{"count": "1"},
							})
						}
						// Additive: also land a method-granularity `calls`
						// edge on the class's own explicit constructor, the
						// same shape as the Ruby `.new` → `initialize` edge in
						// LinkRubyTypeRelations. No edge for a class with no
						// explicit constructor (constructorByClass has no
						// entry — JS/TS's implicit default takes no edge).
						if found && enclosingFnID != "" {
							if ctorID, ok := constructorByClass[targetID]; ok {
								ceid := fmt.Sprintf("calls:%s->%s", enclosingFnID, ctorID)
								if !seen[ceid] {
									seen[ceid] = true
									edges = append(edges, graph.Edge{
										ID: ceid, From: enclosingFnID, To: ctorID,
										Type: graph.EdgeTypeCalls, Confidence: graph.ConfidenceInferred,
										Meta: map[string]string{"via": "instantiate_constructor"},
									})
								}
							}
						}
					}
				}
			}
		}
		// Track enclosing function for new_expression attribution.
		newFnID := enclosingFnID
		if isFunctionLike(t) {
			// Try to determine the function node ID from classTable-adjacent structures.
			// Since we don't have function nodes directly, use the file+name pattern.
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				fnName := nameNode.Content(src)
				// Function node ID: service:file:function:name:line
				newFnID = fmt.Sprintf("%s:%s:function:%s:%d", svcName, relFile, fnName, int(n.StartPoint().Row)+1)
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walkNew(n.NamedChild(i), newFnID)
		}
	}

	// Cross-file uses_type: a type_identifier referencing an imported interface/
	// class (annotations, generic args, member types) binds the nearest enclosing
	// declaration to the type's definition node. Same-file references are already
	// handled by the parser's extractTypeUses; only imports resolve here.
	var walkTypeRefs func(n *sitter.Node)
	walkTypeRefs = func(n *sitter.Node) {
		if n.Type() == "type_identifier" && isTypeUseContext(n) {
			local := n.Content(src)
			if exportedName, isImport := plainImport[local]; isImport {
				if targetID, found := classTable[exportedName]; found {
					if fromID := nearestDecl(fileDecls, int(n.StartPoint().Row)+1); fromID != "" && fromID != targetID {
						eid := fmt.Sprintf("uses_type:%s->%s", fromID, targetID)
						if !seen[eid] {
							seen[eid] = true
							edges = append(edges, graph.Edge{
								ID: eid, From: fromID, To: targetID,
								Type: graph.EdgeTypeUsesType, Confidence: graph.ConfidenceInferred,
								Meta: map[string]string{"via": "type_ref_import"},
							})
						}
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walkTypeRefs(n.NamedChild(i))
		}
	}

	walkNode(root)
	walkNew(root, "")
	walkTypeRefs(root)
	return edges, unresolved
}

// isTypeUseContext reports whether a type_identifier is a use of a type (not a
// declaration name or a heritage target, which are captured elsewhere).
func isTypeUseContext(n *sitter.Node) bool {
	p := n.Parent()
	if p == nil {
		return false
	}
	switch p.Type() {
	case "interface_declaration", "class_declaration",
		"extends_clause", "implements_clause", "extends_type_clause":
		return false
	}
	return true
}

// nearestDecl returns the ID of the declaration whose line is the greatest not
// exceeding refLine (nearest preceding declaration), or "" if none. fileDecls
// is sorted ascending by line.
func nearestDecl(fileDecls []lineNode, refLine int) string {
	id := ""
	for _, d := range fileDecls {
		if d.line <= refLine {
			id = d.id
		} else {
			break
		}
	}
	return id
}

func isFunctionLike(t string) bool {
	switch t {
	case "function_declaration", "function_expression", "arrow_function",
		"method_definition", "generator_function_declaration":
		return true
	}
	return false
}
