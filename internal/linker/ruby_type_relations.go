package linker

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// LinkRubyTypeRelations resolves cross-file inherits (superclass + mixins) and
// instantiates (Foo.new) edges for Ruby. It scans each Ruby file for constant
// declarations and constant references, resolves each reference the way Ruby
// does — innermost enclosing namespace outward — and emits inferred edges.
// Collisions (a name that stays ambiguous after lexical resolution) emit a
// candidate edge per definition plus a ledger entry.
//
// Tier K.7a: the class table is keyed by service. Ruby constant lookup is
// process-local, so a class in service A can never inherit from — or mix in, or
// instantiate — a class defined in separately deployed service B. Resolving bare
// constant names workspace-wide produced 744 phantom cross-service edges in the
// 8-service juniper fleet, where all three Ruby repos vendor their own copy of
// lib/dx.rb and app/.../api_base_controller.rb: a single `include Dx` in orion
// bound to the Vega-Agent and Lyra-Agent copies as well, minting 221 edges from one
// statement. An unresolvable constant is ledgered, never bound across a service.
//
// Scoping to a service is not enough on its own, because one service is free to
// declare the same simple name twice under different namespaces. `class Foo <
// Bar` used to be resolved by Bar's *last component* against a table keyed by
// simple name, so a reference from inside `ClientApi::V1` and one from the top
// level were indistinguishable: orion's two RepositoryControllers collapsed
// into one another and a subclass silently acquired the wrong ancestor chain
// (the same trap fixed for the filter chain in e8e0daf — see resolveSuper in
// rails_filters.go). Namespaces are tracked during the scan and a reference now
// binds to the nearest enclosing definition; the simple-name table survives only
// as the fallback for constants this pass never saw declared.
func LinkRubyTypeRelations(nodes []graph.Node, serviceFiles map[string][]string) ([]graph.Edge, []graph.UnresolvedRef) {
	// Per-service class tables, both keyed off the nodes the parser emitted so a
	// target is always a node that exists:
	//   byName  label → []nodeID, the pre-namespace fallback
	//   byDecl  file\x00label\x00line → nodeID, the join the scan uses to attach
	//           a qualified name to a node it did not create
	byNameByService := make(map[string]map[string][]string)
	byDeclByService := make(map[string]map[string]string)
	fileByID := make(map[string]string)
	total := 0
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
		total++
	}
	if total == 0 {
		return nil, nil
	}
	methodsByClass := buildMethodsByClass(nodes)

	// Service order decides edge order, so it cannot come from a map.
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
			svc:            svcName,
			byName:         byNameByService[svcName],
			byQual:         map[string][]string{},
			fileByID:       fileByID,
			methodsByClass: methodsByClass,
		}
		if ix.byName == nil {
			ix.byName = map[string][]string{}
		}
		byDecl := byDeclByService[svcName]

		// Pass 1: every declaration in the service, with the namespace it sits in.
		// A reference cannot be resolved until the whole service has been read —
		// the definition it names is usually in another file. The per-file scan
		// is parallel; the merge below stays in file order.
		type rubyTypeScan struct {
			decls []rubyDecl
			refs  []rubyTypeRef
		}
		scans := mapParallel(filterRubyFiles(files), func(file string) rubyTypeScan {
			d, r := scanRubyTypes(file, svcName)
			return rubyTypeScan{decls: d, refs: r}
		})
		var refs []rubyTypeRef
		for _, s := range scans {
			for _, d := range s.decls {
				id, ok := byDecl[declKey(d.file, d.name, d.line)]
				if !ok {
					continue // no node for it; the byName fallback still covers the name
				}
				ix.byQual[d.qualified()] = append(ix.byQual[d.qualified()], id)
			}
			refs = append(refs, s.refs...)
		}

		// Pass 2: resolve.
		for _, ref := range refs {
			edges, unresolved := ix.emit(ref, seen)
			allEdges = append(allEdges, edges...)
			allUnresolved = append(allUnresolved, unresolved...)
		}
	}
	return allEdges, allUnresolved
}

func declKey(file, label string, line int) string {
	return fmt.Sprintf("%s\x00%s\x00%d", file, label, line)
}

func isRubyFile(file string) bool {
	ext := strings.ToLower(filepath.Ext(file))
	return ext == ".rb" || ext == ".rake"
}

// isERBFile reports a Rails view. Kept distinct from isRubyFile — that gate
// also covers passes that os.ReadFile + rubysitter.ParseCtx the file
// directly, which would produce garbage on ERB's mixed markup/Ruby content;
// only passes that consume already-extracted virtualRuby content (or, like
// LinkRubyMixinMethods, already-emitted UnresolvedRef entries) can accept it.
func isERBFile(file string) bool {
	return strings.ToLower(filepath.Ext(file)) == ".erb"
}

// ---------------------------------------------------------------------------
// scan
// ---------------------------------------------------------------------------

// rubyDecl is one `class`/`module` declaration and where it sits. `name` is the
// constant exactly as written, which is what the parser used for the node label,
// so `class ClientApi::V1::Base` keeps its compound name and picks up no
// namespace from an enclosing block it does not have.
type rubyDecl struct {
	name string
	ns   []string // enclosing module/class names, outermost first
	file string
	line int
}

func (d rubyDecl) qualified() string {
	if len(d.ns) == 0 {
		return d.name
	}
	return strings.Join(d.ns, "::") + "::" + d.name
}

// rubyTypeRef is one constant reference to resolve, carrying the namespace it
// was written in. Without the namespace the reference is just a name and the
// resolver has to guess.
type rubyTypeRef struct {
	ref      string // as written: `Base`, `ClientApi::V1::Base`, `::Base`
	ns       []string
	file     string
	line     int
	fromID   string
	edgeType graph.EdgeType
	meta     map[string]string
	missKind string
	// sameFile names declared in the reference's own file. Same-file relations
	// are already emitted by extractRubyVariables, so they are dropped here
	// rather than duplicated.
	sameFile map[string]bool
}

func scanRubyTypes(file, svcName string) ([]rubyDecl, []rubyTypeRef) {
	src, root, release, ok := rubyParse(file)
	file = patterns.RelativizeToCwd(file)
	if !ok {
		return nil, nil
	}
	defer release()

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
	var refs []rubyTypeRef
	addRef := func(ref string, ns []string, fromID string, et graph.EdgeType, meta map[string]string, line int, missKind string) {
		if ref == "" || fromID == "" {
			return
		}
		refs = append(refs, rubyTypeRef{
			ref: ref, ns: append([]string{}, ns...), file: file, line: line,
			fromID: fromID, edgeType: et, meta: meta, missKind: missKind,
			sameFile: sameFile,
		})
	}

	var walk func(n *sitter.Node, ns []string, classID, methodID string)
	walk = func(n *sitter.Node, ns []string, classID, methodID string) {
		inner := ns
		switch n.Type() {
		case "class", "module":
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				clsName := nameNode.Content(src)
				line := int(n.StartPoint().Row) + 1
				decls = append(decls, rubyDecl{name: clsName, ns: ns, file: file, line: line})
				classID = fmt.Sprintf("%s:%s:class:%s:%d", svcName, file, clsName, line)

				if superNode := n.ChildByFieldName("superclass"); superNode != nil {
					if ref := constRef(superNode, src); ref != "" {
						addRef(ref, ns, classID, graph.EdgeTypeInherits,
							map[string]string{"via": "superclass"},
							int(superNode.StartPoint().Row)+1, "inherits_unresolved")
					}
				}

				// Mixins are declared in the class body, so they resolve in the
				// namespace *inside* the declaration, not outside it.
				bodyNS := append(append([]string{}, ns...), strings.Split(clsName, "::")...)
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
						args := m.ChildByFieldName("arguments")
						if args == nil {
							continue
						}
						for j := 0; j < int(args.NamedChildCount()); j++ {
							a := args.NamedChild(j)
							ref := constRef(a, src)
							if ref == "" {
								continue
							}
							addRef(ref, bodyNS, classID, graph.EdgeTypeInherits,
								map[string]string{"via": "mixin", "mixin": mname},
								int(a.StartPoint().Row)+1, "inherits_unresolved")
						}
					}
				}
				inner = bodyNS
			}
		case "method", "singleton_method":
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				methodID = fmt.Sprintf("%s:%s:function:%s:%d", svcName, file,
					nameNode.Content(src), int(n.StartPoint().Row)+1)
			}
		case "call":
			mn := n.ChildByFieldName("method")
			if mn != nil && mn.Content(src) == "new" && methodID != "" {
				if recv := n.ChildByFieldName("receiver"); recv != nil && recv.Type() == "constant" {
					addRef(recv.Content(src), ns, methodID, graph.EdgeTypeInstantiates,
						map[string]string{"count": "1"},
						int(recv.StartPoint().Row)+1, "instantiates_unresolved")
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i), inner, classID, methodID)
		}
	}
	walk(root, nil, "", "")
	return decls, refs
}

// constRef reads the constant a node names, keeping every component.
//
// The superclass path used to take the last component of a scope_resolution and
// throw the rest away, which is exactly the information that tells
// `ClientApi::V1::ApiBaseController` apart from a top-level `ApiBaseController`.
// A `superclass` node wraps its constant, so unwrap one level before reading.
func constRef(n *sitter.Node, src []byte) string {
	switch n.Type() {
	case "constant", "scope_resolution":
		return n.Content(src)
	case "superclass":
		for i := 0; i < int(n.NamedChildCount()); i++ {
			if ref := constRef(n.NamedChild(i), src); ref != "" {
				return ref
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// resolve
// ---------------------------------------------------------------------------

type rubyTypeIndex struct {
	svc      string
	byName   map[string][]string // label → node IDs, simple-name fallback
	byQual   map[string][]string // fully qualified name → node IDs
	fileByID map[string]string

	// methodsByClass is classID + "\x00" + methodName → method node IDs (see
	// buildMethodsByClass). Only LinkRubyTypeRelations populates it, to emit a
	// method-granularity `calls` edge alongside a class-granularity
	// `instantiates` edge when the instantiated class declares `initialize`.
	// Left nil for every other caller of rubyTypeIndex, which is safe: a nil
	// map read is just an empty lookup.
	methodsByClass map[string][]string
}

// resolve finds the definitions a constant reference names, innermost enclosing
// namespace first and then outward, the way Ruby's own lookup walks Module.nesting.
func (ix *rubyTypeIndex) resolve(ref string, ns []string) []string {
	if strings.HasPrefix(ref, "::") {
		// `::Foo` is explicitly top level and must not pick up a nesting.
		ref = strings.TrimPrefix(ref, "::")
		if ids := ix.byQual[ref]; len(ids) > 0 {
			return ids
		}
		return ix.byName[ref]
	}
	for i := len(ns); i >= 0; i-- {
		key := ref
		if i > 0 {
			key = strings.Join(ns[:i], "::") + "::" + ref
		}
		if ids := ix.byQual[key]; len(ids) > 0 {
			return ids
		}
	}
	if strings.Contains(ref, "::") {
		// The reference says which namespace it means and no declaration in this
		// service answers to it, so it belongs to a gem or a framework: `class
		// Application < Rails::Application`. Falling back to the last component
		// is the information loss this whole pass exists to undo, and it is not
		// harmless — it bound orion's `Orion::Application` to the unrelated
		// `module Application` in config/initializers/version.rb. Ledger it.
		return nil
	}
	// Nothing in the service declares it under any enclosing namespace. For a
	// bare name the simple-name table is the last resort: it is how this pass
	// behaved throughout, and dropping it would cost the constants whose
	// declaration this pass never parsed. It stays ambiguity-aware — several
	// matches mean several candidate edges and a ledger entry, not a
	// first-match guess.
	return ix.byName[ref]
}

func (ix *rubyTypeIndex) emit(r rubyTypeRef, seen map[string]bool) ([]graph.Edge, []graph.UnresolvedRef) {
	var edges []graph.Edge
	var unresolved []graph.UnresolvedRef

	targets := ix.resolve(r.ref, r.ns)
	if len(targets) == 0 {
		if !r.sameFile[r.ref] {
			missKey := fmt.Sprintf("%s:%s:%s", r.file, r.ref, r.missKind)
			if !seen[missKey] {
				seen[missKey] = true
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: ix.svc, File: r.file, Line: r.line,
					Name: r.ref, Kind: r.missKind,
				})
			}
		}
		return nil, unresolved
	}

	// Same-file relations are extractRubyVariables' job. Filtering by the
	// resolved target's file rather than by name keeps a reference that resolves
	// out of the file even when a same-named class happens to sit in it.
	kept := targets[:0:0]
	for _, id := range targets {
		if ix.fileByID[id] != r.file {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		return nil, nil
	}

	if len(kept) > 1 {
		missKey := fmt.Sprintf("collision:%s:%s:%s", r.file, r.ref, r.missKind)
		if !seen[missKey] {
			seen[missKey] = true
			unresolved = append(unresolved, graph.UnresolvedRef{
				Service: ix.svc, File: r.file, Line: r.line,
				Name: r.ref, Kind: "inherits_unresolved",
			})
		}
	}
	for _, targetID := range kept {
		eid := fmt.Sprintf("%s:%s->%s", string(r.edgeType), r.fromID, targetID)
		if seen[eid] {
			continue
		}
		seen[eid] = true
		conf := graph.ConfidenceInferred
		if len(kept) > 1 {
			conf = graph.ConfidencePartial
		}
		edges = append(edges, graph.Edge{
			ID: eid, From: r.fromID, To: targetID,
			Type: r.edgeType, Confidence: conf, Meta: r.meta,
		})

		// Additive: alongside the class-granularity `instantiates` edge, also
		// land a method-granularity `calls` edge on the class's own
		// `initialize` when it declares one, the same lookup
		// LinkRubyClassMethodCalls does for `ClassName.method_name`. A
		// constructor that does real work (validates args, sets up state)
		// otherwise reads as unreachable dead code even when something in the
		// same service instantiates it every time. No node is fabricated when
		// the class has no explicit `initialize` (Ruby's implicit default
		// takes no edge).
		if r.edgeType == graph.EdgeTypeInstantiates {
			for _, mID := range ix.methodsByClass[targetID+"\x00initialize"] {
				ceid := fmt.Sprintf("calls:%s->%s", r.fromID, mID)
				if seen[ceid] {
					continue
				}
				seen[ceid] = true
				edges = append(edges, graph.Edge{
					ID: ceid, From: r.fromID, To: mID,
					Type: graph.EdgeTypeCalls, Confidence: conf,
					Meta: map[string]string{"via": "instantiate_initialize"},
				})
			}
		}
	}
	return edges, unresolved
}
