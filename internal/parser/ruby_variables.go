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
func extractRubyVariables(file, service string, src []byte) ([]graph.Node, []graph.Edge) {
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil, nil
	}
	defer tree.Close()

	ex := &rubyExtractor{
		file: file, service: service, src: src,
		ivarDecl: map[string]int{},
		nodeSeen: map[string]bool{},
		edgeSeen: map[string]bool{},
	}
	ex.walk(tree.RootNode(), "", "")

	sort.Slice(ex.nodes, func(i, j int) bool { return ex.nodes[i].ID < ex.nodes[j].ID })
	sort.Slice(ex.edges, func(i, j int) bool { return ex.edges[i].ID < ex.edges[j].ID })
	return ex.nodes, ex.edges
}

type rubyExtractor struct {
	file, service string
	src           []byte

	ivarDecl map[string]int // "@name" (class-qualified) → first-seen line

	nodes    []graph.Node
	edges    []graph.Edge
	nodeSeen map[string]bool
	edgeSeen map[string]bool
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

// walk descends the AST carrying the enclosing class name and method node ID.
func (ex *rubyExtractor) walk(node *sitter.Node, class, methodID string) {
	switch node.Type() {
	case "class", "module":
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			name := nameNode.Content(ex.src)
			ex.collectClass(node, name)
			class = name
		}
	case "method", "singleton_method":
		if nameNode := node.ChildByFieldName("name"); nameNode != nil {
			name := nameNode.Content(ex.src)
			methodID = ex.methodNodeID(name, rbLine(node))
			ex.addNode(graph.Node{
				ID: methodID, Type: graph.NodeTypeFunction, Label: name,
				Service: ex.service, File: ex.file, Line: rbLine(node), Language: "ruby",
				Meta: map[string]string{"class": class},
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
						ID: fmt.Sprintf("%s:%s:variable:%s:%d", ex.service, ex.file, name, rbLine(node)),
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
	}

	for i := 0; i < int(node.NamedChildCount()); i++ {
		ex.walk(node.NamedChild(i), class, methodID)
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
		ID: fmt.Sprintf("%s:%s:class:%s:%d", ex.service, ex.file, name, rbLine(node)),
		Type: graph.NodeTypeClass, Label: name,
		Service: ex.service, File: ex.file, Line: rbLine(node), Language: "ruby",
		Meta: map[string]string{
			"methods": strings.Join(methods, ","),
			"attrs":   strings.Join(attrs, ","),
		},
	})
}
