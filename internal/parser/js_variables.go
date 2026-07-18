package parser

import (
	"context"
	"fmt"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	jssitter "github.com/smacker/go-tree-sitter/javascript"
	tssitter "github.com/smacker/go-tree-sitter/typescript/typescript"
	tsxsitter "github.com/smacker/go-tree-sitter/typescript/tsx"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// extractJSVariables is the structural (tree-sitter) variable-tracking pass
// for JavaScript/TypeScript. Unlike the Go SSA pass it has no type checker,
// so everything it emits carries reduced confidence: reads/writes are
// "inferred" (shadowing is approximated lexically), closure captures are
// "partial". Tracked variables are module-scope declarations and locals
// captured by nested functions; function-local variables stay out of the
// graph.
func extractJSVariables(file, service, langTag, grammarLang string, src []byte) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
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
		return nil, nil, nil
	}
	defer tree.Close()

	ex := &jsExtractor{
		file: file, service: service, langTag: langTag, src: src,
		moduleVars: map[string]*jsVar{},
		fnDecls:    map[string]int{},
		classNodes: map[string]string{},
		nodeSeen:   map[string]bool{},
		edgeSeen:   map[string]bool{},
	}
	root := tree.RootNode()
	ex.preCollectClasses(root)
	ex.collectTopLevel(root)
	ex.walk(root, []*jsScope{ex.moduleScope()})
	ex.stampGlobalSymbols(root)

	sort.Slice(ex.nodes, func(i, j int) bool { return ex.nodes[i].ID < ex.nodes[j].ID })
	sort.Slice(ex.edges, func(i, j int) bool { return ex.edges[i].ID < ex.edges[j].ID })
	return ex.nodes, ex.edges, ex.unresolved
}

type jsVar struct {
	nodeID   string
	dataType string
	isSetter bool // Solid signal setter (const [x, setX] = createSignal(...))
}

// jsScope is one lexical function frame (or the module frame at index 0).
type jsScope struct {
	fnName string          // attribution: nearest named enclosing function
	fnLine int
	locals map[string]int  // name → declaration line (function scopes only)
}

type jsExtractor struct {
	file, service, langTag string
	src                    []byte

	moduleVars map[string]*jsVar
	fnDecls    map[string]int    // top-level function name → line
	classNodes map[string]string // class/interface name → nodeID (same-file)

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
						Line: tsLine(stmt), Language: ex.langTag,
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
					Service: ex.service, File: ex.file, Line: tsLine(stmt), Language: ex.langTag,
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
		ex.addNode(graph.Node{
			ID: id, Type: graph.NodeTypeVariable, Label: name,
			Service: ex.service, File: ex.file, Line: line, Language: ex.langTag,
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
		ID: fmt.Sprintf("%s:%s:class:%s:%d", ex.service, ex.file, name, tsLine(stmt)),
		Type: graph.NodeTypeClass, Label: name,
		Service: ex.service, File: ex.file, Line: tsLine(stmt), Language: ex.langTag,
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
//  - JavaScript: class_heritage has a `value` field directly (the superclass expr).
//  - TypeScript: class_heritage has named children extends_clause / implements_clause.
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
		Service: ex.service, File: ex.file, Line: tsLine(stmt), Language: ex.langTag,
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
			ex.addNode(graph.Node{
				ID:    ex.fnNodeID(frame.fnName, frame.fnLine),
				Type:  graph.NodeTypeFunction,
				Label: frame.fnName,
				Service: ex.service, File: ex.file, Line: frame.fnLine,
				Language: ex.langTag,
			})
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
					collectPatternBindings(nameNode, ex.src, func(name string, _ int) {
						cur.locals[name] = tsLine(decl)
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
	case "new_expression":
		ex.handleNew(node, scopes)
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
		Service: ex.service, File: ex.file, Line: declLine, Language: ex.langTag,
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

// handleCall emits flows_to edges when a tracked module variable is passed
// to a function declared in the same file.
func (ex *jsExtractor) handleCall(node *sitter.Node, scopes []*jsScope) {
	fnNode := node.ChildByFieldName("function")
	if fnNode == nil || fnNode.Type() != "identifier" {
		return
	}
	fnName := fnNode.Content(ex.src)
	fnLine, declared := ex.fnDecls[fnName]
	if !declared {
		return
	}
	args := node.ChildByFieldName("arguments")
	if args == nil {
		return
	}
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

// handleNew emits an instantiates edge when the new_expression constructor
// resolves to a same-file class node. Cross-file constructors are resolved by
// LinkJSTypeRelations; unresolvable ones stay silent (no edge, no ledger).
func (ex *jsExtractor) handleNew(node *sitter.Node, scopes []*jsScope) {
	ctor := node.ChildByFieldName("constructor")
	if ctor == nil {
		return
	}
	if ctor.Type() != "identifier" && ctor.Type() != "type_identifier" {
		return
	}
	className := ctor.Content(ex.src)
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

	// Case 2: window.X = fn|{…} assignments at top level.
	// Collect new nodes separately so we don't invalidate funcIdxByLabel during iteration.
	var newNodes []graph.Node
	for i := 0; i < int(root.NamedChildCount()); i++ {
		stmt := root.NamedChild(i)
		if stmt.Type() != "expression_statement" {
			continue
		}
		// expression_statement → assignment_expression
		expr := stmt.NamedChild(0)
		if expr == nil || expr.Type() != "assignment_expression" {
			continue
		}
		left := expr.ChildByFieldName("left")
		right := expr.ChildByFieldName("right")
		if left == nil || right == nil || left.Type() != "member_expression" {
			continue
		}
		obj := left.ChildByFieldName("object")
		prop := left.ChildByFieldName("property")
		if obj == nil || prop == nil || obj.Content(ex.src) != "window" {
			continue
		}
		propName := prop.Content(ex.src)
		lineNo := tsLine(stmt)

		if isFunctionNode(right.Type()) {
			// Named or anonymous function assigned to window.X.
			fnLabel := propName // default: use window property name as label
			if rightName := right.ChildByFieldName("name"); rightName != nil {
				fnLabel = rightName.Content(ex.src)
			}
			if idx, ok := funcIdxByLabel[fnLabel]; ok {
				stamp(idx, propName)
			} else {
				nodeID := ex.fnNodeID(propName, lineNo)
				if !ex.nodeSeen[nodeID] {
					newNodes = append(newNodes, graph.Node{
						ID: nodeID, Type: graph.NodeTypeFunction, Label: propName,
						Service: ex.service, File: ex.file, Line: lineNo, Language: ex.langTag,
						Meta: map[string]string{"global_symbol": propName},
					})
				}
			}
		} else {
			// Object or other value: create a variable node for the global.
			nodeID := ex.varNodeID(propName, lineNo)
			if !ex.nodeSeen[nodeID] {
				newNodes = append(newNodes, graph.Node{
					ID: nodeID, Type: graph.NodeTypeVariable, Label: propName,
					Service: ex.service, File: ex.file, Line: lineNo, Language: ex.langTag,
					Meta: map[string]string{"global_symbol": propName, "scope": "global"},
				})
			}
		}
	}
	for _, n := range newNodes {
		ex.addNode(n)
	}
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
