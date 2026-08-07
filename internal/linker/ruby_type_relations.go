package linker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// LinkRubyTypeRelations resolves cross-file inherits (superclass + mixins) and
// instantiates (Foo.new) edges for Ruby. It re-parses each Ruby file, looks up
// constant names in the service-level class table, and emits inferred edges.
// Collisions (same class name in N files of the SAME service) emit N candidate
// edges + a ledger entry.
//
// Tier K.7a: the class table is keyed by service. Ruby constant lookup is
// process-local, so a class in service A can never inherit from — or mix in, or
// instantiate — a class defined in separately deployed service B. Resolving bare
// constant names workspace-wide produced 744 phantom cross-service edges in the
// 8-service juniper fleet, where all three Ruby repos vendor their own copy of
// lib/dx.rb and app/.../api_base_controller.rb: a single `include Dx` in orion
// bound to the Vega-Agent and Lyra-Agent copies as well, minting 221 edges from one
// statement. An unresolvable constant is ledgered, never bound across a service.
func LinkRubyTypeRelations(nodes []graph.Node, serviceFiles map[string][]string) ([]graph.Edge, []graph.UnresolvedRef) {
	// Build per-service class table: service → name → []nodeID (collision-aware).
	classTableByService := make(map[string]map[string][]string)
	total := 0
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeClass {
			continue
		}
		byName := classTableByService[n.Service]
		if byName == nil {
			byName = make(map[string][]string)
			classTableByService[n.Service] = byName
		}
		byName[n.Label] = append(byName[n.Label], n.ID)
		total++
	}
	if total == 0 {
		return nil, nil
	}

	var allEdges []graph.Edge
	var allUnresolved []graph.UnresolvedRef
	seen := make(map[string]bool)

	for svcName, files := range serviceFiles {
		// Only this service's classes are candidate binding targets.
		classTableMulti := classTableByService[svcName]
		if classTableMulti == nil {
			classTableMulti = map[string][]string{}
		}
		for _, file := range files {
			if !isRubyFile(file) {
				continue
			}
			edges, unresolved := resolveRubyTypeRelations(file, svcName, classTableMulti, seen)
			allEdges = append(allEdges, edges...)
			allUnresolved = append(allUnresolved, unresolved...)
		}
	}
	return allEdges, allUnresolved
}

func isRubyFile(file string) bool {
	ext := strings.ToLower(filepath.Ext(file))
	return ext == ".rb" || ext == ".rake"
}

func resolveRubyTypeRelations(file, svcName string, classTableMulti map[string][]string, seen map[string]bool) ([]graph.Edge, []graph.UnresolvedRef) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil
	}
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil, nil
	}
	defer tree.Close()
	root := tree.RootNode()

	// Build per-file class table for same-file constant resolution (skip cross-file only).
	fileClassNames := make(map[string]bool)
	var collectNames func(n *sitter.Node)
	collectNames = func(n *sitter.Node) {
		t := n.Type()
		if t == "class" || t == "module" {
			if nn := n.ChildByFieldName("name"); nn != nil {
				fileClassNames[nn.Content(src)] = true
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			collectNames(n.NamedChild(i))
		}
	}
	collectNames(root)

	// emitTypeEdge emits an edge to each matching node (collision → candidate edges + ledger).
	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef
	emitTypeEdge := func(edgeType graph.EdgeType, fromID, constName string, meta map[string]string, line int, missKind string) {
		targets, found := classTableMulti[constName]
		if !found {
			// Not in service at all — emit ledger entry if not same-file.
			if !fileClassNames[constName] {
				missKey := fmt.Sprintf("%s:%s:%s", file, constName, missKind)
				if !seen[missKey] {
					seen[missKey] = true
					unresolved = append(unresolved, graph.UnresolvedRef{
						Service: svcName, File: file, Line: line,
						Name: constName, Kind: missKind,
					})
				}
			}
			return
		}
		if fileClassNames[constName] {
			return // same-file: already handled by extractRubyVariables
		}
		if len(targets) > 1 {
			// Collision: emit candidate edge to each + ledger entry.
			missKey := fmt.Sprintf("collision:%s:%s:%s", file, constName, missKind)
			if !seen[missKey] {
				seen[missKey] = true
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: svcName, File: file, Line: line,
					Name: constName, Kind: "inherits_unresolved",
				})
			}
		}
		for _, targetID := range targets {
			eid := fmt.Sprintf("%s:%s->%s", string(edgeType), fromID, targetID)
			if !seen[eid] {
				seen[eid] = true
				conf := graph.ConfidenceInferred
				if len(targets) > 1 {
					conf = graph.ConfidencePartial
				}
				edges = append(edges, graph.Edge{
					ID: eid, From: fromID, To: targetID,
					Type: edgeType, Confidence: conf, Meta: meta,
				})
			}
		}
	}

	// Walk the AST to find class declarations, mixins, and Foo.new.
	var walk func(n *sitter.Node, classID, methodID string)
	walk = func(n *sitter.Node, classID, methodID string) {
		t := n.Type()
		switch t {
		case "class", "module":
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				clsName := nameNode.Content(src)
				newClassID := fmt.Sprintf("%s:%s:class:%s:%d", svcName, file, clsName, int(n.StartPoint().Row)+1)

				// Superclass → cross-file inherits.
				if superNode := n.ChildByFieldName("superclass"); superNode != nil {
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
							superName = superConst.Content(src)
						} else if superConst.Type() == "scope_resolution" {
							if last := superConst.ChildByFieldName("name"); last != nil {
								superName = last.Content(src)
							}
						}
						if superName != "" {
							emitTypeEdge(graph.EdgeTypeInherits, newClassID, superName,
								map[string]string{"via": "superclass"},
								int(superNode.StartPoint().Row)+1, "inherits_unresolved")
						}
					}
				}

				// Walk body for mixin calls.
				if body := n.ChildByFieldName("body"); body != nil {
					for i := 0; i < int(body.NamedChildCount()); i++ {
						m := body.NamedChild(i)
						if m.Type() != "call" {
							continue
						}
						mn := m.ChildByFieldName("method")
						if mn == nil {
							continue
						}
						mname := mn.Content(src)
						if mname != "include" && mname != "extend" && mname != "prepend" {
							continue
						}
						if args := m.ChildByFieldName("arguments"); args != nil {
							for j := 0; j < int(args.NamedChildCount()); j++ {
								a := args.NamedChild(j)
								if a.Type() != "constant" {
									continue
								}
								modName := a.Content(src)
								emitTypeEdge(graph.EdgeTypeInherits, newClassID, modName,
									map[string]string{"via": "mixin", "mixin": mname},
									int(a.StartPoint().Row)+1, "inherits_unresolved")
							}
						}
					}
				}
				classID = newClassID
			}
		case "method", "singleton_method":
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				mName := nameNode.Content(src)
				methodID = fmt.Sprintf("%s:%s:function:%s:%d", svcName, file, mName, int(n.StartPoint().Row)+1)
			}
		case "call":
			// Foo.new → cross-file instantiates.
			mn := n.ChildByFieldName("method")
			if mn != nil && mn.Content(src) == "new" && methodID != "" {
				recv := n.ChildByFieldName("receiver")
				if recv != nil && recv.Type() == "constant" {
					clsName := recv.Content(src)
					emitTypeEdge(graph.EdgeTypeInstantiates, methodID, clsName,
						map[string]string{"count": "1"},
						int(recv.StartPoint().Row)+1, "instantiates_unresolved")
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i), classID, methodID)
		}
	}
	walk(root, "", "")
	return edges, unresolved
}
