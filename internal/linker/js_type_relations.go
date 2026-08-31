package linker

import (
	"fmt"
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
// bindings. Same-file inherits/implements/instantiates edges (confidence=
// static) are already emitted by extractJSVariables; this pass adds
// cross-file inferred edges, plus the instantiate→constructor `calls` fill-in
// for every `instantiates` edge already in priorEdges regardless of which
// pass produced it (see the loop below).
func LinkJSTypeRelations(nodes []graph.Node, priorEdges []graph.Edge, serviceFiles map[string][]string) ([]graph.Edge, []graph.UnresolvedRef) {
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

	// Same-file (and global, no-import) instantiate→constructor fill-in:
	// extractJSVariables' handleNew only emits the class-granularity
	// `instantiates` edge for a `new X()` whose class it resolved in the same
	// file, never the method-granularity `calls` edge onto that class's
	// explicit constructor — the cross-file `walkNew` case below already adds
	// it, but only when the constructor name came in through a plain import,
	// so every same-file instantiation (and every asset-pipeline-style global
	// class with no import/export at all, e.g. `var X = class X {...}`) left
	// its constructor with zero inbound `calls` edges regardless of how many
	// times the class was actually instantiated. Walking priorEdges instead
	// of re-parsing catches both shapes uniformly. EdgeTypeComponentImpl
	// (rails_views.go's react_component(...) mount) gets the same treatment —
	// a mounted class's constructor is otherwise left permanently zero-caller
	// the same way a same-file `new X()` was before this fill-in existed.
	for _, e := range priorEdges {
		if e.Type != graph.EdgeTypeInstantiates && e.Type != graph.EdgeTypeComponentImpl {
			continue
		}
		ctorID, ok := constructorByClass[e.To]
		if !ok {
			continue
		}
		ceid := fmt.Sprintf("calls:%s->%s", e.From, ctorID)
		if seen[ceid] {
			continue
		}
		seen[ceid] = true
		allEdges = append(allEdges, graph.Edge{
			ID: ceid, From: e.From, To: ctorID,
			Type: graph.EdgeTypeCalls, Confidence: graph.ConfidenceInferred,
			Meta: map[string]string{"via": "instantiate_constructor"},
		})
	}

	// fileClassIndex: file (cwd-relative) → label → nodeID. Unlike classTable/
	// svcClassByLabel (service-scoped, first-label-wins), a default-export
	// resolution must address the exact class declared in the exact target
	// file — two files in the same service can legally declare same-named
	// classes, and classTable would silently pick the wrong one.
	fileClassIndex := make(map[string]map[string]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeClass && n.Type != graph.NodeTypeInterface {
			continue
		}
		if fileClassIndex[n.File] == nil {
			fileClassIndex[n.File] = make(map[string]string)
		}
		if _, ex := fileClassIndex[n.File][n.Label]; !ex {
			fileClassIndex[n.File][n.Label] = n.ID
		}
	}

	// defaultExportCache memoizes each target file's default-exported class
	// name (or "" for a miss) across every importer that references it, so a
	// widely-imported default export is only re-parsed once per run.
	defaultExportCache := make(map[string]string)

	for svcName, files := range serviceFiles {
		// Build a per-service class nodeID-by-label (same as classTable but
		// scoped to service, for unresolved miss detection).
		svcClassByLabel := make(map[string]string)
		// svcFileSet: raw (pre-relativize) indexed JS/TS paths for this
		// service, in the same form serviceFiles carries them — the shape
		// resolveJSImportPath (import_edges.go) already expects.
		svcFileSet := make(map[string]bool)
		// svcGlobalClassByName: global name (leaf or full dotted path, e.g.
		// both "PusherClient" and "window.PusherClient") → classID, built from
		// stampGlobalSymbols' class-node stamping (js_variables.go) of a
		// same-file `window.X = X` self-registration. Resolves the DC.15 shape:
		// a `new window.X(...)` in a *different* file than X's declaration.
		svcGlobalClassByName := make(map[string]string)
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
			if n.Type != graph.NodeTypeClass {
				continue
			}
			for _, key := range []string{n.Meta["global_symbol"], n.Meta["global_path"]} {
				if key == "" {
					continue
				}
				if _, ex := svcGlobalClassByName[key]; !ex {
					svcGlobalClassByName[key] = n.ID
				}
			}
		}
		for _, f := range files {
			if isJSFile(f) {
				svcFileSet[f] = true
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
			edges, unresolved := resolveJSTypeRelations(file, svcName, svcClassByLabel, declsByFile[patterns.RelativizeToCwd(file)], constructorByClass, existingEdges, seen, svcFileSet, fileClassIndex, defaultExportCache, svcGlobalClassByName)
			allEdges = append(allEdges, edges...)
			allUnresolved = append(allUnresolved, unresolved...)
		}
	}
	return allEdges, allUnresolved
}

func resolveJSTypeRelations(file, svcName string, classTable map[string]string, fileDecls []lineNode, constructorByClass map[string]string, existingEdges, seen map[string]bool, svcFileSet map[string]bool, fileClassIndex map[string]map[string]string, defaultExportCache map[string]string, globalClassByName map[string]string) ([]graph.Edge, []graph.UnresolvedRef) {
	src, root, lang, ok := jsParse(file)
	if !ok {
		return nil, nil
	}
	// file arrives absolute (serviceFiles carries the raw walked paths,
	// never relativized — unlike the main per-file parse path, which applies
	// this same conversion in javascript.go). Every node/ref ID this pass
	// mints must use the cwd-relative form to match already-written nodes,
	// or a cross-file edge like `instantiates` points at an ID nothing
	// wrote — a FOREIGN KEY constraint failure at write time (confirmed
	// indexing GitNexus's own repo: every cross-file new_expression hit
	// this). Reading the file above still needs the real path.
	relFile := patterns.RelativizeToCwd(file)

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
	// defaultQ matches a bare default-import binding (`import X from './x'`,
	// including the combined `import X, { Y } from './x'` form) — a direct
	// (identifier) child of import_clause is only produced by that shape;
	// namespace_import and named_imports wrap their identifiers one level
	// deeper, so this can't misfire on either.
	defaultQ, _ := compiledQuery(`
(import_statement
  (import_clause
    (identifier) @local)
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

	// Default imports (`import X from './x'`) carry no exported-name
	// information in the import statement itself — unlike a named import,
	// the local name is not guaranteed to match anything in the target file.
	// Resolving one requires following the source specifier to its file and
	// reading *that* file's own default export declaration, keying
	// defaultImportTarget off the target file's actual exported class rather
	// than the local name.
	defaultImportTarget := make(map[string]string) // localName → class/interface nodeID
	if defaultQ != nil {
		cur := sitter.NewQueryCursor()
		cur.Exec(defaultQ, root)
		for {
			m, ok := cur.NextMatch()
			if !ok {
				break
			}
			caps := make(map[string]string)
			for _, c := range m.Captures {
				caps[defaultQ.CaptureNameForId(c.Index)] = c.Node.Content(src)
			}
			local, source := caps["local"], caps["source"]
			if local == "" || source == "" || !isRelative(source) {
				continue
			}
			resolvedFile := resolveJSImportPath(file, strings.Trim(source, "\"'`"), svcFileSet)
			if resolvedFile == "" {
				continue
			}
			relTarget := patterns.RelativizeToCwd(resolvedFile)
			className, cached := defaultExportCache[relTarget]
			if !cached {
				className = findDefaultExportClassName(resolvedFile)
				defaultExportCache[relTarget] = className
			}
			if className == "" {
				continue
			}
			if targetID, found := fileClassIndex[relTarget][className]; found {
				defaultImportTarget[local] = targetID
			}
		}
	}

	if len(plainImport) == 0 && len(defaultImportTarget) == 0 && len(globalClassByName) == 0 {
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

	// emitInstantiate lands the class-granularity `instantiates` edge plus
	// the constructor `calls` fill-in — shared by the plain/default-import
	// identifier case and the DC.15 `new window.X(...)` global-symbol case
	// below, which resolve targetID differently but emit the same two edges.
	emitInstantiate := func(enclosingFnID, targetID string) {
		if enclosingFnID == "" || targetID == "" {
			return
		}
		eid := fmt.Sprintf("instantiates:%s->%s", enclosingFnID, targetID)
		if !seen[eid] {
			seen[eid] = true
			edges = append(edges, graph.Edge{
				ID: eid, From: enclosingFnID, To: targetID,
				Type: graph.EdgeTypeInstantiates, Confidence: graph.ConfidenceInferred,
				Meta: map[string]string{"count": "1"},
			})
		}
		// Additive: also land a method-granularity `calls` edge on the
		// class's own explicit constructor, the same shape as the Ruby
		// `.new` → `initialize` edge in LinkRubyTypeRelations. No edge for a
		// class with no explicit constructor (constructorByClass has no
		// entry — JS/TS's implicit default takes no edge).
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

	// Also handle new_expression cross-file instantiates.
	var walkNew func(n *sitter.Node, enclosingFnID string)
	walkNew = func(n *sitter.Node, enclosingFnID string) {
		t := n.Type()
		if t == "new_expression" {
			ctor := n.ChildByFieldName("constructor")
			if ctor != nil {
				switch ctor.Type() {
				case "identifier", "type_identifier":
					localName := ctor.Content(src)
					var targetID string
					var found bool
					if exportedName, isImport := plainImport[localName]; isImport {
						targetID, found = classTable[exportedName]
					} else if tid, ok := defaultImportTarget[localName]; ok {
						targetID, found = tid, true
					}
					if found {
						emitInstantiate(enclosingFnID, targetID)
					}
				case "member_expression":
					// DC.15: `new window.X(...)` / `new globalThis.X(...)` /
					// `new self.X(...)` — X resolves through the per-service
					// global-symbol table a same-file `window.X = X`
					// self-registration stamped onto the class node
					// (stampGlobalSymbols in js_variables.go), not through
					// this file's own imports.
					if _, leaf, ok := globalMemberPath(ctor, src); ok {
						if targetID, found := globalClassByName[leaf]; found {
							emitInstantiate(enclosingFnID, targetID)
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
			} else if fnID, ok := constDeclFunctionID(n, svcName, relFile, src); ok {
				newFnID = fnID
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

// jsGlobalRoots is the set of identifiers that name the global object.
// Duplicated from parser/js_variables.go's globalRoots/globalMemberPath: the
// linker package can never import internal/parser (see docs/config-baseurl-
// prefix design note), so this tiny AST helper is reproduced here rather than
// exported across the boundary.
var jsGlobalRoots = map[string]bool{"window": true, "globalThis": true, "self": true}

// globalMemberPath returns (dotted, leaf, ok) for a member_expression whose
// left-most object identifier is in {window, globalThis, self}.
//
//	window.maple.PusherClient  -> ("window.maple.PusherClient", "PusherClient", true)
//	window.PusherClient      -> ("window.PusherClient", "PusherClient", true)
//	foo.bar                  -> ("", "", false)
func globalMemberPath(left *sitter.Node, src []byte) (dotted, leaf string, ok bool) {
	if left.Type() != "member_expression" {
		return "", "", false
	}
	prop := left.ChildByFieldName("property")
	if prop == nil {
		return "", "", false
	}
	leaf = prop.Content(src)

	segs := []string{leaf}
	obj := left.ChildByFieldName("object")
	for obj != nil && obj.Type() == "member_expression" {
		p := obj.ChildByFieldName("property")
		if p == nil {
			return "", "", false
		}
		segs = append(segs, p.Content(src))
		obj = obj.ChildByFieldName("object")
	}
	if obj == nil || obj.Type() != "identifier" {
		return "", "", false
	}
	rootName := obj.Content(src)
	if !jsGlobalRoots[rootName] {
		return "", "", false
	}
	parts := make([]string, 0, len(segs)+1)
	parts = append(parts, rootName)
	for i := len(segs) - 1; i >= 0; i-- {
		parts = append(parts, segs[i])
	}
	return strings.Join(parts, "."), leaf, true
}

func isFunctionLike(t string) bool {
	switch t {
	case "function_declaration", "function_expression", "arrow_function",
		"method_definition", "generator_function_declaration":
		return true
	}
	return false
}

// constDeclFunctionID resolves walkNew's enclosing-scope ID for an anonymous
// arrow_function/function_expression assigned to a plain identifier —
// `const foo = (...) => {...}` / `const foo = async function() {...}` — the
// modern-JS shape isFunctionLike's caller could not name via
// n.ChildByFieldName("name") (that field only exists on a `function foo(){}`
// declaration; an arrow/anonymous function_expression has none of its own).
//
// Confirmed live on gitnexus: `const wikiCommandImpl = async (...) => {...}`
// at module scope contains a same-file `new WikiGenerator(...)`, and every
// `new` in a file wired this way silently produced no instantiates edge —
// walkNew's enclosingFnID stayed whatever the outer scope was (typically ""
// at module level), and emitInstantiate drops on an empty enclosingFnID.
// collectTopLevel (js_variables.go) already mints a function node for
// exactly this shape, keyed on the *declaration statement's* line (not the
// function literal's own line, which can differ when a decorator or leading
// comment sits between `const` and the arrow) — this mirrors that exact
// convention so the ID this produces is the one the parser already minted,
// not a fabricated one that would dangle.
func constDeclFunctionID(n *sitter.Node, svcName, relFile string, src []byte) (string, bool) {
	decl := n.Parent()
	if decl == nil || decl.Type() != "variable_declarator" || decl.ChildByFieldName("value") != n {
		return "", false
	}
	nameNode := decl.ChildByFieldName("name")
	if nameNode == nil || nameNode.Type() != "identifier" {
		return "", false
	}
	stmt := decl.Parent()
	if stmt == nil {
		return "", false
	}
	name := nameNode.Content(src)
	line := int(stmt.StartPoint().Row) + 1
	return fmt.Sprintf("%s:%s:function:%s:%d", svcName, relFile, name, line), true
}

// findDefaultExportClassName reads file and returns the class name bound to
// its `export default`, or "" when the file has no default export or the
// default export isn't a class. Handles both `export default class X {...}`
// (declaration field) and `export default X;` (value field, referencing a
// class already declared earlier in the same file — the local name carries
// the resolution, matched later against fileClassIndex). `default` is an
// anonymous grammar token (not a field on export_statement), so its presence
// must be checked positionally among the unnamed children.
func findDefaultExportClassName(file string) string {
	src, root, _, ok := jsParse(file)
	if !ok {
		return ""
	}

	isDefaultExport := func(n *sitter.Node) bool {
		for i := 0; i < int(n.ChildCount()); i++ {
			if n.Child(i).Content(src) == "default" {
				return true
			}
		}
		return false
	}

	var found string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != "" {
			return
		}
		if n.Type() == "export_statement" && isDefaultExport(n) {
			if decl := n.ChildByFieldName("declaration"); decl != nil {
				if decl.Type() == "class_declaration" {
					if nameNode := decl.ChildByFieldName("name"); nameNode != nil {
						found = nameNode.Content(src)
						return
					}
				}
			} else if val := n.ChildByFieldName("value"); val != nil && val.Type() == "identifier" {
				found = val.Content(src)
				return
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return found
}
