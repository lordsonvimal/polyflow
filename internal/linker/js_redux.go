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

// LinkJSRedux models the Redux dispatch chain that the structural JS parser
// cannot see on its own (Tier JCM.3):
//
//	component handler --calls--> ActionCreators.setFoo
//	    --references--> ActionTypes.SET_FOO
//	        --references--> FooReducer      (case ActionTypes.SET_FOO: …)
//
// It is recall-oriented: every edge is ConfidenceInferred and tagged
// Meta["tier"]="jcm3" / Meta["redux"]=<role> so verification filters can drop
// the whole tier. Nothing here needs a graph-schema change — action-type
// constants that no parser captured (keyMirror keys, string consts) are minted
// as synthetic `variable` nodes so the chain has stable endpoints.
//
// Recognised shapes (path-gated, then structure-gated):
//   - constants/*ActionTypes* : keyMirror({ A: null }), const A = "STR"
//   - actions/**              : name: actionCreator(ActionTypes.A, …),
//     function creator() { return { type: ActionTypes.A } }
//   - reducers/**             : switch(action.type){ case ActionTypes.A: },
//     if (action.type === ActionTypes.A)
//   - containers              : combineReducers({ slice: ReducerFn }) →
//     synthetic redux_store node --contains--> each reducer
//   - components              : this.props.actions.X(…), this.props.X(…) for a
//     spread-bound creator X, and dispatch(AC.x(…)) /
//     this.props.dispatch(AC.x(…)) — when the file pulls in
//     an actions/** module (import or bindActionCreators)
//
// Edge roles (Meta["redux"]): creator_type, type_handled_by, reducer_slice,
// props_actions_dispatch, props_bound_dispatch, dispatch_call, dispatch_type.
func LinkJSRedux(nodes []graph.Node, serviceFiles map[string][]string) (newNodes []graph.Node, newEdges []graph.Edge) {
	// --- index existing nodes (File is the cwd-relative form the parser mints) ---
	declsByFile := make(map[string][]lineNode)      // file → decls sorted by line, for attribution
	declIndex := make(map[string]map[string]string) // file → label → nodeID
	fileNodeID := make(map[string]string)           // "service\x00file" → NodeTypeFile ID
	svcOfFile := make(map[string]string)            // file → service
	idToLabel := make(map[string]string)            // decl nodeID → label
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeVariable, graph.NodeTypeClass, graph.NodeTypeMethod:
			if n.Label == "(module)" {
				continue
			}
			declsByFile[n.File] = append(declsByFile[n.File], lineNode{line: n.Line, id: n.ID})
			if declIndex[n.File] == nil {
				declIndex[n.File] = make(map[string]string)
			}
			if _, ok := declIndex[n.File][n.Label]; !ok {
				declIndex[n.File][n.Label] = n.ID
			}
			idToLabel[n.ID] = n.Label
			if n.Service != "" {
				svcOfFile[n.File] = n.Service
			}
		case graph.NodeTypeFile:
			fileNodeID[n.Service+"\x00"+n.File] = n.ID
			if n.Service != "" {
				svcOfFile[n.File] = n.Service
			}
		}
	}
	for f := range declsByFile {
		sort.Slice(declsByFile[f], func(i, j int) bool { return declsByFile[f][i].line < declsByFile[f][j].line })
	}

	// --- file universe ---
	indexed := make(map[string]bool)
	type fileEnt struct{ abs, svc string }
	var jsFiles []fileEnt
	for svc, files := range serviceFiles {
		for _, f := range files {
			indexed[f] = true
			if isJSFile(f) {
				jsFiles = append(jsFiles, fileEnt{abs: f, svc: svc})
				if svc != "" {
					svcOfFile[patterns.RelativizeToCwd(f)] = svc
				}
			}
		}
	}

	seenNode := make(map[string]bool)
	seenEdge := make(map[string]bool)
	// actionTypeID: file → ACTION_NAME → nodeID
	actionTypeID := make(map[string]map[string]string)

	mkVar := func(svc, rel, name string, line int) string {
		if svc == "" {
			svc = svcOfFile[rel]
		}
		if id, ok := declIndex[rel][name]; ok {
			return id
		}
		id := fmt.Sprintf("%s:%s:variable:%s:%d", svc, rel, name, line)
		if !seenNode[id] {
			seenNode[id] = true
			newNodes = append(newNodes, graph.Node{
				ID: id, Type: graph.NodeTypeVariable, Label: name, Service: svc, File: rel,
				Line: line, EndLine: line, Language: "javascript",
				Meta: map[string]string{"redux_action_type": name, "redux": "action_type", "tier": "jcm3", "synthetic": "true"},
			})
		}
		return id
	}
	synthFn := make(map[string]string) // rel\x00name → synthetic fn nodeID (line-independent)
	mkFn := func(svc, rel, name string, line int, role string) string {
		if svc == "" {
			svc = svcOfFile[rel]
		}
		if id, ok := declIndex[rel][name]; ok {
			return id
		}
		if id, ok := synthFn[rel+"\x00"+name]; ok {
			return id
		}
		id := fmt.Sprintf("%s:%s:function:%s:%d", svc, rel, name, line)
		synthFn[rel+"\x00"+name] = id
		if !seenNode[id] {
			seenNode[id] = true
			newNodes = append(newNodes, graph.Node{
				ID: id, Type: graph.NodeTypeFunction, Label: name, Service: svc, File: rel,
				Line: line, EndLine: line, Language: "javascript",
				Meta: map[string]string{"redux": role, "tier": "jcm3", "synthetic": "true"},
			})
		}
		return id
	}
	addEdge := func(t graph.EdgeType, from, to, role string) {
		if from == "" || to == "" || from == to {
			return
		}
		eid := fmt.Sprintf("%s:%s->%s", t, from, to)
		if seenEdge[eid] {
			return
		}
		seenEdge[eid] = true
		newEdges = append(newEdges, graph.Edge{
			ID: eid, From: from, To: to, Type: t, Confidence: graph.ConfidenceInferred,
			Meta: map[string]string{"redux": role, "tier": "jcm3"},
		})
	}

	// === Phase 1: action-type constants ===
	// Types-path files: keyMirror keys + SCREAMING string consts. Any other
	// file: only an unambiguous inline `const X = { FOO: "FOO", … }` action map
	// (all-string values, SCREAMING_CASE keys) — the hooks-era `useReducer`
	// shape where the type table lives next to the component.
	for _, fe := range jsFiles {
		rel := patterns.RelativizeToCwd(fe.abs)
		isTypes, _, _ := reduxPathRole(rel)
		src, root, _, ok := jsParse(fe.abs)
		if !ok {
			continue
		}
		var names map[string]int
		if isTypes {
			names = reduxCollectActionTypes(root, src)
		} else {
			names = reduxCollectInlineActionTypes(root, src)
		}
		for name, line := range names {
			if actionTypeID[rel] == nil {
				actionTypeID[rel] = make(map[string]string)
			}
			actionTypeID[rel][name] = mkVar(fe.svc, rel, name, line)
		}
	}

	// === Phase 2a: action creators ===
	// Builds the creator-name → node-ID registry that 2b's dispatch-site
	// detection needs, independent of the order files are visited in.
	creatorNodeID := make(map[string]map[string]string) // actionsRel → creatorName → nodeID
	recordCreator := func(rel, name, id string) {
		if name == "" || id == "" {
			return
		}
		if creatorNodeID[rel] == nil {
			creatorNodeID[rel] = make(map[string]string)
		}
		if _, ok := creatorNodeID[rel][name]; !ok {
			creatorNodeID[rel][name] = id
		}
	}
	for _, fe := range jsFiles {
		rel := patterns.RelativizeToCwd(fe.abs)
		if _, isActions, _ := reduxPathRole(rel); !isActions {
			continue
		}
		svc := fe.svc
		src, root, _, ok := jsParse(fe.abs)
		if !ok {
			continue
		}
		imports := reduxImports(root, src, fe.abs, indexed)
		resolveTypeRef := reduxTypeRefResolver(src, rel, imports, actionTypeID)
		attrFrom := func(line int) string {
			if id := nearestDecl(declsByFile[rel], line); id != "" {
				return id
			}
			return fileNodeID[svc+"\x00"+rel]
		}
		reduxWalk(root, func(n *sitter.Node) {
			switch n.Type() {
			case "pair":
				// creator:  foo: actionCreator(ActionTypes.FOO, …)
				key, val := n.ChildByFieldName("key"), n.ChildByFieldName("value")
				if key == nil || val == nil || val.Type() != "call_expression" {
					return
				}
				cf := val.ChildByFieldName("function")
				if cf == nil || !reduxIsActionCreatorCall(cf.Content(src)) {
					return
				}
				a := val.ChildByFieldName("arguments")
				if a == nil || a.NamedChildCount() == 0 {
					return
				}
				cname := strings.Trim(key.Content(src), "\"'`")
				cid := mkFn(svc, rel, cname, int(key.StartPoint().Row)+1, "creator")
				recordCreator(rel, cname, cid)
				addEdge(graph.EdgeTypeReferences, cid, resolveTypeRef(a.NamedChild(0)), "creator_type")

			case "object":
				// hand-written creator:  return { type: ActionTypes.FOO, … }
				for j := 0; j < int(n.NamedChildCount()); j++ {
					p := n.NamedChild(j)
					if p.Type() != "pair" {
						continue
					}
					k := p.ChildByFieldName("key")
					if k == nil || strings.Trim(k.Content(src), "\"'`") != "type" {
						continue
					}
					tid := resolveTypeRef(p.ChildByFieldName("value"))
					if tid == "" {
						continue
					}
					from := attrFrom(int(n.StartPoint().Row) + 1)
					addEdge(graph.EdgeTypeReferences, from, tid, "creator_type")
					recordCreator(rel, idToLabel[from], from)
				}
			}
		})
	}

	// === Phase 2b: reducers, dispatch sites, combineReducers slice maps ===
	for _, fe := range jsFiles {
		rel := patterns.RelativizeToCwd(fe.abs)
		svc := fe.svc
		src, root, _, ok := jsParse(fe.abs)
		if !ok {
			continue
		}
		_, _, isReducers := reduxPathRole(rel)
		imports := reduxImports(root, src, fe.abs, indexed)
		resolveTypeRef := reduxTypeRefResolver(src, rel, imports, actionTypeID)
		attrFrom := func(line int) string {
			if id := nearestDecl(declsByFile[rel], line); id != "" {
				return id
			}
			return fileNodeID[svc+"\x00"+rel]
		}

		// action-creator modules this file pulls in, plus the union of their
		// creator names → node IDs (for `props.<creator>()` / `dispatch(x())`).
		acFile := ""
		fileCreators := make(map[string]string)
		absorbAC := func(acRel string) {
			if acFile == "" {
				acFile = acRel
			}
			for nm, id := range creatorNodeID[acRel] {
				fileCreators[nm] = id
			}
		}
		for _, imp := range imports {
			if _, ia, _ := reduxPathRole(imp.file); ia {
				absorbAC(imp.file)
			}
		}
		// bindActionCreators(<ident>, …) where <ident> resolves to an actions module
		reduxWalk(root, func(n *sitter.Node) {
			if n.Type() != "call_expression" {
				return
			}
			f := n.ChildByFieldName("function")
			if f == nil || f.Type() != "identifier" || f.Content(src) != "bindActionCreators" {
				return
			}
			a := n.ChildByFieldName("arguments")
			if a == nil || a.NamedChildCount() == 0 {
				return
			}
			if a0 := a.NamedChild(0); a0.Type() == "identifier" {
				if imp, ok := imports[a0.Content(src)]; ok {
					if _, ia, _ := reduxPathRole(imp.file); ia {
						absorbAC(imp.file)
					}
				}
			}
		})

		// resolveCreatorCall turns `AC.setFoo(…)` / a bare imported `setFoo(…)`
		// into the creator node ID, or "" when the callee isn't an action creator.
		resolveCreatorCall := func(call *sitter.Node) string {
			if call == nil || call.Type() != "call_expression" {
				return ""
			}
			cf := call.ChildByFieldName("function")
			if cf == nil {
				return ""
			}
			switch cf.Type() {
			case "member_expression":
				o, p := cf.ChildByFieldName("object"), cf.ChildByFieldName("property")
				if o != nil && p != nil && o.Type() == "identifier" {
					if imp, ok := imports[o.Content(src)]; ok {
						if _, ia, _ := reduxPathRole(imp.file); ia {
							return reduxCreatorNode(imp.file, p.Content(src), creatorNodeID, svcOfFile, mkFn)
						}
					}
				}
			case "identifier":
				nm := cf.Content(src)
				if imp, ok := imports[nm]; ok {
					if _, ia, _ := reduxPathRole(imp.file); ia {
						exp := imp.exported
						if exp == "" {
							exp = nm
						}
						return reduxCreatorNode(imp.file, exp, creatorNodeID, svcOfFile, mkFn)
					}
				}
			}
			return ""
		}

		reduxWalk(root, func(n *sitter.Node) {
			switch n.Type() {
			case "switch_case":
				// reducer:  switch (action.type) { case ActionTypes.FOO: … }
				if !isReducers && !reduxSwitchesOnActionType(n, src) {
					return
				}
				if tid := resolveTypeRef(n.ChildByFieldName("value")); tid != "" {
					addEdge(graph.EdgeTypeReferences, tid, attrFrom(int(n.StartPoint().Row)+1), "type_handled_by")
				}

			case "binary_expression":
				// reducer:  action.type === ActionTypes.FOO
				op := n.ChildByFieldName("operator")
				if op == nil || (op.Content(src) != "===" && op.Content(src) != "==") {
					return
				}
				l, r := n.ChildByFieldName("left"), n.ChildByFieldName("right")
				if !isReducers && !reduxIsTypeAccess(l, src) && !reduxIsTypeAccess(r, src) {
					return
				}
				for _, side := range []*sitter.Node{l, r} {
					if tid := resolveTypeRef(side); tid != "" {
						addEdge(graph.EdgeTypeReferences, tid, attrFrom(int(n.StartPoint().Row)+1), "type_handled_by")
					}
				}

			case "call_expression":
				line := int(n.StartPoint().Row) + 1
				f := n.ChildByFieldName("function")
				if f == nil {
					return
				}

				// combineReducers({ slice: ReducerFn, … }) → store --contains--> reducer
				if f.Type() == "identifier" && f.Content(src) == "combineReducers" {
					a := n.ChildByFieldName("arguments")
					if a == nil || a.NamedChildCount() == 0 {
						return
					}
					obj := a.NamedChild(0)
					if obj.Type() == "identifier" {
						obj = reduxObjectDecl(root, src, obj.Content(src))
					}
					if obj == nil || obj.Type() != "object" {
						return
					}
					storeID := "redux_store:" + rel
					if !seenNode[storeID] {
						seenNode[storeID] = true
						newNodes = append(newNodes, graph.Node{
							ID: storeID, Type: graph.NodeTypeVariable, Label: "redux_store",
							Service: svc, File: rel, Line: line, EndLine: line, Language: "javascript",
							Meta: map[string]string{"redux": "store", "tier": "jcm3", "synthetic": "true"},
						})
					}
					for j := 0; j < int(obj.NamedChildCount()); j++ {
						p := obj.NamedChild(j)
						if p.Type() != "pair" {
							continue
						}
						v := p.ChildByFieldName("value")
						if v == nil || v.Type() != "identifier" {
							continue
						}
						vn := v.Content(src)
						rid := declIndex[rel][vn]
						if rid == "" {
							if imp, ok := imports[vn]; ok {
								exp := imp.exported
								if exp == "" {
									exp = vn
								}
								if rid = declIndex[imp.file][exp]; rid == "" {
									if rid = declIndex[imp.file][vn]; rid == "" {
										rid = mkFn(svcOfFile[imp.file], imp.file, exp, 1, "reducer")
									}
								}
							}
						}
						addEdge(graph.EdgeTypeContains, storeID, rid, "reducer_slice")
					}
					return
				}

				// dispatch(AC.x(…)) / this.props.dispatch(AC.x(…)) / dispatch(named(…))
				isDispatch := false
				switch f.Type() {
				case "identifier":
					isDispatch = f.Content(src) == "dispatch"
				case "member_expression":
					if p := f.ChildByFieldName("property"); p != nil && p.Content(src) == "dispatch" {
						isDispatch = true
					}
				}
				if isDispatch {
					if a := n.ChildByFieldName("arguments"); a != nil && a.NamedChildCount() > 0 {
						arg0 := a.NamedChild(0)
						if cid := resolveCreatorCall(arg0); cid != "" {
							addEdge(graph.EdgeTypeCalls, attrFrom(line), cid, "dispatch_call")
						} else if arg0.Type() == "object" {
							// dispatch({ type: ActionTypes.FOO, payload })
							if tid := reduxTypeField(arg0, src, resolveTypeRef); tid != "" {
								addEdge(graph.EdgeTypeReferences, attrFrom(line), tid, "dispatch_type")
							}
						}
					}
					return
				}

				// props.actions.X(…) / this.props.actions.X(…)
				// props.X(…) / this.props.X(…) where X is a bound action creator
				if f.Type() != "member_expression" {
					return
				}
				prop, obj := f.ChildByFieldName("property"), f.ChildByFieldName("object")
				if prop == nil || obj == nil {
					return
				}
				name := prop.Content(src)
				if obj.Type() == "member_expression" {
					op := obj.ChildByFieldName("property")
					if op != nil && op.Content(src) == "actions" && reduxIsPropsNode(obj.ChildByFieldName("object"), src) {
						if acFile != "" {
							toID := reduxCreatorNode(acFile, name, creatorNodeID, svcOfFile, mkFn)
							addEdge(graph.EdgeTypeCalls, attrFrom(line), toID, "props_actions_dispatch")
						}
						return
					}
				}
				if reduxIsPropsNode(obj, src) {
					if id := fileCreators[name]; id != "" {
						addEdge(graph.EdgeTypeCalls, attrFrom(line), id, "props_bound_dispatch")
					}
				}
			}
		})
	}

	return newNodes, newEdges
}

// reduxIsTypeAccess reports whether n is a `<x>.type` member access — the
// discriminant shape of a reducer switch / guard, usable as a path-independent
// signal that a switch really dispatches on action types.
func reduxIsTypeAccess(n *sitter.Node, src []byte) bool {
	if n == nil || n.Type() != "member_expression" {
		return false
	}
	p := n.ChildByFieldName("property")
	return p != nil && p.Content(src) == "type"
}

// reduxSwitchesOnActionType walks up from a switch_case to its switch_statement
// and reports whether the discriminant is a `<x>.type` access.
func reduxSwitchesOnActionType(caseNode *sitter.Node, src []byte) bool {
	for p := caseNode.Parent(); p != nil; p = p.Parent() {
		if p.Type() != "switch_statement" {
			continue
		}
		v := p.ChildByFieldName("value")
		if v != nil && v.Type() == "parenthesized_expression" && v.NamedChildCount() > 0 {
			v = v.NamedChild(0)
		}
		return reduxIsTypeAccess(v, src)
	}
	return false
}

// reduxObjectDecl finds `const <name> = { … }` in the tree and returns the
// object literal, or nil.
func reduxObjectDecl(root *sitter.Node, src []byte, name string) *sitter.Node {
	var found *sitter.Node
	reduxWalk(root, func(n *sitter.Node) {
		if found != nil || n.Type() != "variable_declarator" {
			return
		}
		nm, val := n.ChildByFieldName("name"), n.ChildByFieldName("value")
		if nm != nil && val != nil && nm.Type() == "identifier" &&
			nm.Content(src) == name && val.Type() == "object" {
			found = val
		}
	})
	return found
}

// reduxTypeField resolves the `type:` property of an action object literal to
// its action-type node ID, or "".
func reduxTypeField(obj *sitter.Node, src []byte, resolve func(*sitter.Node) string) string {
	for j := 0; j < int(obj.NamedChildCount()); j++ {
		p := obj.NamedChild(j)
		if p.Type() != "pair" {
			continue
		}
		k := p.ChildByFieldName("key")
		if k == nil || strings.Trim(k.Content(src), "\"'`") != "type" {
			continue
		}
		return resolve(p.ChildByFieldName("value"))
	}
	return ""
}

// reduxTypeRefResolver turns `ActionTypes.FOO` / a bare imported `FOO` into the
// action-type node ID, or "" when it isn't one we know.
func reduxTypeRefResolver(src []byte, rel string, imports map[string]reduxImport, actionTypeID map[string]map[string]string) func(*sitter.Node) string {
	return func(v *sitter.Node) string {
		if v == nil {
			return ""
		}
		switch v.Type() {
		case "member_expression":
			o, p := v.ChildByFieldName("object"), v.ChildByFieldName("property")
			if o != nil && p != nil && o.Type() == "identifier" {
				if imp, ok := imports[o.Content(src)]; ok {
					if m := actionTypeID[imp.file]; m != nil {
						return m[p.Content(src)]
					}
				}
				if m := actionTypeID[rel]; m != nil {
					return m[p.Content(src)]
				}
			}
		case "identifier":
			nm := v.Content(src)
			if imp, ok := imports[nm]; ok {
				exp := imp.exported
				if exp == "" {
					exp = nm
				}
				if m := actionTypeID[imp.file]; m != nil {
					return m[exp]
				}
			}
			if m := actionTypeID[rel]; m != nil {
				return m[nm]
			}
		}
		return ""
	}
}

// reduxCreatorNode returns the known creator node for (actionsFile, name), or a
// line-independent synthetic function node when the creator wasn't captured.
func reduxCreatorNode(acRel, name string, creatorNodeID map[string]map[string]string, svcOfFile map[string]string, mkFn func(string, string, string, int, string) string) string {
	if m := creatorNodeID[acRel]; m != nil {
		if id := m[name]; id != "" {
			return id
		}
	}
	return mkFn(svcOfFile[acRel], acRel, name, 1, "creator")
}

// reduxIsPropsNode reports whether n is `props` or `<x>.props` (e.g. `this.props`).
func reduxIsPropsNode(n *sitter.Node, src []byte) bool {
	if n == nil {
		return false
	}
	switch n.Type() {
	case "identifier":
		return n.Content(src) == "props"
	case "member_expression":
		p := n.ChildByFieldName("property")
		return p != nil && p.Content(src) == "props"
	}
	return false
}

// reduxImport is a resolved import binding: which in-workspace file the local
// name comes from, and the name it is exported as there.
type reduxImport struct {
	file     string // cwd-relative resolved path, or "" if external/unresolved
	exported string // export name in `file` ("" = default/namespace/same as local)
}

func reduxImports(root *sitter.Node, src []byte, importingFile string, indexed map[string]bool) map[string]reduxImport {
	out := make(map[string]reduxImport)
	reduxWalk(root, func(n *sitter.Node) {
		if n.Type() != "import_statement" {
			return
		}
		s := n.ChildByFieldName("source")
		if s == nil {
			return
		}
		resolved := resolveJSImportPath(importingFile, strings.Trim(s.Content(src), "\"'`"), indexed)
		if resolved == "" {
			return
		}
		rel := patterns.RelativizeToCwd(resolved)
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c.Type() != "import_clause" {
				continue
			}
			for j := 0; j < int(c.NamedChildCount()); j++ {
				d := c.NamedChild(j)
				switch d.Type() {
				case "identifier":
					out[d.Content(src)] = reduxImport{file: rel}
				case "namespace_import":
					if id := reduxFirstChild(d, "identifier"); id != nil {
						out[id.Content(src)] = reduxImport{file: rel}
					}
				case "named_imports":
					for k := 0; k < int(d.NamedChildCount()); k++ {
						sp := d.NamedChild(k)
						if sp.Type() != "import_specifier" {
							continue
						}
						nm := sp.ChildByFieldName("name")
						if nm == nil {
							continue
						}
						local := nm.Content(src)
						if al := sp.ChildByFieldName("alias"); al != nil {
							local = al.Content(src)
						}
						out[local] = reduxImport{file: rel, exported: nm.Content(src)}
					}
				}
			}
		}
	})
	return out
}

// reduxCollectActionTypes finds action-type string constants in an ActionTypes
// module: keyMirror({ A: null, … }) keys and top-level SCREAMING_CASE string
// consts. Returns name → 1-based decl line.
func reduxCollectActionTypes(root *sitter.Node, src []byte) map[string]int {
	out := make(map[string]int)
	reduxWalk(root, func(n *sitter.Node) {
		switch n.Type() {
		case "call_expression":
			f := n.ChildByFieldName("function")
			if f == nil || f.Type() != "identifier" || !strings.Contains(strings.ToLower(f.Content(src)), "keymirror") {
				return
			}
			args := n.ChildByFieldName("arguments")
			if args == nil {
				return
			}
			for i := 0; i < int(args.NamedChildCount()); i++ {
				obj := args.NamedChild(i)
				if obj.Type() != "object" {
					continue
				}
				for j := 0; j < int(obj.NamedChildCount()); j++ {
					p := obj.NamedChild(j)
					if p.Type() != "pair" {
						continue
					}
					if key := p.ChildByFieldName("key"); key != nil {
						if nm := strings.Trim(key.Content(src), "\"'`"); nm != "" {
							out[nm] = int(key.StartPoint().Row) + 1
						}
					}
				}
			}
		case "variable_declarator":
			nm, val := n.ChildByFieldName("name"), n.ChildByFieldName("value")
			if nm == nil || nm.Type() != "identifier" || val == nil {
				return
			}
			if val.Type() != "string" {
				return
			}
			name := nm.Content(src)
			if len(name) > 1 && name == strings.ToUpper(name) {
				out[name] = int(nm.StartPoint().Row) + 1
			}
		}
	})
	return out
}

// reduxCollectInlineActionTypes finds `const X = { FOO: "FOO", BAR: "BAR" }`
// action-type tables declared inline (not in a constants/ module). To stay
// precise it only accepts objects with ≥2 pairs whose every value is a string
// literal and whose every key is SCREAMING_CASE. Returns name → 1-based line.
func reduxCollectInlineActionTypes(root *sitter.Node, src []byte) map[string]int {
	out := make(map[string]int)
	reduxWalk(root, func(n *sitter.Node) {
		if n.Type() != "variable_declarator" {
			return
		}
		val := n.ChildByFieldName("value")
		if val == nil || val.Type() != "object" {
			return
		}
		type kv struct {
			name string
			line int
		}
		var keys []kv
		allStr := true
		for i := 0; i < int(val.NamedChildCount()); i++ {
			p := val.NamedChild(i)
			if p.Type() != "pair" {
				continue
			}
			k, v := p.ChildByFieldName("key"), p.ChildByFieldName("value")
			if k == nil || v == nil {
				allStr = false
				break
			}
			if v.Type() != "string" {
				allStr = false
				break
			}
			nm := strings.Trim(k.Content(src), "\"'`")
			if len(nm) < 2 || nm != strings.ToUpper(nm) || nm == strings.ToLower(nm) {
				allStr = false
				break
			}
			keys = append(keys, kv{nm, int(k.StartPoint().Row) + 1})
		}
		if !allStr || len(keys) < 2 {
			return
		}
		for _, k := range keys {
			out[k.name] = k.line
		}
	})
	return out
}

func reduxIsActionCreatorCall(name string) bool {
	l := strings.ToLower(name)
	return strings.Contains(l, "actioncreator") || strings.Contains(l, "createaction")
}

// reduxPathRole classifies a file by its conventional Redux directory.
func reduxPathRole(rel string) (isTypes, isActions, isReducers bool) {
	l := strings.ToLower(rel)
	base := strings.ToLower(filepath.Base(rel))
	isTypes = strings.Contains(base, "actiontypes") ||
		(strings.Contains(l, "/constants/") && strings.Contains(base, "type"))
	isActions = strings.Contains(l, "/actions/") || strings.Contains(base, "actioncreator")
	isReducers = strings.Contains(l, "/reducers/") || strings.HasSuffix(base, "reducer.js") ||
		strings.HasSuffix(base, "reducer.jsx") || strings.HasSuffix(base, "reducer.ts")
	return
}

func reduxWalk(n *sitter.Node, fn func(*sitter.Node)) {
	fn(n)
	for i := 0; i < int(n.NamedChildCount()); i++ {
		reduxWalk(n.NamedChild(i), fn)
	}
}

func reduxFirstChild(n *sitter.Node, typ string) *sitter.Node {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if c := n.NamedChild(i); c.Type() == typ {
			return c
		}
	}
	return nil
}
