package linker

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// LinkRubyClassMethodCalls resolves cross-file `ClassName.method_name` calls.
// extractRubyVariables (internal/parser/ruby_variables.go) already resolves
// this shape when ClassName is declared in the same file as the call site;
// this pass covers the cross-file case the same way LinkRubyTypeRelations
// covers cross-file `inherits`/`instantiates` for the same receiver-typed
// shape.
//
// Two outcomes per resolved receiver class:
//   - It declares a method by that name (`def self.x`, or any `def x` — the
//     graph does not distinguish singleton from instance methods, the same
//     imprecision the same-file parser fix already accepts) → a method-level
//     `calls` edge, exactly what a same-file call would get.
//   - It doesn't — an ActiveRecord finder, `create!`, a `scope :x, -> {}`
//     macro (which the parser never turns into a method node at all) — a
//     class-granularity `calls` edge to the class node itself, so a
//     blast-radius query still reaches the model even with no method node to
//     land on. This is the same granularity EdgeTypeInstantiates already
//     gives a bare `Foo.new`.
//
// Nothing is ledgered when the receiver constant does not resolve to any
// class node this service declares: that is not a same-file miss this pass
// somehow owns, it's Rails/Ruby-stdlib/gem code (`Rails.logger`,
// `ActiveRecord::Base`) no repository defines, and ledgering every such call
// site would just be the same noise LinkRubyTypeRelations already avoids for
// inherits/instantiates.
//
// Scoped to a service for the reason LinkRubyTypeRelations is: Ruby constant
// lookup is process-local, so a call in service A can never resolve to a
// class in a separately deployed service B.
func LinkRubyClassMethodCalls(nodes []graph.Node, serviceFiles map[string][]string) ([]graph.Edge, []graph.UnresolvedRef) {
	byNameByService := make(map[string]map[string][]string)
	byDeclByService := make(map[string]map[string]string)
	fileByID := make(map[string]string)
	classTotal := 0
	classIDByFileLabel := make(map[string]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeClass {
			continue
		}
		byName := byNameByService[n.Service]
		if byName == nil {
			byName = make(map[string][]string)
			byNameByService[n.Service] = byName
			byDeclByService[n.Service] = make(map[string]string)
		}
		byName[n.Label] = append(byName[n.Label], n.ID)
		byDeclByService[n.Service][declKey(n.File, n.Label, n.Line)] = n.ID
		fileByID[n.ID] = n.File
		classIDByFileLabel[n.File+"\x00"+n.Label] = n.ID
		classTotal++
	}
	if classTotal == 0 {
		return nil, nil
	}

	// Method ownership: classID + "\x00" + methodName → method node IDs.
	// Keyed off Meta["class"], the same join linkRubyClassMembers uses — a
	// reopened class in another file is a different node with its own
	// methods, which is correct: the edge below lands on whichever
	// declaration this service's constant resolution actually names.
	methodsByClass := make(map[string][]string)
	for i := range nodes {
		n := &nodes[i]
		if (n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeMethod) || n.Meta["class"] == "" {
			continue
		}
		clsID, ok := classIDByFileLabel[n.File+"\x00"+n.Meta["class"]]
		if !ok {
			continue
		}
		key := clsID + "\x00" + n.Label
		methodsByClass[key] = append(methodsByClass[key], n.ID)
	}
	for k := range methodsByClass {
		sort.Strings(methodsByClass[k])
	}

	svcNames := make([]string, 0, len(serviceFiles))
	for svcName := range serviceFiles {
		svcNames = append(svcNames, svcName)
	}
	sort.Strings(svcNames)

	var allEdges []graph.Edge
	var allUnresolved []graph.UnresolvedRef
	seen := make(map[string]bool)

	for _, svcName := range svcNames {
		files := append([]string{}, serviceFiles[svcName]...)
		sort.Strings(files)

		ix := &rubyTypeIndex{
			svc:      svcName,
			byName:   byNameByService[svcName],
			byQual:   map[string][]string{},
			fileByID: fileByID,
		}
		if ix.byName == nil {
			ix.byName = map[string][]string{}
		}
		byDecl := byDeclByService[svcName]

		var refs []classMethodCallRef
		for _, file := range files {
			if !isRubyFile(file) {
				continue
			}
			decls, fileRefs := scanRubyClassMethodCalls(file, svcName)
			for _, d := range decls {
				id, ok := byDecl[declKey(d.file, d.name, d.line)]
				if !ok {
					continue
				}
				ix.byQual[d.qualified()] = append(ix.byQual[d.qualified()], id)
			}
			refs = append(refs, fileRefs...)
		}

		for _, ref := range refs {
			edges, unresolved := emitClassMethodCall(ix, ref, methodsByClass, seen)
			allEdges = append(allEdges, edges...)
			allUnresolved = append(allUnresolved, unresolved...)
		}
	}
	return allEdges, allUnresolved
}

func emitClassMethodCall(
	ix *rubyTypeIndex,
	ref classMethodCallRef,
	methodsByClass map[string][]string,
	seen map[string]bool,
) ([]graph.Edge, []graph.UnresolvedRef) {
	targets := ix.resolve(ref.receiver, ref.ns)
	if len(targets) == 0 {
		return nil, nil
	}

	// Same-file relations are extractRubyVariables' job (Tier: the same-file
	// class-method-call fix). Filtering by the resolved target's file rather
	// than by name keeps a reference that resolves out of the file even when
	// a same-named class happens to sit in it.
	kept := targets[:0:0]
	for _, id := range targets {
		if ix.fileByID[id] != ref.file {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		return nil, nil
	}

	var unresolved []graph.UnresolvedRef
	conf := graph.ConfidenceInferred
	if len(kept) > 1 {
		conf = graph.ConfidencePartial
		missKey := fmt.Sprintf("classmethod_collision:%s:%d:%s.%s", ref.file, ref.line, ref.receiver, ref.mname)
		if !seen[missKey] {
			seen[missKey] = true
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: ix.svc, File: ref.file, Line: ref.line,
				Name: ref.receiver + "." + ref.mname, Kind: "class_method_call_collision",
			})
		}
	}

	var edges []graph.Edge
	for _, classID := range kept {
		methodTargets := methodsByClass[classID+"\x00"+ref.mname]
		if len(methodTargets) > 0 {
			for _, mID := range methodTargets {
				eid := fmt.Sprintf("calls:%s->%s", ref.fromID, mID)
				if seen[eid] {
					continue
				}
				seen[eid] = true
				edges = append(edges, graph.Edge{
					ID: eid, From: ref.fromID, To: mID,
					Type: graph.EdgeTypeCalls, Confidence: conf,
					Meta: map[string]string{"via": "class_method_call"},
				})
			}
			continue
		}
		// No user-defined method by that name — an ActiveRecord finder, a
		// `scope` macro, or any other class-level DSL the parser never turns
		// into a method node. A class-granularity edge keeps the model
		// reachable for blast radius anyway (see doc comment).
		eid := fmt.Sprintf("calls:%s->%s:class", ref.fromID, classID)
		if seen[eid] {
			continue
		}
		seen[eid] = true
		edges = append(edges, graph.Edge{
			ID: eid, From: ref.fromID, To: classID,
			Type: graph.EdgeTypeCalls, Confidence: conf,
			Meta: map[string]string{"via": "class_method_call", "granularity": "class", "method": ref.mname},
		})
	}
	return edges, unresolved
}

// ---------------------------------------------------------------------------
// scan
// ---------------------------------------------------------------------------

// classMethodCallRef is one `Constant.method_name(...)` call site to resolve.
type classMethodCallRef struct {
	receiver string // constant as written: `Product`, `ClientApi::V1::Product`
	ns       []string
	file     string
	line     int
	fromID   string // enclosing method node ID (the caller)
	mname    string
}

// scanRubyClassMethodCalls walks file once for both class declarations (with
// namespace, for LinkRubyClassMethodCalls' resolver) and constant-receiver
// method calls. `new`/`include`/`extend`/`prepend` are excluded: those
// receiver shapes are already resolved elsewhere (instantiates/inherits).
func scanRubyClassMethodCalls(file, svcName string) ([]rubyDecl, []classMethodCallRef) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil
	}
	file = patterns.RelativizeToCwd(file)
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil, nil
	}
	defer tree.Close()
	root := tree.RootNode()

	// Same-file receivers are extractRubyVariables' job; skip them here so
	// this pass never emits a shadow/duplicate edge for a call the parser
	// already resolved (or deliberately left unresolved) same-file.
	sameFile := map[string]bool{}
	var collectNames func(n *sitter.Node)
	collectNames = func(n *sitter.Node) {
		if t := n.Type(); t == "class" || t == "module" {
			if nn := n.ChildByFieldName("name"); nn != nil {
				sameFile[nn.Content(src)] = true
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			collectNames(n.NamedChild(i))
		}
	}
	collectNames(root)

	var decls []rubyDecl
	var refs []classMethodCallRef

	var walk func(n *sitter.Node, ns []string, methodID string)
	walk = func(n *sitter.Node, ns []string, methodID string) {
		inner := ns
		switch n.Type() {
		case "class", "module":
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				clsName := nameNode.Content(src)
				line := int(n.StartPoint().Row) + 1
				decls = append(decls, rubyDecl{name: clsName, ns: ns, file: file, line: line})
				inner = append(append([]string{}, ns...), strings.Split(clsName, "::")...)
			}
		case "method", "singleton_method":
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				methodID = fmt.Sprintf("%s:%s:function:%s:%d", svcName, file,
					nameNode.Content(src), int(n.StartPoint().Row)+1)
			}
		case "call":
			if methodID != "" {
				if mn := n.ChildByFieldName("method"); mn != nil {
					mname := mn.Content(src)
					switch mname {
					case "new", "include", "extend", "prepend":
						// resolved elsewhere (instantiates/inherits)
					default:
						if recv := n.ChildByFieldName("receiver"); recv != nil && recv.Type() == "constant" {
							ref := recv.Content(src)
							if !sameFile[ref] {
								refs = append(refs, classMethodCallRef{
									receiver: ref, ns: append([]string{}, ns...),
									file: file, line: int(n.StartPoint().Row) + 1,
									fromID: methodID, mname: mname,
								})
							}
						}
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i), inner, methodID)
		}
	}
	walk(root, nil, "")
	return decls, refs
}
