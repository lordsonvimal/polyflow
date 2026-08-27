package parser

import (
	"context"
	"fmt"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	jssitter "github.com/smacker/go-tree-sitter/javascript"
	tsxsitter "github.com/smacker/go-tree-sitter/typescript/tsx"
	tssitter "github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// extractJSVariables is the structural (tree-sitter) variable-tracking pass
// for JavaScript/TypeScript. Unlike the Go SSA pass it has no type checker,
// so everything it emits carries reduced confidence: reads/writes are
// "inferred" (shadowing is approximated lexically), closure captures are
// "partial". Tracked variables are module-scope declarations and locals
// captured by nested functions; function-local variables stay out of the
// graph.
//
// The fourth return value carries the jQuery event registrations this pass read
// (Tier K.4). They are not graph objects yet: the element a selector names lives
// in another file, so the join is the linker's, and javascript.go stamps them
// onto the pattern matcher's dom_target nodes on the way out.
func extractJSVariables(file, service, langTag, grammarLang string, src []byte) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, []jqListener) {
	var lang *sitter.Language
	switch grammarLang {
	case "typescript":
		lang = tssitter.GetLanguage()
	case "tsx":
		lang = tsxsitter.GetLanguage()
	default:
		lang = jssitter.GetLanguage()
	}
	p := sitter.NewParser()
	p.SetLanguage(lang)
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil, nil, nil, nil
	}
	defer tree.Close()

	ex := &jsExtractor{
		file: file, service: service, langTag: langTag, src: src,
		moduleVars: map[string]*jsVar{},
		fnDecls:    map[string]int{},
		localFns:   map[string]int{},
		classNodes: map[string]string{},
		signals:    map[string]string{},
		nodeSeen:   map[string]bool{},
		edgeSeen:   map[string]bool{},
	}
	root := tree.RootNode()
	ex.preCollectClasses(root)
	ex.collectTopLevel(root)
	ex.collectLocalFns(root)
	ex.walk(root, []*jsScope{ex.moduleScope()})
	ex.extractTypeUses(root)
	ex.stampGlobalSymbols(root)

	sort.Slice(ex.nodes, func(i, j int) bool { return ex.nodes[i].ID < ex.nodes[j].ID })
	sort.Slice(ex.edges, func(i, j int) bool { return ex.edges[i].ID < ex.edges[j].ID })
	return ex.nodes, ex.edges, ex.unresolved, ex.jqListeners
}

type jsVar struct {
	nodeID   string
	dataType string
	isSetter bool // Solid signal setter (const [x, setX] = createSignal(...))
	reactive bool // Solid reactive accessor (createSignal/createResource/createMemo)
}

// jsScope is one lexical function frame (or the module frame at index 0).
type jsScope struct {
	fnName string // attribution: nearest named enclosing function
	fnLine int
	locals map[string]int // name → declaration line (function scopes only)
}

type jsExtractor struct {
	file, service, langTag string
	src                    []byte

	moduleVars map[string]*jsVar
	fnDecls    map[string]int // top-level function name → line
	// localFns maps every self-attributable function name (nested function
	// declarations + `const handler = () => …` locals, at any depth) to its decl
	// line — the same line the walk mints the function node at. It lets Y.7
	// resolve JSX/addEventListener handlers that are component-local consts (the
	// dominant React/Solid idiom) to their function node. Cross-scope name
	// collisions are approximated (last-wins), consistent with this pass's
	// reduced-confidence, no-type-checker contract.
	localFns   map[string]int
	classNodes map[string]string // class/interface name → nodeID (same-file)
	// signals maps a Solid reactive accessor name (createSignal/createResource/
	// createMemo binding) to its variable node ID, so a JSX interpolation reading
	// that accessor can source a signal→element dom_write (Y.6). Both module- and
	// function-local accessors register here; DFS visits a declaration before the
	// JSX that reads it, so the current binding is resolvable by name.
	signals map[string]string

	// jqHandlers claims an inline jQuery handler's function body for the
	// synthetic handler node minted at its registration site (Tier K.4), keyed
	// by the handler's start byte. Without it every listener body attributes to
	// the file's single (module) node.
	jqHandlers map[uint32]jsScope
	// jqListeners are the resolved jQuery registrations, handed back to
	// javascript.go to stamp onto the matcher's dom_target nodes.
	jqListeners []jqListener

	nodes      []graph.Node
	edges      []graph.Edge
	unresolved []graph.UnresolvedRef
	nodeSeen   map[string]bool
	edgeSeen   map[string]bool
}

// moduleScope builds the root frame, pre-populated with the module-level
// names collected by collectTopLevel so identifier resolution can reach them.
func (ex *jsExtractor) moduleScope() *jsScope {
	s := &jsScope{locals: map[string]int{}}
	for name := range ex.moduleVars {
		s.locals[name] = 0
	}
	for name, ln := range ex.fnDecls {
		s.locals[name] = ln
	}
	return s
}

func (ex *jsExtractor) addNode(n graph.Node) {
	if !ex.nodeSeen[n.ID] {
		ex.nodeSeen[n.ID] = true
		ex.nodes = append(ex.nodes, n)
	}
}

func (ex *jsExtractor) addEdge(typ graph.EdgeType, from, to, confidence string, meta map[string]string) {
	id := fmt.Sprintf("jsvar:%s:%s->%s", typ, from, to)
	if ex.edgeSeen[id] {
		return
	}
	ex.edgeSeen[id] = true
	ex.edges = append(ex.edges, graph.Edge{
		ID: id, From: from, To: to, Type: typ, Confidence: confidence, Meta: meta,
	})
}

func (ex *jsExtractor) varNodeID(name string, line int) string {
	return fmt.Sprintf("%s:%s:variable:%s:%d", ex.service, ex.file, name, line)
}

func (ex *jsExtractor) fnNodeID(name string, line int) string {
	return fmt.Sprintf("%s:%s:function:%s:%d", ex.service, ex.file, name, line)
}

func tsLine(n *sitter.Node) int { return int(n.StartPoint().Row) + 1 }

func tsEndLine(n *sitter.Node) int { return int(n.EndPoint().Row) + 1 }

// isAccessorMethod reports whether a method_definition is a `get`/`set`
// accessor (`get value() {...}`) — invoked by property read/write syntax
// (`obj.value`, `obj.value = x`), never by a call expression. No pattern in
// the graph can ever produce a `calls` edge to one (there is no `.value()`
// call syntax), so without a distinct signal it reads as permanently
// zero-caller dead code regardless of how many times its property is
// actually accessed — the same "invoked without a literal call site" shape
// as GORM's reflect-dispatched TableName or a JS object-literal callback
// value, stamped so indexer.go's classifyRoot can route it to root_kind=
// callback instead of unreachable.
func isAccessorMethod(node *sitter.Node) bool {
	if node.Type() != "method_definition" {
		return false
	}
	for i := 0; i < int(node.ChildCount()); i++ {
		c := node.Child(i)
		switch c.Type() {
		case "get", "set":
			return true
		case "property_identifier", "computed_property_name", "string", "number", "private_property_identifier":
			return false // reached the name; get/set would have preceded it
		}
	}
	return false
}

// isFunctionNode reports whether the AST node opens a new function scope.
func isFunctionNode(t string) bool {
	switch t {
	case "function_declaration", "function_expression", "function", "arrow_function",
		"method_definition", "generator_function_declaration", "generator_function":
		return true
	}
	return false
}

// literalType maps an initializer node to a rough runtime type.
func literalType(t string) string {
	switch t {
	case "string", "template_string":
		return "string"
	case "number":
		return "number"
	case "true", "false":
		return "boolean"
	case "array":
		return "array"
	case "object":
		return "object"
	case "arrow_function", "function_expression", "function":
		return "function"
	case "new_expression":
		return "object"
	}
	return ""
}

// collectTopLevel finds module-scope declarations: variables, functions,
// classes. Export wrappers are unwrapped.
func (ex *jsExtractor) collectTopLevel(root *sitter.Node) {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		stmt := root.NamedChild(i)
		t := stmt.Type()
		if t == "export_statement" {
			if decl := stmt.ChildByFieldName("declaration"); decl != nil {
				stmt = decl
				t = stmt.Type()
			}
		}
		switch t {
		case "lexical_declaration", "variable_declaration":
			kind := "var"
			if first := stmt.Child(0); first != nil {
				kind = first.Content(ex.src) // const | let | var
			}
			for j := 0; j < int(stmt.NamedChildCount()); j++ {
				decl := stmt.NamedChild(j)
				if decl.Type() != "variable_declarator" {
					continue
				}
				nameNode := decl.ChildByFieldName("name")
				if nameNode == nil {
					continue
				}
				if nameNode.Type() != "identifier" {
					// Destructuring: const [notification, setNotification] =
					// createSignal(...) — register every bound identifier as a
					// module variable so signal reads/setter calls resolve.
					ex.collectDestructured(decl, nameNode, kind)
					continue
				}
				name := nameNode.Content(ex.src)
				value := decl.ChildByFieldName("value")

				// Arrow/function initializers are functions, not variables.
				if value != nil && isFunctionNode(value.Type()) {
					ex.fnDecls[name] = tsLine(stmt)
					ex.addNode(graph.Node{
						ID: ex.fnNodeID(name, tsLine(stmt)), Type: graph.NodeTypeFunction,
						Label: name, Service: ex.service, File: ex.file,
						Line: tsLine(stmt), EndLine: tsEndLine(stmt), Language: ex.langTag,
					})
					continue
				}

				dataType := ""
				if ta := decl.ChildByFieldName("type"); ta != nil {
					dataType = strings.TrimPrefix(ta.Content(ex.src), ": ")
					dataType = strings.TrimPrefix(dataType, ":")
					dataType = strings.TrimSpace(dataType)
				} else if value != nil {
					dataType = literalType(value.Type())
				}
				id := ex.varNodeID(name, tsLine(stmt))
				ex.moduleVars[name] = &jsVar{nodeID: id, dataType: dataType}
				ex.addNode(graph.Node{
					ID: id, Type: graph.NodeTypeVariable, Label: name,
					Service: ex.service, File: ex.file, Line: tsLine(stmt), EndLine: tsEndLine(stmt), Language: ex.langTag,
					Meta: map[string]string{
						"data_type": dataType, "kind": kind,
						"scope": "module", "mutable": fmt.Sprintf("%t", kind != "const"),
					},
				})
			}
		case "function_declaration", "generator_function_declaration":
			if nameNode := stmt.ChildByFieldName("name"); nameNode != nil {
				ex.fnDecls[nameNode.Content(ex.src)] = tsLine(stmt)
			}
		case "class_declaration":
			ex.collectClass(stmt)
			if nameNode := stmt.ChildByFieldName("name"); nameNode != nil {
				classNodeID := fmt.Sprintf("%s:%s:class:%s:%d", ex.service, ex.file, nameNode.Content(ex.src), tsLine(stmt))
				ex.processClassHeritage(stmt, classNodeID)
			}
		case "interface_declaration":
			ex.collectInterface(stmt)
		}
	}
}

// collectLocalFns scans the whole tree for self-attributable functions — nested
// `function foo(){}` declarations and `const foo = () => …` / `const foo =
// function(){}` bindings at any depth — recording name→decl-line so Y.7 handler
// resolution reaches component-local handlers, not just module-level ones. The
// line matches the walk's minted function-node line so the resolved ID exists.
func (ex *jsExtractor) collectLocalFns(n *sitter.Node) {
	switch n.Type() {
	case "function_declaration", "generator_function_declaration":
		if name := n.ChildByFieldName("name"); name != nil {
			ex.localFns[name.Content(ex.src)] = tsLine(n)
		}
	case "variable_declarator":
		if name := n.ChildByFieldName("name"); name != nil && name.Type() == "identifier" {
			if val := n.ChildByFieldName("value"); val != nil && isFunctionNode(val.Type()) {
				ex.localFns[name.Content(ex.src)] = tsLine(declStatement(n))
			}
		}
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		ex.collectLocalFns(n.NamedChild(i))
	}
}

// resolveHandlerFn resolves a bare handler identifier to the line of its
// function node, preferring a module-level declaration over a local one.
func (ex *jsExtractor) resolveHandlerFn(name string) (int, bool) {
	if line, ok := ex.fnDecls[name]; ok {
		return line, true
	}
	line, ok := ex.localFns[name]
	return line, ok
}

// collectDestructured registers every identifier bound by a destructuring
// declarator (array_pattern / object_pattern) as a module variable. The
// initializer callee is recorded so consumers can see where the binding came
// from (e.g. init=createSignal for Solid signals).
func (ex *jsExtractor) collectDestructured(decl, pattern *sitter.Node, kind string) {
	init := ""
	if value := decl.ChildByFieldName("value"); value != nil && value.Type() == "call_expression" {
		if fn := value.ChildByFieldName("function"); fn != nil {
			init = fn.Content(ex.src)
		}
	}
	prim, resourceFn := reactiveInit(decl, ex.src)
	accessorTaken := false
	collectPatternBindings(pattern, ex.src, func(name string, _ int) {
		line := tsLine(declStatement(decl))
		id := ex.varNodeID(name, line)
		setter := isSignalSetter(init, name)
		ex.moduleVars[name] = &jsVar{nodeID: id, isSetter: setter}
		meta := map[string]string{
			"kind": kind, "scope": "module", "destructured": "true",
			"mutable": fmt.Sprintf("%t", kind != "const"),
		}
		if init != "" {
			meta["init"] = init
		}
		if setter {
			meta["setter"] = "true"
		}
		// Y.6: the first non-setter binding of a reactive primitive is the
		// accessor — it drives DOM writes and (for createResource) carries the
		// loader fn the linker joins to the fetch's http_client node.
		if prim != "" && !setter && !accessorTaken {
			accessorTaken = true
			ex.moduleVars[name].reactive = true
			ex.signals[name] = id
			meta["reactive"] = reactiveKind(prim)
			if resourceFn != "" {
				meta["resource_fn"] = resourceFn
			}
		}
		ex.addNode(graph.Node{
			ID: id, Type: graph.NodeTypeVariable, Label: name,
			Service: ex.service, File: ex.file, Line: line, EndLine: line, Language: ex.langTag,
			Meta: meta,
		})
	})
}

// isSignalSetter reports whether a destructured binding is a Solid signal
// setter: const [x, setX] = createSignal(...). Calling it writes the signal.
func isSignalSetter(init, name string) bool {
	if init != "createSignal" {
		return false
	}
	rest, ok := strings.CutPrefix(name, "set")
	return ok && rest != "" && rest[0] >= 'A' && rest[0] <= 'Z'
}

// reactiveInit inspects a destructuring declarator's initializer and reports the
// Solid reactive primitive it calls (createSignal/createResource/createMemo) and,
// for createResource, the first-argument identifier — the loader fn whose fetch
// feeds the resource. Empty prim means the binding is not reactive.
func reactiveInit(decl *sitter.Node, src []byte) (prim, resourceFn string) {
	value := decl.ChildByFieldName("value")
	if value == nil || value.Type() != "call_expression" {
		return "", ""
	}
	fn := value.ChildByFieldName("function")
	if fn == nil {
		return "", ""
	}
	switch fn.Content(src) {
	case "createSignal", "createResource", "createMemo":
		prim = fn.Content(src)
	default:
		return "", ""
	}
	if prim == "createResource" {
		if args := value.ChildByFieldName("arguments"); args != nil {
			for i := 0; i < int(args.NamedChildCount()); i++ {
				if a := args.NamedChild(i); a.Type() == "identifier" {
					resourceFn = a.Content(src)
					break
				}
			}
		}
	}
	return prim, resourceFn
}

// reactiveKind maps a reactive primitive callee to the meta.reactive tag.
func reactiveKind(prim string) string {
	switch prim {
	case "createResource":
		return "resource"
	case "createMemo":
		return "memo"
	default:
		return "signal"
	}
}

// collectPatternBindings visits the identifiers bound by a destructuring
// pattern: array elements, object shorthand properties, pair values, rest
// elements, and defaulted bindings. Object property *keys* are not bindings
// and are skipped.
func collectPatternBindings(n *sitter.Node, src []byte, visit func(name string, line int)) {
	switch n.Type() {
	case "identifier", "shorthand_property_identifier_pattern":
		visit(n.Content(src), int(n.StartPoint().Row)+1)
		return
	case "pair_pattern":
		if v := n.ChildByFieldName("value"); v != nil {
			collectPatternBindings(v, src, visit)
		}
		return
	case "assignment_pattern":
		if l := n.ChildByFieldName("left"); l != nil {
			collectPatternBindings(l, src, visit)
		}
		return
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		collectPatternBindings(n.NamedChild(i), src, visit)
	}
}

// extractTypeUses emits same-file `uses_type` edges from a declaration to the
// TypeScript interface/class types it references in annotations, interface
// members, class fields, and generic arguments — the JS analog of the Go struct
// `uses_type` pass. It resolves the referenced name against same-file
// interfaces/classes (ex.classNodes); cross-file references are left for
// LinkJSTypeRelations. This is what connects otherwise-dangling frontend DTO
// types (a type declared and used but never instantiated) into the graph.
func (ex *jsExtractor) extractTypeUses(n *sitter.Node) {
	if n.Type() == "type_identifier" && isTypeReferenceContext(n) {
		if toID, ok := ex.classNodes[n.Content(ex.src)]; ok {
			if fromID := ex.enclosingDeclID(n); fromID != "" && fromID != toID {
				ex.addEdge(graph.EdgeTypeUsesType, fromID, toID,
					graph.ConfidenceStatic, map[string]string{"via": "type_ref"})
			}
		}
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		ex.extractTypeUses(n.NamedChild(i))
	}
}

// isTypeReferenceContext reports whether a type_identifier is a *use* of a type
// rather than a declaration name or a heritage target (which are already
// captured as the type node itself / inherits / implements edges).
func isTypeReferenceContext(n *sitter.Node) bool {
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

// enclosingDeclID resolves the graph node ID of the nearest declaration that
// owns a type reference: an interface, class, function, or variable (including
// a destructured signal accessor). Returns "" when no backing node exists.
func (ex *jsExtractor) enclosingDeclID(n *sitter.Node) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		switch p.Type() {
		case "interface_declaration", "class_declaration":
			if nm := p.ChildByFieldName("name"); nm != nil {
				if id, ok := ex.classNodes[nm.Content(ex.src)]; ok {
					return id
				}
			}
		case "function_declaration", "generator_function_declaration":
			if nm := p.ChildByFieldName("name"); nm != nil {
				if id, ok := ex.resolveFnID(nm.Content(ex.src)); ok {
					return id
				}
			}
		case "variable_declarator":
			nm := p.ChildByFieldName("name")
			if nm == nil {
				continue
			}
			switch nm.Type() {
			case "identifier":
				name := nm.Content(ex.src)
				if v, ok := ex.moduleVars[name]; ok {
					return v.nodeID
				}
				if id, ok := ex.resolveFnID(name); ok {
					return id
				}
			case "array_pattern", "object_pattern":
				// Destructured signal accessor (const [x] = createSignal<T>()):
				// attribute to the accessor's variable node.
				var id string
				collectPatternBindings(nm, ex.src, func(bn string, _ int) {
					if id != "" {
						return
					}
					if v, ok := ex.moduleVars[bn]; ok {
						id = v.nodeID
					} else if sid, ok := ex.signals[bn]; ok {
						id = sid
					}
				})
				if id != "" {
					return id
				}
			}
		}
	}
	return ""
}

// resolveFnID resolves a function name to its node ID via the module-level then
// local function registries.
func (ex *jsExtractor) resolveFnID(name string) (string, bool) {
	if line, ok := ex.fnDecls[name]; ok {
		return ex.fnNodeID(name, line), true
	}
	if line, ok := ex.localFns[name]; ok {
		return ex.fnNodeID(name, line), true
	}
	return "", false
}

func (ex *jsExtractor) collectClass(stmt *sitter.Node) {
	nameNode := stmt.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(ex.src)
	var methods, fields []string
	if body := stmt.ChildByFieldName("body"); body != nil {
		for j := 0; j < int(body.NamedChildCount()); j++ {
			m := body.NamedChild(j)
			switch m.Type() {
			case "method_definition":
				if mn := m.ChildByFieldName("name"); mn != nil {
					methods = append(methods, mn.Content(ex.src))
				}
			case "public_field_definition", "field_definition":
				if fn := m.ChildByFieldName("property"); fn != nil {
					fields = append(fields, fn.Content(ex.src))
				}
			}
		}
	}
	ex.addNode(graph.Node{
		ID:   fmt.Sprintf("%s:%s:class:%s:%d", ex.service, ex.file, name, tsLine(stmt)),
		Type: graph.NodeTypeClass, Label: name,
		Service: ex.service, File: ex.file, Line: tsLine(stmt), EndLine: tsEndLine(stmt), Language: ex.langTag,
		Meta: map[string]string{
			"methods": strings.Join(methods, ","),
			"fields":  strings.Join(fields, ","),
		},
	})
}

// preCollectClasses records all top-level class and interface names into
// ex.classNodes so that processClassHeritage can resolve same-file parents
// even when the parent class is declared after the child.
func (ex *jsExtractor) preCollectClasses(root *sitter.Node) {
	for i := 0; i < int(root.NamedChildCount()); i++ {
		stmt := root.NamedChild(i)
		if stmt.Type() == "export_statement" {
			if decl := stmt.ChildByFieldName("declaration"); decl != nil {
				stmt = decl
			}
		}
		switch stmt.Type() {
		case "class_declaration":
			if n := stmt.ChildByFieldName("name"); n != nil {
				name := n.Content(ex.src)
				id := fmt.Sprintf("%s:%s:class:%s:%d", ex.service, ex.file, name, tsLine(stmt))
				ex.classNodes[name] = id
			}
		case "interface_declaration":
			if n := stmt.ChildByFieldName("name"); n != nil {
				name := n.Content(ex.src)
				id := fmt.Sprintf("%s:%s:interface:%s:%d", ex.service, ex.file, name, tsLine(stmt))
				ex.classNodes[name] = id
			}
		}
	}
}

// processClassHeritage reads the class_heritage node of a class_declaration
// and emits inherits/implements edges for same-file parents. Cross-file
// parents are resolved by LinkJSTypeRelations; expression superclasses go to
// the inherits_unresolved ledger.
//
// The JS and TS grammars differ:
//   - JavaScript: class_heritage has a `value` field directly (the superclass expr).
//   - TypeScript: class_heritage has named children extends_clause / implements_clause.
func (ex *jsExtractor) processClassHeritage(stmt *sitter.Node, classID string) {
	// Find class_heritage named child.
	var heritage *sitter.Node
	for i := 0; i < int(stmt.NamedChildCount()); i++ {
		c := stmt.NamedChild(i)
		if c.Type() == "class_heritage" {
			heritage = c
			break
		}
	}
	if heritage == nil {
		return
	}

	// Try TypeScript grammar first: look for extends_clause / implements_clause children.
	foundTSClauses := false
	for i := 0; i < int(heritage.NamedChildCount()); i++ {
		clause := heritage.NamedChild(i)
		switch clause.Type() {
		case "extends_clause":
			foundTSClauses = true
			// TypeScript extends_clause: `value` field contains the parent.
			val := clause.ChildByFieldName("value")
			if val == nil {
				// No value field — check first named child (some grammar versions).
				if clause.NamedChildCount() > 0 {
					val = clause.NamedChild(0)
				}
			}
			if val == nil {
				continue
			}
			ex.resolveExtendsValue(classID, val)
		case "implements_clause":
			foundTSClauses = true
			// Each named child is a type_identifier (or generic_type etc.).
			for j := 0; j < int(clause.NamedChildCount()); j++ {
				ti := clause.NamedChild(j)
				ex.resolveImplementsType(classID, ti)
			}
		}
	}

	if !foundTSClauses {
		// JavaScript grammar: the parent expression is a direct named child of
		// class_heritage (no extends_clause wrapper, no value field).
		if heritage.NamedChildCount() > 0 {
			ex.resolveExtendsValue(classID, heritage.NamedChild(0))
		}
	}
}

// resolveExtendsValue processes the value of an extends clause: emits an
// inherits edge when the parent resolves same-file, or ledger entry otherwise.
func (ex *jsExtractor) resolveExtendsValue(classID string, val *sitter.Node) {
	switch val.Type() {
	case "identifier", "type_identifier":
		parentName := val.Content(ex.src)
		if parentID, ok := ex.classNodes[parentName]; ok {
			ex.addEdge(graph.EdgeTypeInherits, classID, parentID, graph.ConfidenceStatic,
				map[string]string{"via": "extends"})
		} else {
			ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
				Service: ex.service, File: ex.file,
				Line: tsLine(val), Name: parentName, Kind: "inherits_unresolved",
			})
		}
	default:
		// Expression superclass (e.g. mixin(Base)) — never guessed.
		ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
			Service: ex.service, File: ex.file,
			Line: tsLine(val), Name: val.Content(ex.src), Kind: "inherits_unresolved",
		})
	}
}

// resolveImplementsType processes one type in an implements clause.
func (ex *jsExtractor) resolveImplementsType(classID string, ti *sitter.Node) {
	name := ""
	switch ti.Type() {
	case "type_identifier":
		name = ti.Content(ex.src)
	case "generic_type":
		if base := ti.ChildByFieldName("name"); base != nil {
			name = base.Content(ex.src)
		}
	}
	if name == "" {
		return
	}
	if ifaceID, ok := ex.classNodes[name]; ok {
		ex.addEdge(graph.EdgeTypeImplements, classID, ifaceID, graph.ConfidenceStatic,
			map[string]string{"nominal": "true"})
	} else {
		ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
			Service: ex.service, File: ex.file,
			Line: tsLine(ti), Name: name, Kind: "implements_unresolved",
		})
	}
}

// collectInterface emits a NodeTypeInterface node for a TypeScript
// interface_declaration and inherits edges for extends_type_clause parents.
func (ex *jsExtractor) collectInterface(stmt *sitter.Node) {
	nameNode := stmt.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(ex.src)
	nodeID := fmt.Sprintf("%s:%s:interface:%s:%d", ex.service, ex.file, name, tsLine(stmt))

	// Collect method signatures for the methods meta field.
	var methods []string
	if body := stmt.ChildByFieldName("body"); body != nil {
		for i := 0; i < int(body.NamedChildCount()); i++ {
			m := body.NamedChild(i)
			switch m.Type() {
			case "property_signature", "method_signature", "call_signature",
				"construct_signature", "index_signature":
				if mn := m.ChildByFieldName("name"); mn != nil {
					methods = append(methods, mn.Content(ex.src))
				}
			}
		}
	}
	ex.addNode(graph.Node{
		ID: nodeID, Type: graph.NodeTypeInterface, Label: name,
		Service: ex.service, File: ex.file, Line: tsLine(stmt), EndLine: tsEndLine(stmt), Language: ex.langTag,
		Meta: map[string]string{"methods": strings.Join(methods, ",")},
	})

	// Interface extends: inherits edges between interface nodes.
	for i := 0; i < int(stmt.NamedChildCount()); i++ {
		c := stmt.NamedChild(i)
		if c.Type() != "extends_type_clause" {
			continue
		}
		for j := 0; j < int(c.NamedChildCount()); j++ {
			parent := c.NamedChild(j)
			parentName := ""
			switch parent.Type() {
			case "type_identifier":
				parentName = parent.Content(ex.src)
			case "generic_type":
				if base := parent.ChildByFieldName("name"); base != nil {
					parentName = base.Content(ex.src)
				}
			}
			if parentName == "" {
				continue
			}
			if parentID, ok := ex.classNodes[parentName]; ok {
				ex.addEdge(graph.EdgeTypeInherits, nodeID, parentID, graph.ConfidenceStatic,
					map[string]string{"via": "extends"})
			} else {
				ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
					Service: ex.service, File: ex.file,
					Line: tsLine(parent), Name: parentName, Kind: "inherits_unresolved",
				})
			}
		}
	}
}

// attribution returns the graph node ID of the nearest named enclosing
// function (or the module variable owning the frame for reactive-primitive
// initializers like createMemo), or "" at module level.
func attribution(scopes []*jsScope, ex *jsExtractor) string {
	for i := len(scopes) - 1; i >= 1; i-- {
		if scopes[i].fnName != "" {
			return ex.fnNodeID(scopes[i].fnName, scopes[i].fnLine)
		}
	}
	return ""
}

// moduleNodeID lazily materialises the synthetic per-file module node (same
// ID format the pattern matcher uses, so the store deduplicates) and returns
// its ID. It attributes accesses in module-level statements that belong to no
// declarator — top-level side effects that run on import.
func (ex *jsExtractor) moduleNodeID() string {
	id := fmt.Sprintf("%s:%s:function:(module):0", ex.service, ex.file)
	ex.addNode(graph.Node{
		ID: id, Type: graph.NodeTypeFunction, Label: "(module)",
		Service: ex.service, File: ex.file, Line: 0, Language: ex.langTag,
		Meta: map[string]string{"scope": "module"},
	})
	return id
}

// moduleAttr resolves the attribution for an access with no named enclosing
// function: the module-level declarator whose initializer contains the node
// (const filtered = createMemo(() => …) → the `filtered` variable node),
// falling back to the synthetic module node. This is what connects reactive
// derivations to the state they read.
func (ex *jsExtractor) moduleAttr(node *sitter.Node) string {
	for p := node.Parent(); p != nil; p = p.Parent() {
		if p.Type() != "variable_declarator" {
			continue
		}
		nameNode := p.ChildByFieldName("name")
		if nameNode == nil || nameNode.Type() != "identifier" {
			continue
		}
		name := nameNode.Content(ex.src)
		if v, ok := ex.moduleVars[name]; ok {
			return v.nodeID
		}
		if line, ok := ex.fnDecls[name]; ok {
			return ex.fnNodeID(name, line)
		}
	}
	return ex.moduleNodeID()
}

// resolve finds which frame declares name: -1 unknown, 0 module, >0 function.
func resolve(scopes []*jsScope, name string) int {
	for i := len(scopes) - 1; i >= 1; i-- {
		if _, ok := scopes[i].locals[name]; ok {
			return i
		}
	}
	if _, ok := scopes[0].locals[name]; ok {
		return 0
	}
	return -1
}

func (ex *jsExtractor) walk(node *sitter.Node, scopes []*jsScope) {
	t := node.Type()

	if isFunctionNode(t) && node.Parent() != nil {
		frame := &jsScope{locals: map[string]int{}}
		// Named function declarations attribute to themselves; anonymous
		// functions (arrow, callbacks) inherit the parent attribution unless
		// they are a top-level `const name = () => …` initializer.
		selfAttributed := false
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			frame.fnName, frame.fnLine = nameNode.Content(ex.src), tsLine(node)
			selfAttributed = true
		} else if decl := node.Parent(); decl != nil && decl.Type() == "variable_declarator" {
			if dn := decl.ChildByFieldName("name"); dn != nil && dn.Type() == "identifier" {
				frame.fnName = dn.Content(ex.src)
				frame.fnLine = tsLine(declStatement(decl))
				selfAttributed = true
			}
		} else if h, claimed := ex.jqHandlers[node.StartByte()]; claimed {
			// An inline jQuery handler: anonymous in the source, but K.4 gave it
			// a node named after the element and event it serves, so its body
			// attributes there instead of to (module).
			frame.fnName, frame.fnLine = h.fnName, h.fnLine
			selfAttributed = true
		}
		// Materialise the function node when this frame attributes to the
		// function node itself. The pattern matcher only emits nodes for
		// top-level function_declarations and `const = arrow` initializers,
		// so named function *expressions* (`return function enqueue(fn){…}`)
		// and `const = function(){}` had no backing node — leaving the `from`
		// endpoint of any capture/read/write edge dangling and failing the
		// edges."from" FK. addNode dedups, so this is a no-op for the cases
		// the matcher already covers.
		if selfAttributed {
			n := graph.Node{
				ID:      ex.fnNodeID(frame.fnName, frame.fnLine),
				Type:    graph.NodeTypeFunction,
				Label:   frame.fnName,
				Service: ex.service, File: ex.file, Line: frame.fnLine, EndLine: frame.fnLine,
				Language: ex.langTag,
			}
			if isAccessorMethod(node) {
				n.Meta = map[string]string{"js_accessor": "true"}
			}
			ex.addNode(n)
		}
		if frame.fnName == "" {
			// inherit attribution from nearest named ancestor frame
			for i := len(scopes) - 1; i >= 1; i-- {
				if scopes[i].fnName != "" {
					frame.fnName, frame.fnLine = scopes[i].fnName, scopes[i].fnLine
					break
				}
			}
		}
		// Parameters shadow outer names.
		if params := node.ChildByFieldName("parameters"); params != nil {
			collectIdentifiers(params, ex.src, func(name string, ln int) {
				frame.locals[name] = ln
			})
		} else if param := node.ChildByFieldName("parameter"); param != nil {
			collectIdentifiers(param, ex.src, func(name string, ln int) {
				frame.locals[name] = ln
			})
		}
		scopes = append(scopes, frame)
	}

	switch t {
	case "lexical_declaration", "variable_declaration":
		// Local declarations register as shadows in the current function
		// frame (module-level ones were already collected).
		if len(scopes) > 1 {
			cur := scopes[len(scopes)-1]
			for j := 0; j < int(node.NamedChildCount()); j++ {
				decl := node.NamedChild(j)
				if decl.Type() != "variable_declarator" {
					continue
				}
				nameNode := decl.ChildByFieldName("name")
				if nameNode == nil {
					continue
				}
				if nameNode.Type() == "identifier" {
					cur.locals[nameNode.Content(ex.src)] = tsLine(decl)
				} else {
					// Destructured locals (const [sel, setSel] = createSignal(...))
					// shadow outer names and are capturable by nested closures.
					prim, resourceFn := reactiveInit(decl, ex.src)
					init := ""
					if value := decl.ChildByFieldName("value"); value != nil && value.Type() == "call_expression" {
						if fn := value.ChildByFieldName("function"); fn != nil {
							init = fn.Content(ex.src)
						}
					}
					accessorTaken := false
					collectPatternBindings(nameNode, ex.src, func(name string, _ int) {
						cur.locals[name] = tsLine(decl)
						// Y.6: a function-local reactive accessor (the createSignal/
						// createResource binding inside a component) gets a variable
						// node so it can source signal→element dom_write. Only the
						// accessor is materialised — ordinary locals stay out of the
						// graph — because a signal's reach extends beyond one function.
						if prim != "" && !isSignalSetter(init, name) && !accessorTaken {
							accessorTaken = true
							line := tsLine(declStatement(decl))
							id := ex.varNodeID(name, line)
							meta := map[string]string{
								"kind": "var", "scope": "local", "reactive": reactiveKind(prim),
							}
							if resourceFn != "" {
								meta["resource_fn"] = resourceFn
							}
							ex.addNode(graph.Node{
								ID: id, Type: graph.NodeTypeVariable, Label: name,
								Service: ex.service, File: ex.file, Line: line, EndLine: line, Language: ex.langTag,
								Meta: meta,
							})
							ex.signals[name] = id
						}
					})
				}
			}
		}
	case "assignment_expression", "augmented_assignment_expression":
		if left := node.ChildByFieldName("left"); left != nil && left.Type() == "identifier" {
			ex.handleWrite(left, left.Content(ex.src), scopes)
		}
	case "identifier":
		ex.handleRead(node, scopes)
	case "call_expression":
		ex.handleCall(node, scopes)
		ex.handleResponseConsume(node, scopes)
		ex.handleAddEventListener(node)
		ex.handleJQueryListener(node)
	case "new_expression":
		ex.handleNew(node, scopes)
	case "jsx_expression":
		ex.handleJSXWrite(node)
	case "jsx_opening_element", "jsx_self_closing_element":
		ex.handleJSXEvent(node)
	}

	for i := 0; i < int(node.NamedChildCount()); i++ {
		ex.walk(node.NamedChild(i), scopes)
	}
}

// declStatement climbs from a variable_declarator to its declaration
// statement so line numbers match collectTopLevel's.
func declStatement(decl *sitter.Node) *sitter.Node {
	if p := decl.Parent(); p != nil {
		return p
	}
	return decl
}

func (ex *jsExtractor) handleWrite(node *sitter.Node, name string, scopes []*jsScope) {
	from := attribution(scopes, ex)
	frame := resolve(scopes, name)
	switch {
	case frame == 0: // module variable
		v := ex.moduleVars[name]
		if v == nil {
			return
		}
		if from == "" {
			from = ex.moduleAttr(node)
		}
		if from == "" || from == v.nodeID || from == ex.fnNodeID(name, ex.fnDecls[name]) {
			return
		}
		ex.addEdge(graph.EdgeTypeWrites, from, v.nodeID, graph.ConfidenceInferred,
			map[string]string{"op": "assign"})
	case frame > 0 && frame < len(scopes)-1: // captured outer local
		ex.captureEdge(node, name, scopes, frame, true)
	}
}

// handleResponseConsume records the DTO a fetch response is decoded into
// (Y.4 client side). It fires on `await res.json()` whose result is typed —
// either annotated (`const d: NodeDetail = await res.json()`) or asserted
// (`(await res.json()) as GraphNode[]`) — and emits `function → interface`
// `consumes`. The type name is resolved against same-file interfaces only;
// cross-file/imported decode targets and untyped `.json()` calls are ledgered
// (#12): no edge, no fabricated endpoint.
func (ex *jsExtractor) handleResponseConsume(call *sitter.Node, scopes []*jsScope) {
	fn := call.ChildByFieldName("function")
	if fn == nil || fn.Type() != "member_expression" {
		return
	}
	prop := fn.ChildByFieldName("property")
	if prop == nil || prop.Content(ex.src) != "json" {
		return
	}
	// Climb past the await (await res.json()) to the typing context.
	cur := call
	if p := cur.Parent(); p != nil && p.Type() == "await_expression" {
		cur = p
	}
	typeNode := ex.decodeTargetType(cur)
	if typeNode == nil {
		return // untyped decode — ledgered
	}
	name := baseTypeName(typeNode, ex.src)
	if name == "" {
		return
	}
	nodeID, ok := ex.classNodes[name]
	if !ok {
		return // cross-file/unresolved type — ledgered
	}
	from := attribution(scopes, ex)
	if from == "" {
		from = ex.moduleAttr(call)
	}
	if from == "" || from == nodeID {
		return
	}
	meta := map[string]string{"response_type": name, "via": "json_decode"}
	if typeNode.Type() == "array_type" {
		meta["container"] = "slice"
	}
	ex.addEdge(graph.EdgeTypeConsumes, from, nodeID, graph.ConfidenceStatic, meta)
}

// decodeTargetType returns the TS type node that the expression `expr`
// (an `await res.json()` result) is typed as, via a surrounding `as`
// assertion or an enclosing typed variable declarator — or nil when untyped.
func (ex *jsExtractor) decodeTargetType(expr *sitter.Node) *sitter.Node {
	p := expr.Parent()
	for p != nil && p.Type() == "parenthesized_expression" {
		expr, p = p, p.Parent()
	}
	if p == nil {
		return nil
	}
	switch p.Type() {
	case "as_expression", "satisfies_expression":
		// `<expr> as T` — T is the last named child.
		if n := p.NamedChildCount(); n > 0 {
			return p.NamedChild(int(n) - 1)
		}
	case "variable_declarator":
		if p.ChildByFieldName("value") == expr {
			if ta := p.ChildByFieldName("type"); ta != nil && ta.NamedChildCount() > 0 {
				return ta.NamedChild(0) // type_annotation wraps the actual type
			}
		}
	}
	return nil
}

// baseTypeName reduces a TS type node to the bare named type — peeling
// array (`T[]`) and generic (`Array<T>`, `Promise<T>`) wrappers — so a
// decode target of `GraphNode[]` resolves to `GraphNode`.
func baseTypeName(t *sitter.Node, src []byte) string {
	switch t.Type() {
	case "type_identifier", "identifier":
		return t.Content(src)
	case "array_type":
		if t.NamedChildCount() > 0 {
			return baseTypeName(t.NamedChild(0), src)
		}
	case "generic_type":
		if args := t.ChildByFieldName("type_arguments"); args != nil && args.NamedChildCount() == 1 {
			return baseTypeName(args.NamedChild(0), src)
		}
		if name := t.ChildByFieldName("name"); name != nil {
			return name.Content(src)
		}
	case "parenthesized_type":
		if t.NamedChildCount() > 0 {
			return baseTypeName(t.NamedChild(0), src)
		}
	}
	return ""
}

func (ex *jsExtractor) handleRead(node *sitter.Node, scopes []*jsScope) {
	parent := node.Parent()
	if parent == nil {
		return
	}
	switch parent.Type() {
	case "variable_declarator":
		if parent.ChildByFieldName("name") == node {
			return
		}
	case "assignment_expression", "augmented_assignment_expression":
		if parent.ChildByFieldName("left") == node {
			return
		}
	case "member_expression":
		if parent.ChildByFieldName("property") == node {
			return
		}
	case "pair", "property_identifier", "function_declaration", "method_definition",
		"formal_parameters":
		return
	case "required_parameter", "optional_parameter":
		// The pattern side is a binding; a default *value* referencing a
		// module constant (maxDim = MAX_EXPORT_DIM) is a read.
		if parent.ChildByFieldName("value") != node {
			return
		}
	case "assignment_pattern":
		if parent.ChildByFieldName("left") == node {
			return
		}
	case "call_expression":
		if parent.ChildByFieldName("function") == node {
			// Calls to declared functions are call edges, not variable reads.
			// Calls to variables (signal accessors/setters: notification(),
			// setNotification(x)) read/write the binding, so they fall through.
			if _, isFn := ex.fnDecls[node.Content(ex.src)]; isFn {
				return
			}
		}
	}
	name := node.Content(ex.src)
	from := attribution(scopes, ex)
	frame := resolve(scopes, name)
	switch {
	case frame == 0:
		v := ex.moduleVars[name]
		if v == nil {
			return
		}
		if from == "" {
			from = ex.moduleAttr(node)
		}
		if from == "" || from == v.nodeID || from == ex.fnNodeID(name, ex.fnDecls[name]) {
			return
		}
		// Calling a Solid signal setter mutates the signal: setX(v) is a
		// write on the binding, not a read.
		if v.isSetter && parent.Type() == "call_expression" && parent.ChildByFieldName("function") == node {
			ex.addEdge(graph.EdgeTypeWrites, from, v.nodeID, graph.ConfidenceInferred,
				map[string]string{"op": "call"})
			return
		}
		ex.addEdge(graph.EdgeTypeReads, from, v.nodeID, graph.ConfidenceInferred, nil)
	case frame > 0 && frame < len(scopes)-1:
		ex.captureEdge(node, name, scopes, frame, false)
	}
}

// captureEdge materialises a captured-variable node for an outer function
// local and links the capturing function to it. JS closures share the
// binding, so mutation propagates — captures are by reference.
func (ex *jsExtractor) captureEdge(node *sitter.Node, name string, scopes []*jsScope, frame int, isWrite bool) {
	from := attribution(scopes, ex)
	if from == "" {
		// Module-level reactive blocks (createEffect closures) have no named
		// enclosing function; attribute to the owning declarator/module node.
		from = ex.moduleAttr(node)
	}
	if from == "" {
		return
	}
	declLine := scopes[frame].locals[name]
	id := ex.varNodeID(name, declLine)
	ex.addNode(graph.Node{
		ID: id, Type: graph.NodeTypeVariable, Label: name,
		Service: ex.service, File: ex.file, Line: declLine, EndLine: declLine, Language: ex.langTag,
		Meta: map[string]string{
			"kind": "var", "scope": "captured", "mutable": "true",
		},
	})
	// Same-file closure captures are a reliable structural fact (both the
	// capturing scope and the declaring local live in ex.file), so they carry
	// `inferred` confidence and render in the default view — closure flow is
	// legible without opting into partial edges (Phase U.4).
	ex.addEdge(graph.EdgeTypeCaptures, from, id, graph.ConfidenceInferred,
		map[string]string{"by": "ref"})
	if isWrite {
		ex.addEdge(graph.EdgeTypeWrites, from, id, graph.ConfidencePartial,
			map[string]string{"op": "assign", "via": "closure"})
	}
}

// handleJSXWrite emits signal→element dom_write edges (Y.6, render tail). A JSX
// interpolation — text `{sig()}` or attribute `attr={sig()}` — that reads a Solid
// reactive accessor binds that signal to the enclosing DOM element. The element
// node is minted lazily (bare `<span>` has no id/class, so the matcher never
// emitted one) and only when a resolvable signal read is found, so a dom_write
// never dangles (#10) and dynamic/untyped writes are simply not matched (#11).
func (ex *jsExtractor) handleJSXWrite(jsxExpr *sitter.Node) {
	if len(ex.signals) == 0 {
		return
	}
	elID, elLine, tag := ex.enclosingJSXElement(jsxExpr)
	if elID == "" {
		return
	}
	elementAdded := false
	seen := map[string]bool{}
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		switch n.Type() {
		case "jsx_element", "jsx_self_closing_element", "jsx_expression":
			if n != jsxExpr {
				// Nested elements/expressions own their own writes; walk reaches
				// them separately. Don't attribute their reads to this element.
				return
			}
		case "identifier":
			name := n.Content(ex.src)
			if sigID, ok := ex.signals[name]; ok && !seen[name] {
				seen[name] = true
				if !elementAdded {
					elementAdded = true
					ex.addNode(graph.Node{
						ID: elID, Type: graph.NodeTypeElement, Label: tag,
						Service: ex.service, File: ex.file, Line: elLine, EndLine: elLine, Language: ex.langTag,
						Meta: map[string]string{"tag": tag},
					})
				}
				ex.addEdge(graph.EdgeTypeDOMWrite, sigID, elID, graph.ConfidenceInferred,
					map[string]string{"via": "jsx", "element": tag})
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			visit(n.NamedChild(i))
		}
	}
	visit(jsxExpr)
}

// enclosingJSXElement climbs to the nearest jsx_element / jsx_self_closing_element
// wrapping an interpolation and returns a deterministic element node ID, its line,
// and its tag name. Empty ID means the expression is not inside a JSX element.
func (ex *jsExtractor) enclosingJSXElement(n *sitter.Node) (id string, line int, tag string) {
	for p := n.Parent(); p != nil; p = p.Parent() {
		switch p.Type() {
		case "jsx_element":
			open := p.ChildByFieldName("open_tag")
			if open == nil {
				open = p.NamedChild(0)
			}
			tag = jsxTagName(open, ex.src)
			ln := tsLine(p)
			return fmt.Sprintf("%s:%s:element:%s:%d", ex.service, ex.file, tag, ln), ln, tag
		case "jsx_self_closing_element":
			tag = jsxTagName(p, ex.src)
			ln := tsLine(p)
			return fmt.Sprintf("%s:%s:element:%s:%d", ex.service, ex.file, tag, ln), ln, tag
		}
	}
	return "", 0, ""
}

// handleJSXEvent emits element→function dom_listen edges (Y.7, event head). A JSX
// event attribute — `onClick={handler}` (React/Solid camelCase) or `on:click={handler}`
// (Solid namespaced) — binds the enclosing DOM element to its handler function.
// The ref is resolved to a same-file function declaration; inline arrow handlers,
// call expressions, and cross-file/member refs carry no stable function node, so
// they are ledgered (#12) — never fabricated. The element node is minted lazily
// (only on a resolved handler) so a dom_listen never dangles (#10).
func (ex *jsExtractor) handleJSXEvent(open *sitter.Node) {
	tag := jsxTagName(open, ex.src)
	// The element node anchors on the enclosing jsx_element (for an opening tag)
	// or the self-closing element itself — matching enclosingJSXElement's IDs so
	// a dom_write and a dom_listen on the same element share one node.
	anchor := open
	if open.Type() == "jsx_opening_element" {
		if p := open.Parent(); p != nil && p.Type() == "jsx_element" {
			anchor = p
		}
	}
	elLine := tsLine(anchor)
	elID := fmt.Sprintf("%s:%s:element:%s:%d", ex.service, ex.file, tag, elLine)

	elementAdded := false
	for i := 0; i < int(open.NamedChildCount()); i++ {
		attr := open.NamedChild(i)
		if attr.Type() != "jsx_attribute" {
			continue
		}
		nameNode := attr.NamedChild(0)
		if nameNode == nil {
			continue
		}
		event := eventName(nameNode.Content(ex.src))
		if event == "" {
			continue
		}
		val := jsxAttrValueExpr(attr)
		if val == nil {
			continue
		}
		switch {
		case val.Type() == "identifier":
			// Bare handler ref: onClick={handleClick} — a same-file function.
			ref := val.Content(ex.src)
			if fnLine, ok := ex.resolveHandlerFn(ref); ok {
				ex.emitDOMListen(elID, &elementAdded, elLine, tag, ex.fnNodeID(ref, fnLine),
					event, "", graph.ConfidenceStatic)
			} else {
				ex.ledgerDOMListen(elLine, ref)
			}
		case isFunctionNode(val.Type()):
			// Inline handler: onClick={() => doThing()} — no stable node of its
			// own, so bind the element to each same-file function the arrow body
			// invokes (the possible-flow head; recall over precision). Member/
			// store-method and setter calls don't resolve and are ledgered.
			targets := ex.inlineHandlerTargets(val)
			if len(targets) == 0 {
				ex.ledgerDOMListen(elLine, event+":inline")
				continue
			}
			for _, tgt := range targets {
				ex.emitDOMListen(elID, &elementAdded, elLine, tag, ex.fnNodeID(tgt.name, tgt.line),
					event, "inline", graph.ConfidenceInferred)
			}
		default:
			// Member/other handler expression (this.onClick, obj.fn) — ledgered.
			ex.ledgerDOMListen(elLine, strings.TrimSpace(val.Content(ex.src)))
		}
	}
}

// emitDOMListen mints the element node lazily (only on the first resolved
// handler, so a dom_listen never dangles, #10) and adds the element→function
// edge. handlerKind distinguishes a bare ref ("") from an inline arrow target.
func (ex *jsExtractor) emitDOMListen(elID string, elementAdded *bool, elLine int, tag, toID, event, handlerKind, conf string) {
	if !*elementAdded {
		*elementAdded = true
		ex.addNode(graph.Node{
			ID: elID, Type: graph.NodeTypeElement, Label: tag,
			Service: ex.service, File: ex.file, Line: elLine, EndLine: elLine, Language: ex.langTag,
			Meta: map[string]string{"tag": tag},
		})
	}
	meta := map[string]string{"event": event, "via": "jsx"}
	if handlerKind != "" {
		meta["handler"] = handlerKind
	}
	ex.addEdge(graph.EdgeTypeDOMListen, elID, toID, conf, meta)
}

// ledgerDOMListen records an unresolvable handler so every event attribute
// reaches output or the ledger (#12) — never a fabricated edge.
func (ex *jsExtractor) ledgerDOMListen(line int, name string) {
	ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
		Service: ex.service, File: ex.file, Line: line,
		Name: name, Kind: "dom_listen_unresolved",
	})
}

// handlerTarget is a same-file function an inline handler invokes.
type handlerTarget struct {
	name string
	line int
}

// inlineHandlerTargets scans an inline handler (arrow / function expression) for
// bare-identifier call sites that resolve to a same-file function, returning
// each once. Member calls (store.method()), signal setters, and unresolved
// identifiers are skipped — they carry no function node to bind the element to.
func (ex *jsExtractor) inlineHandlerTargets(fn *sitter.Node) []handlerTarget {
	var out []handlerTarget
	seen := map[string]bool{}
	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		if n.Type() == "call_expression" {
			if callee := n.ChildByFieldName("function"); callee != nil && callee.Type() == "identifier" {
				name := callee.Content(ex.src)
				if line, ok := ex.resolveHandlerFn(name); ok && !seen[name] {
					seen[name] = true
					out = append(out, handlerTarget{name, line})
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			visit(n.NamedChild(i))
		}
	}
	visit(fn)
	return out
}

// eventName maps a JSX event-attribute name to its DOM event type, or "" when
// the attribute is not an event handler. Handles React/Solid camelCase
// (onClick → click, onInput → input) and Solid namespaced (on:click → click).
func eventName(attr string) string {
	if rest, ok := strings.CutPrefix(attr, "on:"); ok {
		return strings.ToLower(rest)
	}
	rest, ok := strings.CutPrefix(attr, "on")
	if !ok || rest == "" || !(rest[0] >= 'A' && rest[0] <= 'Z') {
		return ""
	}
	return strings.ToLower(rest)
}

// jsxAttrValueExpr returns the expression bound by a JSX attribute value
// `={expr}` — the inner expression of the jsx_expression container — or nil for
// bare/string attributes. The caller dispatches on its node type (identifier =
// bare handler ref, arrow/function = inline handler, else = ledgered).
func jsxAttrValueExpr(attr *sitter.Node) *sitter.Node {
	var val *sitter.Node
	for i := int(attr.NamedChildCount()) - 1; i >= 1; i-- {
		if c := attr.NamedChild(i); c.Type() == "jsx_expression" {
			val = c
			break
		}
	}
	if val == nil {
		return nil
	}
	for i := 0; i < int(val.NamedChildCount()); i++ {
		if inner := val.NamedChild(i); inner.Type() != "comment" {
			return inner
		}
	}
	return nil
}

// handleAddEventListener emits element→function dom_listen for a vanilla
// `target.addEventListener("evt", handler)` call (Y.7). The receiver expression
// is the DOM target (an element ref, document, or window); the handler is
// resolved to a same-file function. Dynamic event names, inline/anon handlers,
// and cross-file refs are ledgered (#12). The element node is minted lazily so
// the edge never dangles (#10).
func (ex *jsExtractor) handleAddEventListener(call *sitter.Node) {
	fn := call.ChildByFieldName("function")
	if fn == nil || fn.Type() != "member_expression" {
		return
	}
	prop := fn.ChildByFieldName("property")
	if prop == nil || prop.Content(ex.src) != "addEventListener" {
		return
	}
	args := call.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() < 2 {
		return
	}
	evtArg := args.NamedChild(0)
	if evtArg.Type() != "string" {
		return // dynamic event name — unresolvable
	}
	event := strings.Trim(evtArg.Content(ex.src), "\"'`")
	handler := args.NamedChild(1)
	line := tsLine(call)

	// Concise DOM-target label from the receiver; call-expression receivers
	// (document.getElementById("x")) stay generic to keep the node ID clean.
	target := "element"
	if recv := fn.ChildByFieldName("object"); recv != nil {
		switch recv.Type() {
		case "identifier", "member_expression":
			target = recv.Content(ex.src)
		}
	}
	elID := fmt.Sprintf("%s:%s:element:%s:%d", ex.service, ex.file, target, line)
	added := false
	emit := func(toID, kind, conf string) {
		if !added {
			added = true
			ex.addNode(graph.Node{
				ID: elID, Type: graph.NodeTypeElement, Label: target,
				Service: ex.service, File: ex.file, Line: line, EndLine: line, Language: ex.langTag,
				Meta: map[string]string{"tag": target, "via": "addEventListener"},
			})
		}
		meta := map[string]string{"event": event, "via": "add_event_listener"}
		if kind != "" {
			meta["handler"] = kind
		}
		ex.addEdge(graph.EdgeTypeDOMListen, elID, toID, conf, meta)
	}

	switch {
	case handler.Type() == "identifier":
		ref := handler.Content(ex.src)
		if fnLine, ok := ex.resolveHandlerFn(ref); ok {
			emit(ex.fnNodeID(ref, fnLine), "", graph.ConfidenceStatic)
		} else {
			ex.ledgerDOMListen(line, ref)
		}
	case isFunctionNode(handler.Type()):
		targets := ex.inlineHandlerTargets(handler)
		if len(targets) == 0 {
			ex.ledgerDOMListen(line, event+":inline")
			return
		}
		for _, tgt := range targets {
			emit(ex.fnNodeID(tgt.name, tgt.line), "inline", graph.ConfidenceInferred)
		}
	default:
		ex.ledgerDOMListen(line, strings.TrimSpace(handler.Content(ex.src)))
	}
}

// jsxTagName extracts the tag identifier from a jsx_opening_element or
// jsx_self_closing_element node, tolerating member/namespaced names.
func jsxTagName(n *sitter.Node, src []byte) string {
	if n == nil {
		return "el"
	}
	if name := n.ChildByFieldName("name"); name != nil {
		return name.Content(src)
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c.Type() == "identifier" || c.Type() == "member_expression" || c.Type() == "jsx_namespace_name" {
			return c.Content(src)
		}
	}
	return "el"
}

// handleCall emits flows_to edges when a tracked module variable is passed
// to a function declared in the same file, and (B.1) calls edges when a
// same-file function declaration is passed as a bare identifier argument.
// emitNestedLocalCallEdge resolves a bare-identifier call site whose callee
// is a function-expression variable declared in a NESTED (non-module) lexical
// scope — `var el = function(id){...}` inside one function, called as
// `el(...)` inside that same function — and emits the calls edge the
// pattern matcher's own component_fn_call pass can never produce for it: the
// matcher's node-name index (nameByFileAndName) is built only from
// tree-sitter-pattern-emitted nodes, and no YAML pattern captures a
// non-arrow, non-top-level `var x = function(){}` — only walk()'s
// self-attribution above does, entirely outside the matcher's node universe.
// Without this, every such local was permanently zero-caller regardless of
// how many times it was actually called, because neither side of the call
// graph could see the other's half.
//
// Scoped strictly to frame > 0 (a real nested function scope, not module
// level): module-level names are already correctly resolved by the pattern
// matcher's Pass 3, and re-resolving them here would just double the edge.
// resolve() walks scopes innermost-first, so two sibling scopes each
// shadowing the same name (two different `var el = ...` in two different
// outer functions) each resolve to their OWN declaration.
func (ex *jsExtractor) emitNestedLocalCallEdge(node *sitter.Node, fnName string, scopes []*jsScope) {
	frame := resolve(scopes, fnName)
	if frame <= 0 {
		return
	}
	line, ok := scopes[frame].locals[fnName]
	if !ok {
		return
	}
	toID := ex.fnNodeID(fnName, line)
	if !ex.nodeSeen[toID] {
		// The local name shadows a non-function declaration (`var count = 0`
		// named like a call-site identifier) — no function node exists at
		// this ID, so there is nothing valid to point an edge at.
		return
	}
	fromID := attribution(scopes, ex)
	if fromID == "" {
		fromID = ex.moduleAttr(node)
	}
	if fromID == "" || fromID == toID {
		return
	}
	ex.addEdge(graph.EdgeTypeCalls, fromID, toID, graph.ConfidenceStatic, nil)
}

func (ex *jsExtractor) handleCall(node *sitter.Node, scopes []*jsScope) {
	args := node.ChildByFieldName("arguments")
	if args == nil {
		return
	}

	// Existing flows_to logic: only fires when the callee is a same-file fn.
	fnNode := node.ChildByFieldName("function")
	if fnNode != nil && fnNode.Type() == "identifier" {
		fnName := fnNode.Content(ex.src)
		ex.emitNestedLocalCallEdge(node, fnName, scopes)
		fnLine, declared := ex.fnDecls[fnName]
		if declared {
			for i := 0; i < int(args.NamedChildCount()); i++ {
				arg := args.NamedChild(i)
				if arg.Type() != "identifier" {
					continue
				}
				name := arg.Content(ex.src)
				if resolve(scopes, name) != 0 {
					continue
				}
				v := ex.moduleVars[name]
				if v == nil {
					continue
				}
				// Objects/arrays are handles — mutations inside the callee are
				// visible outside. Primitives copy.
				mode := "unknown"
				switch v.dataType {
				case "object", "array", "function":
					mode = "ref"
				case "string", "number", "boolean":
					mode = "value"
				}
				ex.addEdge(graph.EdgeTypeFlowsTo, v.nodeID, ex.fnNodeID(fnName, fnLine),
					graph.ConfidenceInferred,
					map[string]string{"mode": mode, "data_type": v.dataType})
			}
		}
	}

	// B.1: func-arg detection — for any call expression, check whether any
	// bare identifier argument is a same-file function declaration. If so,
	// emit a calls edge (confidence static — same file, tree-sitter-proven).
	// Member-expression arguments are skipped (JS binding semantics; descoped).
	// Unresolved identifiers are NOT ledgered (spec B.1 clause 3).
	fromID := attribution(scopes, ex)
	if fromID == "" {
		fromID = ex.moduleAttr(node)
	}
	if fromID == "" {
		return
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		arg := args.NamedChild(i)
		if arg.Type() != "identifier" {
			continue
		}
		name := arg.Content(ex.src)
		fnLine, isFn := ex.fnDecls[name]
		if !isFn {
			continue
		}
		toID := ex.fnNodeID(name, fnLine)
		if fromID == toID {
			continue
		}
		ex.addEdge(graph.EdgeTypeCalls, fromID, toID, graph.ConfidenceStatic,
			map[string]string{"via": "func_arg"})
	}
}

// handleNew emits an instantiates edge when the new_expression constructor
// resolves to a same-file class node. Cross-file constructors are resolved by
// LinkJSTypeRelations; unresolvable ones stay silent (no edge, no ledger).
// A `new window.X(...)` / `new globalThis.X(...)` constructor (member_expression
// rooted at a global object) resolves the same way a bare `new X(...)` does
// when X is declared in this file — the common self-registration shape
// (`window.X = X` at the bottom of the file, stamped onto the class node
// itself by stampGlobalSymbols) means classNodes already has the entry.
func (ex *jsExtractor) handleNew(node *sitter.Node, scopes []*jsScope) {
	ctor := node.ChildByFieldName("constructor")
	if ctor == nil {
		return
	}
	var className string
	switch ctor.Type() {
	case "identifier", "type_identifier":
		className = ctor.Content(ex.src)
	case "member_expression":
		_, leaf, ok := globalMemberPath(ctor, ex.src)
		if !ok {
			return
		}
		className = leaf
	default:
		return
	}
	classID, ok := ex.classNodes[className]
	if !ok {
		return // not same-file; linker may resolve cross-file
	}
	fromID := attribution(scopes, ex)
	if fromID == "" {
		fromID = ex.moduleAttr(node)
	}
	if fromID == "" || fromID == classID {
		return
	}
	edgeID := fmt.Sprintf("instantiates:%s->%s", fromID, classID)
	if ex.edgeSeen[edgeID] {
		return
	}
	ex.edgeSeen[edgeID] = true
	ex.edges = append(ex.edges, graph.Edge{
		ID: edgeID, From: fromID, To: classID,
		Type: graph.EdgeTypeInstantiates, Confidence: graph.ConfidenceStatic,
		Meta: map[string]string{"count": "1"},
	})
}

// stampGlobalSymbols stamps Meta["global_symbol"] = name on function/variable
// nodes that are visible at the window-global level:
//
//   - Top-level function declarations in non-module files (no import/export).
//   - window.X = fn|{…} assignments at the top level (any file).
//
// Called after walk() so all nodes are present.
func (ex *jsExtractor) stampGlobalSymbols(root *sitter.Node) {
	// Detect non-module: any top-level import_statement or export_statement.
	isModule := false
	for i := 0; i < int(root.NamedChildCount()); i++ {
		t := root.NamedChild(i).Type()
		if t == "import_statement" || t == "export_statement" {
			isModule = true
			break
		}
	}

	// Build function node index by label → slice index (stable after walk).
	funcIdxByLabel := make(map[string]int)
	for i, n := range ex.nodes {
		if n.Type == graph.NodeTypeFunction {
			if _, exists := funcIdxByLabel[n.Label]; !exists {
				funcIdxByLabel[n.Label] = i
			}
		}
	}

	// Node index by ID → slice index, used to stamp an already-declared class
	// node (ex.classNodes stores IDs, not slice positions) when it is the
	// target of a `window.X = X` self-registration assignment below.
	nodeIdxByID := make(map[string]int, len(ex.nodes))
	for i, n := range ex.nodes {
		nodeIdxByID[n.ID] = i
	}

	stamp := func(idx int, globalName string) {
		if ex.nodes[idx].Meta == nil {
			ex.nodes[idx].Meta = map[string]string{}
		}
		ex.nodes[idx].Meta["global_symbol"] = globalName
	}

	// Case 1: non-module top-level function declarations.
	if !isModule {
		for i := 0; i < int(root.NamedChildCount()); i++ {
			stmt := root.NamedChild(i)
			if stmt.Type() != "function_declaration" {
				continue
			}
			nameNode := stmt.ChildByFieldName("name")
			if nameNode == nil {
				continue
			}
			name := nameNode.Content(ex.src)
			if idx, ok := funcIdxByLabel[name]; ok {
				stamp(idx, name)
			}
		}
	}

	// Case 2: <global-root>.…X = fn|{…} assignments, at any depth.
	// Handles both the single-level top-level fast path (window.save = …) and
	// namespaced/wrapped registrations (window.maple.foo = …, inside IIFEs/modules).
	// Collect new nodes separately so we don't invalidate funcIdxByLabel during iteration.
	var newNodes []graph.Node
	stampAssign := func(expr *sitter.Node) {
		left := expr.ChildByFieldName("left")
		right := expr.ChildByFieldName("right")
		if left == nil || right == nil || left.Type() != "member_expression" {
			return
		}
		dotted, leaf, ok := globalMemberPath(left, ex.src)
		if !ok {
			return
		}
		lineNo := tsLine(expr)

		// Class self-registration: `window.X = X`, referencing a class already
		// declared in this file (DC.15's confirmed shape — e.g. pusher_client.es6's
		// `window.PusherClient = PusherClient`). Stamp the class node itself
		// rather than minting a phantom variable node, so a cross-file
		// `new window.PusherClient(...)` can resolve through it later.
		if right.Type() == "identifier" {
			if classID, ok := ex.classNodes[right.Content(ex.src)]; ok {
				if idx, ok2 := nodeIdxByID[classID]; ok2 {
					stamp(idx, leaf)
					ex.nodes[idx].Meta["global_path"] = dotted
					return
				}
			}
		}

		if isFunctionNode(right.Type()) {
			// Named or anonymous function assigned to <global>.…leaf.
			fnLabel := leaf // default: use the leaf property name as label
			if rightName := right.ChildByFieldName("name"); rightName != nil {
				fnLabel = rightName.Content(ex.src)
			}
			if idx, ok := funcIdxByLabel[fnLabel]; ok {
				stamp(idx, leaf)
				ex.nodes[idx].Meta["global_path"] = dotted
			} else {
				nodeID := ex.fnNodeID(leaf, lineNo)
				if !ex.nodeSeen[nodeID] {
					newNodes = append(newNodes, graph.Node{
						ID: nodeID, Type: graph.NodeTypeFunction, Label: leaf,
						Service: ex.service, File: ex.file, Line: lineNo, EndLine: lineNo, Language: ex.langTag,
						Meta: map[string]string{"global_symbol": leaf, "global_path": dotted},
					})
				}
			}
		} else {
			// Object or other value: create a variable node for the global.
			nodeID := ex.varNodeID(leaf, lineNo)
			if !ex.nodeSeen[nodeID] {
				newNodes = append(newNodes, graph.Node{
					ID: nodeID, Type: graph.NodeTypeVariable, Label: leaf,
					Service: ex.service, File: ex.file, Line: lineNo, EndLine: lineNo, Language: ex.langTag,
					Meta: map[string]string{"global_symbol": leaf, "global_path": dotted, "scope": "global"},
				})
			}
		}
	}

	// Fast path: expression_statement → assignment_expression at root.
	// Slow path: recurse into every other subtree to catch wrapped registrations.
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "assignment_expression" {
			stampAssign(n)
			return
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	for i := 0; i < int(root.NamedChildCount()); i++ {
		stmt := root.NamedChild(i)
		if stmt.Type() == "expression_statement" {
			if expr := stmt.NamedChild(0); expr != nil && expr.Type() == "assignment_expression" {
				stampAssign(expr)
				continue
			}
		}
		walk(stmt)
	}
	for _, n := range newNodes {
		ex.addNode(n)
	}
}

// globalRoots is the set of identifiers that name the global object.
var globalRoots = map[string]bool{"window": true, "globalThis": true, "self": true}

// globalMemberPath returns (dotted, leaf, ok) for a member_expression whose
// left-most object identifier is in {window, globalThis, self}.
//
//	window.maple.closeVulnerabilityModal  -> ("window.maple.closeVulnerabilityModal", "closeVulnerabilityModal", true)
//	window.save                         -> ("window.save", "save", true)
//	foo.bar                             -> ("", "", false)
func globalMemberPath(left *sitter.Node, src []byte) (dotted, leaf string, ok bool) {
	if left.Type() != "member_expression" {
		return "", "", false
	}
	prop := left.ChildByFieldName("property")
	if prop == nil {
		return "", "", false
	}
	leaf = prop.Content(src)

	// Walk the object chain to the root identifier, collecting segments.
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
	if !globalRoots[rootName] {
		return "", "", false
	}
	// segs is leaf-first; reverse and prepend root to build the dotted path.
	parts := make([]string, 0, len(segs)+1)
	parts = append(parts, rootName)
	for i := len(segs) - 1; i >= 0; i-- {
		parts = append(parts, segs[i])
	}
	return strings.Join(parts, "."), leaf, true
}

// collectIdentifiers visits the identifiers *bound* under n (parameter
// patterns, destructuring) and reports their name and line. Default-value
// expressions and type annotations are not bindings: `maxDim = MAX_EXPORT_DIM`
// binds maxDim only — treating the default as a local would shadow the module
// constant and swallow its reads edge.
func collectIdentifiers(n *sitter.Node, src []byte, visit func(name string, line int)) {
	if n.Type() == "identifier" {
		visit(n.Content(src), int(n.StartPoint().Row)+1)
	}
	if n.Type() == "assignment_pattern" {
		if l := n.ChildByFieldName("left"); l != nil {
			collectIdentifiers(l, src, visit)
		}
		return
	}
	value := n.ChildByFieldName("value")
	typeAnn := n.ChildByFieldName("type")
	for i := 0; i < int(n.NamedChildCount()); i++ {
		child := n.NamedChild(i)
		if child == value || child == typeAnn {
			continue
		}
		collectIdentifiers(child, src, visit)
	}
}
