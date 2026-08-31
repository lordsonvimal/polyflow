package linker

import (
	"fmt"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// LinkJSReceiverTypeCalls resolves JS/TS method calls whose receiver's class
// is recoverable through purely syntactic evidence — the JS/TS analogue of
// ruby_receiver_types.go, which has no equivalent in this package at all
// until now. Three shapes are covered:
//
//   - `this.method(...)` inside a class method body. Confirmed live on
//     gitnexus: 1671 call sites across 39 files in src/core/ingestion alone
//     use exactly this shape, and NO existing parser or linker pass resolves
//     a `this.` receiver at all — this is a bigger, more foundational gap
//     than the typed-dispatch shapes below, not a narrow pattern.
//   - a local variable/parameter/class-field whose type is known: assigned
//     `new Foo(...)`, or carrying an explicit TypeScript type annotation
//     (including a constructor parameter property — `constructor(private
//     builder: CfgBuilder)` — which is both a local and a class field of
//     that type for the rest of the class).
//   - an interface-typed receiver (e.g. `config: FieldExtractionConfig`)
//     fans out to every class implementing that interface — its true
//     runtime type could be any of them, and an interface itself has no
//     method bodies of its own to point at. This is the JS/TS analogue of
//     Ruby's downward override dispatch (ruby_override_dispatch.go).
//
// A resolved class dispatches through its own ancestor chain (nearest
// definer wins) so a call landing on an inherited-but-not-overridden method
// still resolves, the same way Ruby's mixin/ancestor walk already does for
// an implicit self-call.
//
// This is intentionally still not general type inference: a receiver whose
// type comes from anything else (a function's own return type, a generic
// type parameter, a destructured value, an untyped plain-JS parameter) is
// left unresolved, the same "syntactically recoverable slice only" bar
// ruby_receiver_types.go holds itself to.
func LinkJSReceiverTypeCalls(nodes []graph.Node, priorEdges []graph.Edge, serviceFiles map[string][]string) ([]graph.Edge, []graph.UnresolvedRef) {
	type classInfo struct {
		id, label     string
		line, endLine int
	}
	classesByFile := make(map[string][]classInfo)
	svcClassByLabel := make(map[string]map[string]string)
	svcInterfaceByLabel := make(map[string]map[string]string)
	classByID := make(map[string]bool)
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypeClass:
			classesByFile[n.File] = append(classesByFile[n.File], classInfo{n.ID, n.Label, n.Line, n.EndLine})
			classByID[n.ID] = true
			m := svcClassByLabel[n.Service]
			if m == nil {
				m = map[string]string{}
				svcClassByLabel[n.Service] = m
			}
			if _, ok := m[n.Label]; !ok {
				m[n.Label] = n.ID
			}
		case graph.NodeTypeInterface:
			m := svcInterfaceByLabel[n.Service]
			if m == nil {
				m = map[string]string{}
				svcInterfaceByLabel[n.Service] = m
			}
			if _, ok := m[n.Label]; !ok {
				m[n.Label] = n.ID
			}
		}
	}
	if len(classByID) == 0 {
		return nil, nil
	}

	// methodsByClass: classID -> methodName -> methodID. JS/TS method nodes
	// carry no Meta["class"] link the way Ruby's do (buildMethodsByClass in
	// ruby_class_method_calls.go), so a method's owning class is recovered
	// the same way LinkJSTypeRelations' constructorByClass already recovers
	// a constructor's owning class: a function node's line falling inside
	// [class.Line, class.EndLine] in the same file, innermost range wins.
	type funcInfo struct {
		line  int
		id    string
		label string
	}
	funcsByFile := make(map[string][]funcInfo)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeFunction {
			funcsByFile[n.File] = append(funcsByFile[n.File], funcInfo{n.Line, n.ID, n.Label})
		}
	}
	methodsByClass := make(map[string]map[string]string)
	for file, fns := range funcsByFile {
		classes := classesByFile[file]
		if len(classes) == 0 {
			continue
		}
		for _, fn := range fns {
			var best *classInfo
			for i := range classes {
				c := &classes[i]
				if fn.line >= c.line && fn.line <= c.endLine {
					if best == nil || (c.endLine-c.line) < (best.endLine-best.line) {
						best = c
					}
				}
			}
			if best == nil {
				continue
			}
			m := methodsByClass[best.id]
			if m == nil {
				m = map[string]string{}
				methodsByClass[best.id] = m
			}
			if _, ok := m[fn.label]; !ok {
				m[fn.label] = fn.id
			}
		}
	}

	// parentOf/implementersByInterface, built from inherits/implements edges
	// already emitted by the parser or LinkJSTypeRelations (this pass must
	// run after it — see link_passes.go). ancestorChain lets a `this.` or
	// typed-receiver call resolve to an inherited method the receiving
	// class itself never overrides.
	parentOf := make(map[string]string)
	implementersByInterface := make(map[string][]string)
	for _, e := range priorEdges {
		switch e.Type {
		case graph.EdgeTypeInherits:
			if classByID[e.To] {
				parentOf[e.From] = e.To
			}
		case graph.EdgeTypeImplements:
			implementersByInterface[e.To] = append(implementersByInterface[e.To], e.From)
		}
	}
	ancestorChain := func(classID string) []string {
		var chain []string
		cur := classID
		for depth := 0; depth < 8 && cur != ""; depth++ {
			chain = append(chain, cur)
			cur = parentOf[cur]
		}
		return chain
	}

	var allEdges []graph.Edge
	var allUnresolved []graph.UnresolvedRef
	seen := make(map[string]bool)

	for svcName, files := range serviceFiles {
		classByLabel := svcClassByLabel[svcName]
		ifaceByLabel := svcInterfaceByLabel[svcName]
		if len(classByLabel) == 0 && len(ifaceByLabel) == 0 {
			continue
		}
		for _, file := range files {
			if !isJSFile(file) {
				continue
			}
			edges := resolveJSReceiverTypeCalls(file, svcName, classByLabel, ifaceByLabel, methodsByClass, ancestorChain, implementersByInterface, seen)
			allEdges = append(allEdges, edges...)
		}
	}
	return allEdges, allUnresolved
}

// typeAnnotationName extracts the bare type name from a TypeScript
// type_annotation node (`: Foo`, `: Foo<Bar>`), or "" for any type shape
// too ambiguous to trust (union, array, primitive, etc.) — the same
// deliberately-narrow bar inferRubyExprClass holds a Ruby expression to.
func typeAnnotationName(typeAnno *sitter.Node, src []byte) string {
	if typeAnno == nil {
		return ""
	}
	t := typeAnno
	if typeAnno.Type() == "type_annotation" {
		if typeAnno.NamedChildCount() == 0 {
			return ""
		}
		t = typeAnno.NamedChild(0)
	}
	switch t.Type() {
	case "type_identifier":
		return t.Content(src)
	case "generic_type":
		if base := t.ChildByFieldName("name"); base != nil {
			return base.Content(src)
		}
	}
	return ""
}

// collectClassFieldTypes records classID+"."+fieldName -> typeName for every
// type-annotated class field declaration and every constructor parameter
// property (`constructor(private builder: CfgBuilder)` — a TS shape that is
// simultaneously a constructor-scoped local and, for the rest of the class,
// a typed field accessed as `this.builder`). TS's public_field_definition
// names its field via the "name" field; plain JS's field_definition (no type
// possible) uses "property" instead — collectClass in js_variables.go only
// checks "property", so it silently collects nothing for a TS class's field
// list, a pre-existing gap this pass does not depend on and does not fix.
func collectClassFieldTypes(root *sitter.Node, src []byte, svcName, relFile string, fieldType map[string]string) {
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "class_declaration" {
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				classID := fmt.Sprintf("%s:%s:class:%s:%d", svcName, relFile, nameNode.Content(src), int(n.StartPoint().Row)+1)
				if body := n.ChildByFieldName("body"); body != nil {
					for i := 0; i < int(body.NamedChildCount()); i++ {
						m := body.NamedChild(i)
						switch m.Type() {
						case "public_field_definition":
							if fn := m.ChildByFieldName("name"); fn != nil {
								if tn := typeAnnotationName(m.ChildByFieldName("type"), src); tn != "" {
									fieldType[classID+"."+fn.Content(src)] = tn
								}
							}
						case "field_definition":
							if fn := m.ChildByFieldName("property"); fn != nil {
								if tn := typeAnnotationName(m.ChildByFieldName("type"), src); tn != "" {
									fieldType[classID+"."+fn.Content(src)] = tn
								}
							}
						case "method_definition":
							if mn := m.ChildByFieldName("name"); mn != nil && mn.Content(src) == "constructor" {
								collectParamProperties(m, classID, src, fieldType)
							}
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
}

// collectParamProperties scans a constructor's parameter list for TS
// parameter properties: a required_parameter/optional_parameter carrying an
// accessibility modifier (public/private/protected) or `readonly` is both a
// constructor-local and a same-named, same-typed class field.
func collectParamProperties(ctor *sitter.Node, classID string, src []byte, fieldType map[string]string) {
	params := ctor.ChildByFieldName("parameters")
	if params == nil {
		return
	}
	for i := 0; i < int(params.NamedChildCount()); i++ {
		p := params.NamedChild(i)
		if p.Type() != "required_parameter" && p.Type() != "optional_parameter" {
			continue
		}
		isProperty := false
		for j := 0; j < int(p.ChildCount()); j++ {
			switch p.Child(j).Content(src) {
			case "public", "private", "protected", "readonly":
				isProperty = true
			}
		}
		if !isProperty {
			continue
		}
		pattern := p.ChildByFieldName("pattern")
		if pattern == nil || pattern.Type() != "identifier" {
			continue
		}
		if tn := typeAnnotationName(p.ChildByFieldName("type"), src); tn != "" {
			fieldType[classID+"."+pattern.Content(src)] = tn
		}
	}
}

// resolveJSReceiverTypeCalls re-parses file, tracking the enclosing class
// (for `this.`), the enclosing function (the `from` of any emitted `calls`
// edge — minted via the same convention constDeclFunctionID documents), and
// a per-function-scope local variable/parameter type map, then resolves
// every `<receiver>.<method>(...)` call site whose receiver's type it can
// pin down.
func resolveJSReceiverTypeCalls(file, svcName string, classByLabel, ifaceByLabel map[string]string, methodsByClass map[string]map[string]string, ancestorChain func(string) []string, implementersByInterface map[string][]string, seen map[string]bool) []graph.Edge {
	src, root, _, ok := jsParse(file)
	if !ok {
		return nil
	}
	relFile := patterns.RelativizeToCwd(file)

	fieldType := make(map[string]string) // classID+"."+fieldName -> typeName
	collectClassFieldTypes(root, src, svcName, relFile, fieldType)

	var edges []graph.Edge

	resolveOnClass := func(fromID, classID, methodName string) bool {
		for _, cid := range ancestorChain(classID) {
			if mID, ok := methodsByClass[cid][methodName]; ok {
				eid := fmt.Sprintf("calls:%s->%s", fromID, mID)
				if !seen[eid] {
					seen[eid] = true
					edges = append(edges, graph.Edge{
						ID: eid, From: fromID, To: mID,
						Type: graph.EdgeTypeCalls, Confidence: graph.ConfidenceInferred,
						Meta: map[string]string{"via": "receiver_type_call"},
					})
				}
				return true
			}
		}
		return false
	}

	resolveOnType := func(fromID, typeName, methodName string) {
		if classID, ok := classByLabel[typeName]; ok {
			resolveOnClass(fromID, classID, methodName)
			return
		}
		if ifaceID, ok := ifaceByLabel[typeName]; ok {
			// Interface dispatch fans out: the true runtime type could be
			// any implementer, so every one that has the method gets an
			// edge (the JS/TS analogue of Ruby's downward override
			// dispatch in ruby_override_dispatch.go).
			for _, implID := range implementersByInterface[ifaceID] {
				resolveOnClass(fromID, implID, methodName)
			}
		}
	}

	var classStack []string
	var walk func(n *sitter.Node, fnID string, locals map[string]string)
	walk = func(n *sitter.Node, fnID string, locals map[string]string) {
		t := n.Type()

		poppedClass := false
		if t == "class_declaration" {
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				classID := fmt.Sprintf("%s:%s:class:%s:%d", svcName, relFile, nameNode.Content(src), int(n.StartPoint().Row)+1)
				classStack = append(classStack, classID)
				poppedClass = true
			}
		}

		childFnID, childLocals := fnID, locals
		if isFunctionLike(t) {
			childLocals = map[string]string{}
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				childFnID = fmt.Sprintf("%s:%s:function:%s:%d", svcName, relFile, nameNode.Content(src), int(n.StartPoint().Row)+1)
			} else if id, ok := constDeclFunctionID(n, svcName, relFile, src); ok {
				childFnID = id
			}
			if params := n.ChildByFieldName("parameters"); params != nil {
				for i := 0; i < int(params.NamedChildCount()); i++ {
					p := params.NamedChild(i)
					if p.Type() != "required_parameter" && p.Type() != "optional_parameter" {
						continue
					}
					pattern := p.ChildByFieldName("pattern")
					if pattern == nil || pattern.Type() != "identifier" {
						continue
					}
					if tn := typeAnnotationName(p.ChildByFieldName("type"), src); tn != "" {
						childLocals[pattern.Content(src)] = tn
					}
				}
			}
		}

		if t == "variable_declarator" {
			if nameNode := n.ChildByFieldName("name"); nameNode != nil && nameNode.Type() == "identifier" {
				varName := nameNode.Content(src)
				if tn := typeAnnotationName(n.ChildByFieldName("type"), src); tn != "" {
					locals[varName] = tn
				} else if val := n.ChildByFieldName("value"); val != nil && val.Type() == "new_expression" {
					if ctor := val.ChildByFieldName("constructor"); ctor != nil {
						switch ctor.Type() {
						case "identifier", "type_identifier":
							locals[varName] = ctor.Content(src)
						}
					}
				}
			}
		}

		if t == "call_expression" && fnID != "" {
			if fn := n.ChildByFieldName("function"); fn != nil && fn.Type() == "member_expression" {
				obj := fn.ChildByFieldName("object")
				prop := fn.ChildByFieldName("property")
				if obj != nil && prop != nil && prop.Type() == "property_identifier" {
					methodName := prop.Content(src)
					switch obj.Type() {
					case "this":
						if len(classStack) > 0 {
							resolveOnClass(fnID, classStack[len(classStack)-1], methodName)
						}
					case "identifier":
						if tn, ok := locals[obj.Content(src)]; ok {
							resolveOnType(fnID, tn, methodName)
						}
					case "member_expression":
						innerObj := obj.ChildByFieldName("object")
						innerProp := obj.ChildByFieldName("property")
						if innerObj != nil && innerObj.Type() == "this" && innerProp != nil && len(classStack) > 0 {
							if tn, ok := fieldType[classStack[len(classStack)-1]+"."+innerProp.Content(src)]; ok {
								resolveOnType(fnID, tn, methodName)
							}
						}
					}
				}
			}
		}

		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i), childFnID, childLocals)
		}

		if poppedClass {
			classStack = classStack[:len(classStack)-1]
		}
	}
	walk(root, "", map[string]string{})
	return edges
}
