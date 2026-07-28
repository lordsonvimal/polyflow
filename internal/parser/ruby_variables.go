package parser

import (
	"context"
	"fmt"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// extractRubyVariables is the structural variable-tracking pass for Ruby:
// constants, classes (with methods and attr_* accessors), and instance /
// class variables with reads/writes edges from the enclosing method. All
// edges are inferred confidence — Ruby's dynamism rules out certainty.
// Block-capture tracking is deliberately skipped in v1: blocks are so
// pervasive in Ruby that lexical capture edges would be mostly noise.
func extractRubyVariables(file, service string, src []byte) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef) {
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil, nil, nil
	}
	defer tree.Close()

	ex := &rubyExtractor{
		file: file, service: service, src: src,
		ivarDecl:           map[string]int{},
		classTable:         map[string]string{},
		nodeSeen:           map[string]bool{},
		edgeSeen:           map[string]bool{},
		methodsByClassName: map[string]string{},
		methodsByName:      map[string][]string{},
	}
	// Pre-collect class names for same-file constant resolution, and method
	// definitions (class-scoped and flat) so calls to a method defined later
	// in the file still resolve (forward references).
	ex.preCollectRubyClasses(tree.RootNode())
	ex.preCollectRubyMethods(tree.RootNode(), "")
	ex.walk(tree.RootNode(), "", "", "")

	sort.Slice(ex.nodes, func(i, j int) bool { return ex.nodes[i].ID < ex.nodes[j].ID })
	sort.Slice(ex.edges, func(i, j int) bool { return ex.edges[i].ID < ex.edges[j].ID })
	return ex.nodes, ex.edges, ex.unresolved
}

type rubyExtractor struct {
	file, service string
	src           []byte

	ivarDecl   map[string]int    // "@name" (class-qualified) → first-seen line
	classTable map[string]string // class/module name → nodeID (same-file)

	// methodsByClassName/methodsByName index same-file method definitions for
	// bare-call resolution (implicit-self calls): "class\x00name" → nodeID,
	// and name → every nodeID sharing that name (used only when the
	// class-scoped lookup misses and there is exactly one file-wide
	// candidate, so an ambiguous name never misattributes a call).
	methodsByClassName map[string]string
	methodsByName      map[string][]string

	nodes      []graph.Node
	edges      []graph.Edge
	unresolved []graph.UnresolvedRef
	nodeSeen   map[string]bool
	edgeSeen   map[string]bool
}

func (ex *rubyExtractor) addNode(n graph.Node) {
	if !ex.nodeSeen[n.ID] {
		ex.nodeSeen[n.ID] = true
		ex.nodes = append(ex.nodes, n)
	}
}

func (ex *rubyExtractor) addEdge(typ graph.EdgeType, from, to string, meta map[string]string) {
	id := fmt.Sprintf("rbvar:%s:%s->%s", typ, from, to)
	if ex.edgeSeen[id] {
		return
	}
	ex.edgeSeen[id] = true
	ex.edges = append(ex.edges, graph.Edge{
		ID: id, From: from, To: to, Type: typ,
		Confidence: graph.ConfidenceInferred, Meta: meta,
	})
}

func rbLine(n *sitter.Node) int { return int(n.StartPoint().Row) + 1 }

// ivarNode materialises the variable node for an instance/class variable the
// first time it is seen and returns its ID.
func (ex *rubyExtractor) ivarNode(name, class string, ln int) string {
	key := class + "\x00" + name
	declLine, seen := ex.ivarDecl[key]
	if !seen {
		declLine = ln
		ex.ivarDecl[key] = ln
	}
	scope := "instance"
	if strings.HasPrefix(name, "@@") {
		scope = "class"
	}
	id := fmt.Sprintf("%s:%s:variable:%s:%d", ex.service, ex.file, name, declLine)
	ex.addNode(graph.Node{
		ID: id, Type: graph.NodeTypeVariable, Label: name,
		Service: ex.service, File: ex.file, Line: declLine, Language: "ruby",
		Meta: map[string]string{
			"kind": "var", "scope": scope, "mutable": "true",
			"class": class,
		},
	})
	return id
}

func (ex *rubyExtractor) methodNodeID(method string, ln int) string {
	return fmt.Sprintf("%s:%s:function:%s:%d", ex.service, ex.file, method, ln)
}

// preCollectRubyClasses scans the AST recursively to build classTable:
// className → nodeID for all class/module declarations in the file.
func (ex *rubyExtractor) preCollectRubyClasses(node *sitter.Node) {
	t := node.Type()
	if t == "class" || t == "module" {
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			name := nameNode.Content(ex.src)
			id := fmt.Sprintf("%s:%s:class:%s:%d", ex.service, ex.file, name, rbLine(node))
			ex.classTable[name] = id
		}
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		ex.preCollectRubyClasses(node.NamedChild(i))
	}
}

// preCollectRubyMethods scans the AST recursively to build methodsByClassName
// and methodsByName: every method/singleton_method definition, keyed by its
// enclosing class (bare name, matching classTable's simplification) and by
// its bare name alone. Runs before walk so a call to a method defined later
// in the same file still resolves.
func (ex *rubyExtractor) preCollectRubyMethods(node *sitter.Node, class string) {
	switch node.Type() {
	case "class", "module":
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			class = nameNode.Content(ex.src)
		}
	case "method", "singleton_method":
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			name := nameNode.Content(ex.src)
			id := ex.methodNodeID(name, rbLine(node))
			if class != "" {
				ex.methodsByClassName[class+"\x00"+name] = id
			}
			ex.methodsByName[name] = append(ex.methodsByName[name], id)
		}
	}
	for i := 0; i < int(node.NamedChildCount()); i++ {
		ex.preCollectRubyMethods(node.NamedChild(i), class)
	}
}

// walk descends the AST carrying the enclosing class name, class nodeID, and method nodeID.
func (ex *rubyExtractor) walk(node *sitter.Node, class, classID, methodID string) {
	switch node.Type() {
	case "class", "module":
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			name := nameNode.Content(ex.src)
			ex.collectClass(node, name)
			class = name
			classID = fmt.Sprintf("%s:%s:class:%s:%d", ex.service, ex.file, name, rbLine(node))

			// Superclass → inherits edge.
			if superNode := node.ChildByFieldName("superclass"); superNode != nil {
				// superNode is a `superclass` AST node; its first named child is the constant.
				var superConst *sitter.Node
				for i := 0; i < int(superNode.NamedChildCount()); i++ {
					c := superNode.NamedChild(i)
					if c.Type() == "constant" || c.Type() == "scope_resolution" {
						superConst = c
						break
					}
				}
				if superConst != nil {
					superName := ""
					if superConst.Type() == "constant" {
						superName = superConst.Content(ex.src)
					} else if superConst.Type() == "scope_resolution" {
						// Foo::Bar — use last component only for table lookup.
						if last := superConst.ChildByFieldName("name"); last != nil {
							superName = last.Content(ex.src)
						}
					}
					if superName != "" {
						if parentID, ok := ex.classTable[superName]; ok {
							ex.addEdge(graph.EdgeTypeInherits, classID, parentID,
								map[string]string{"via": "superclass"})
						} else {
							ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
								Service: ex.service, File: ex.file,
								Line: rbLine(superNode), Name: superName, Kind: "inherits_unresolved",
							})
						}
					}
				}
			}
		}
	case "method", "singleton_method":
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			name := nameNode.Content(ex.src)
			methodID = ex.methodNodeID(name, rbLine(node))
			meta := map[string]string{"class": class}
			// end_line lets comm-node enclosing attribution (linkRubyEnclosingCalls)
			// bound this method's body by line range rather than nearest-preceding.
			meta["end_line"] = fmt.Sprintf("%d", int(node.EndPoint().Row)+1)
			// X.2: qualified_name is the <Type>#<method> join key delayed_job's
			// dj_target (matcher.go) and jobs.yaml's contract rules match against.
			if class != "" {
				meta["qualified_name"] = class + "#" + name
			} else {
				meta["qualified_name"] = name
			}
			ex.addNode(graph.Node{
				ID: methodID, Type: graph.NodeTypeFunction, Label: name,
				Service: ex.service, File: ex.file, Line: rbLine(node), Language: "ruby",
				Meta: meta,
			})
		}
	case "assignment", "operator_assignment":
		left := node.ChildByFieldName("left")
		if left != nil {
			switch left.Type() {
			case "constant":
				// Top-level or class-level constant definition.
				if methodID == "" {
					name := left.Content(ex.src)
					ex.addNode(graph.Node{
						ID:   fmt.Sprintf("%s:%s:variable:%s:%d", ex.service, ex.file, name, rbLine(node)),
						Type: graph.NodeTypeVariable, Label: name,
						Service: ex.service, File: ex.file, Line: rbLine(node), Language: "ruby",
						Meta: map[string]string{
							"kind": "const", "scope": "module", "mutable": "false",
							"class": class,
						},
					})
				}
			case "instance_variable", "class_variable":
				id := ex.ivarNode(left.Content(ex.src), class, rbLine(node))
				if methodID != "" {
					ex.addEdge(graph.EdgeTypeWrites, methodID, id, map[string]string{"op": "assign"})
				}
			}
		}
	case "instance_variable", "class_variable":
		// A read unless it is the left side of an assignment (handled above).
		if parent := node.Parent(); parent != nil {
			pt := parent.Type()
			if (pt == "assignment" || pt == "operator_assignment") && parent.ChildByFieldName("left") == node {
				break
			}
			if methodID != "" {
				id := ex.ivarNode(node.Content(ex.src), class, rbLine(node))
				ex.addEdge(graph.EdgeTypeReads, methodID, id, nil)
			}
		}
	case "call":
		mn := node.ChildByFieldName("method")
		if mn == nil {
			break
		}
		mname := mn.Content(ex.src)
		switch mname {
		case "include", "extend", "prepend":
			// Mixin calls inside a class body (no receiver or self-implicit).
			receiver := node.ChildByFieldName("receiver")
			if classID != "" && methodID == "" && (receiver == nil || receiver.Content(ex.src) == "self") {
				if args := node.ChildByFieldName("arguments"); args != nil {
					for i := 0; i < int(args.NamedChildCount()); i++ {
						a := args.NamedChild(i)
						if a.Type() != "constant" {
							continue
						}
						modName := a.Content(ex.src)
						if modID, ok := ex.classTable[modName]; ok {
							ex.addEdge(graph.EdgeTypeInherits, classID, modID,
								map[string]string{"via": "mixin", "mixin": mname})
						} else {
							ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
								Service: ex.service, File: ex.file,
								Line: rbLine(a), Name: modName, Kind: "inherits_unresolved",
							})
						}
					}
				}
			}
		case "new":
			// Foo.new → instantiates from enclosing method.
			if methodID != "" {
				receiver := node.ChildByFieldName("receiver")
				if receiver != nil && receiver.Type() == "constant" {
					clsName := receiver.Content(ex.src)
					if clsID, ok := ex.classTable[clsName]; ok {
						edgeKey := fmt.Sprintf("instantiates:%s->%s", methodID, clsID)
						if !ex.edgeSeen[edgeKey] {
							ex.edgeSeen[edgeKey] = true
							ex.edges = append(ex.edges, graph.Edge{
								ID:         edgeKey,
								From:       methodID,
								To:         clsID,
								Type:       graph.EdgeTypeInstantiates,
								Confidence: graph.ConfidenceInferred,
								Meta:       map[string]string{"count": "1"},
							})
						}
					} // cross-file Foo.new resolved by LinkRubyTypeRelations
				}
			}
		default:
			// Bare/implicit-self method calls: helper(x), save, self.foo.
			// Receiver-typed calls (article.save) need static type inference
			// Ruby's dynamism rules out here, so they are left alone — same
			// scope restriction as the other patterns in this file (rule 9:
			// only attribute a call when the target is unambiguous).
			if methodID == "" {
				break
			}
			if receiver := node.ChildByFieldName("receiver"); receiver != nil && receiver.Content(ex.src) != "self" {
				break
			}
			targetID := ""
			if class != "" {
				targetID = ex.methodsByClassName[class+"\x00"+mname]
			}
			if targetID == "" {
				if ids := ex.methodsByName[mname]; len(ids) == 1 {
					targetID = ids[0]
				}
			}
			if targetID != "" {
				ex.addEdge(graph.EdgeTypeCalls, methodID, targetID, nil)
			} else {
				ex.unresolved = append(ex.unresolved, graph.UnresolvedRef{
					Service: ex.service, File: ex.file,
					Line: rbLine(node), Name: mname, Kind: "call_ref",
				})
			}
		}
	}

	for i := 0; i < int(node.NamedChildCount()); i++ {
		ex.walk(node.NamedChild(i), class, classID, methodID)
	}
}

// collectClass emits a class node with its method names and attr_* symbols.
func (ex *rubyExtractor) collectClass(node *sitter.Node, name string) {
	var methods, attrs []string
	if body := node.ChildByFieldName("body"); body != nil {
		for i := 0; i < int(body.NamedChildCount()); i++ {
			m := body.NamedChild(i)
			switch m.Type() {
			case "method":
				if mn := m.ChildByFieldName("name"); mn != nil {
					methods = append(methods, mn.Content(ex.src))
				}
			case "call":
				// attr_accessor :a, :b / attr_reader / attr_writer
				mn := m.ChildByFieldName("method")
				if mn == nil || !strings.HasPrefix(mn.Content(ex.src), "attr_") {
					continue
				}
				if args := m.ChildByFieldName("arguments"); args != nil {
					for j := 0; j < int(args.NamedChildCount()); j++ {
						a := args.NamedChild(j)
						if a.Type() == "simple_symbol" {
							attrs = append(attrs, strings.TrimPrefix(a.Content(ex.src), ":"))
						}
					}
				}
			}
		}
	}
	ex.addNode(graph.Node{
		ID:   fmt.Sprintf("%s:%s:class:%s:%d", ex.service, ex.file, name, rbLine(node)),
		Type: graph.NodeTypeClass, Label: name,
		Service: ex.service, File: ex.file, Line: rbLine(node), Language: "ruby",
		Meta: map[string]string{
			"methods": strings.Join(methods, ","),
			"attrs":   strings.Join(attrs, ","),
		},
	})
}
