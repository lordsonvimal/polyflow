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
func extractJSVariables(file, service, langTag, grammarLang string, src []byte) ([]graph.Node, []graph.Edge) {
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
		return nil, nil
	}
	defer tree.Close()

	ex := &jsExtractor{
		file: file, service: service, langTag: langTag, src: src,
		moduleVars: map[string]*jsVar{},
		fnDecls:    map[string]int{},
		nodeSeen:   map[string]bool{},
		edgeSeen:   map[string]bool{},
	}
	root := tree.RootNode()
	ex.collectTopLevel(root)
	ex.walk(root, []*jsScope{ex.moduleScope()})

	sort.Slice(ex.nodes, func(i, j int) bool { return ex.nodes[i].ID < ex.nodes[j].ID })
	sort.Slice(ex.edges, func(i, j int) bool { return ex.edges[i].ID < ex.edges[j].ID })
	return ex.nodes, ex.edges
}

type jsVar struct {
	nodeID   string
	dataType string
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
	fnDecls    map[string]int // top-level function name → line

	nodes    []graph.Node
	edges    []graph.Edge
	nodeSeen map[string]bool
	edgeSeen map[string]bool
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
				if nameNode == nil || nameNode.Type() != "identifier" {
					continue // destructuring patterns are not tracked in v1
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
		}
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

// attribution returns the graph node ID of the nearest named enclosing
// function, or "" at module level.
func attribution(scopes []*jsScope, ex *jsExtractor) string {
	for i := len(scopes) - 1; i >= 1; i-- {
		if scopes[i].fnName != "" {
			return ex.fnNodeID(scopes[i].fnName, scopes[i].fnLine)
		}
	}
	return ""
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
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			frame.fnName, frame.fnLine = nameNode.Content(ex.src), tsLine(node)
		} else if decl := node.Parent(); decl != nil && decl.Type() == "variable_declarator" {
			if dn := decl.ChildByFieldName("name"); dn != nil && dn.Type() == "identifier" {
				frame.fnName = dn.Content(ex.src)
				frame.fnLine = tsLine(declStatement(decl))
			}
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
				if nameNode := decl.ChildByFieldName("name"); nameNode != nil && nameNode.Type() == "identifier" {
					cur.locals[nameNode.Content(ex.src)] = tsLine(decl)
				}
			}
		}
	case "assignment_expression", "augmented_assignment_expression":
		if left := node.ChildByFieldName("left"); left != nil && left.Type() == "identifier" {
			ex.handleWrite(left.Content(ex.src), scopes)
		}
	case "identifier":
		ex.handleRead(node, scopes)
	case "call_expression":
		ex.handleCall(node, scopes)
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

func (ex *jsExtractor) handleWrite(name string, scopes []*jsScope) {
	from := attribution(scopes, ex)
	frame := resolve(scopes, name)
	switch {
	case frame == 0: // module variable
		v := ex.moduleVars[name]
		if v == nil || from == "" || from == ex.fnNodeID(name, ex.fnDecls[name]) {
			return
		}
		ex.addEdge(graph.EdgeTypeWrites, from, v.nodeID, graph.ConfidenceInferred,
			map[string]string{"op": "assign"})
	case frame > 0 && frame < len(scopes)-1: // captured outer local
		ex.captureEdge(name, scopes, frame, true)
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
		"formal_parameters", "required_parameter", "optional_parameter":
		return
	case "call_expression":
		if parent.ChildByFieldName("function") == node {
			return // plain function calls are call edges, not variable reads
		}
	}
	name := node.Content(ex.src)
	from := attribution(scopes, ex)
	frame := resolve(scopes, name)
	switch {
	case frame == 0:
		v := ex.moduleVars[name]
		if v == nil || from == "" || from == ex.fnNodeID(name, ex.fnDecls[name]) {
			return
		}
		ex.addEdge(graph.EdgeTypeReads, from, v.nodeID, graph.ConfidenceInferred, nil)
	case frame > 0 && frame < len(scopes)-1:
		ex.captureEdge(name, scopes, frame, false)
	}
}

// captureEdge materialises a captured-variable node for an outer function
// local and links the capturing function to it. JS closures share the
// binding, so mutation propagates — captures are by reference.
func (ex *jsExtractor) captureEdge(name string, scopes []*jsScope, frame int, isWrite bool) {
	from := attribution(scopes, ex)
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
	ex.addEdge(graph.EdgeTypeCaptures, from, id, graph.ConfidencePartial,
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

// collectIdentifiers visits every identifier under n (parameter patterns,
// destructuring) and reports its name and line.
func collectIdentifiers(n *sitter.Node, src []byte, visit func(name string, line int)) {
	if n.Type() == "identifier" {
		visit(n.Content(src), int(n.StartPoint().Row)+1)
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		collectIdentifiers(n.NamedChild(i), src, visit)
	}
}
