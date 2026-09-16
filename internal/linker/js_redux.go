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
// props_actions_dispatch, props_bound_dispatch, dispatch_call, dispatch_type;
// JCM.10 selector/read side: slice_reducer, selector_read, reselect_input,
// whole_state_read (`state => state` / `{...state}` → the combined store node);
// JCM.11: thunk_dispatch (thunk inner dispatches), inline-arrow reducer
// type_handled_by, connect({shorthand}) props_bound_dispatch, effect_gap
// (redux-saga / redux-observable coverage stub).
// Phase RTK (Redux Toolkit, path-independent): createSlice → slice/reducer +
// per-key creator_type/type_handled_by; createAsyncThunk → creator + creator_type;
// configureStore({reducer:{…}}) → redux_store node + reducer_slice.
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

	// importsCache memoizes reduxImports per file: Phase 2a, Phase RTK, Phase
	// 2b and Phase 3 each need a file's import bindings, and previously each
	// called reduxImports independently — a full extra tree walk per phase
	// that reaches the same file, for an identical (root, src, file) input
	// every time. XM.20 (docs/factpipe-cross-framework-matching-plan.md).
	importsCache := make(map[string]map[string]reduxImport)
	getImports := func(fe fileEnt, root *sitter.Node, src []byte) map[string]reduxImport {
		if c, ok := importsCache[fe.abs]; ok {
			return c
		}
		c := reduxImports(root, src, fe.abs, indexed)
		importsCache[fe.abs] = c
		return c
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
	// addEdge10 is the JCM.10 (selector / read side) edge emitter — same shape as
	// addEdge but tier jcm10 and a distinct edge-id suffix so it never collides
	// with a jcm3 dispatch-chain edge between the same endpoints.
	addEdge10 := func(t graph.EdgeType, from, to, role string) {
		if from == "" || to == "" || from == to {
			return
		}
		eid := fmt.Sprintf("%s:%s->%s#jcm10", t, from, to)
		if seenEdge[eid] {
			return
		}
		seenEdge[eid] = true
		newEdges = append(newEdges, graph.Edge{
			ID: eid, From: from, To: to, Type: t, Confidence: graph.ConfidenceInferred,
			Meta: map[string]string{"redux": role, "tier": "jcm10"},
		})
	}
	// knownSlices / sliceNode back the JCM.10 slice model: each combineReducers
	// key becomes a stable `redux_slice:<name>` node --contains--> its reducer,
	// and selector reads (mapStateToProps / useSelector / reselect) target it.
	// addEdge11 emits the JCM.11 chain edges (thunk dispatches, connect-shorthand
	// dispatch, inline-arrow reducer type handling) — same shape as addEdge but
	// tier jcm11 with a distinct id suffix so it never collides with a jcm3 edge.
	addEdge11 := func(t graph.EdgeType, from, to, role string) {
		if from == "" || to == "" || from == to {
			return
		}
		eid := fmt.Sprintf("%s:%s->%s#jcm11", t, from, to)
		if seenEdge[eid] {
			return
		}
		seenEdge[eid] = true
		newEdges = append(newEdges, graph.Edge{
			ID: eid, From: from, To: to, Type: t, Confidence: graph.ConfidenceInferred,
			Meta: map[string]string{"redux": role, "tier": "jcm11"},
		})
	}
	knownSlices := make(map[string]bool)
	// rtkActionFiles: rel → true for any file defining createSlice /
	// createAsyncThunk creators, so Phase 2b treats a bare imported creator
	// from it like one from a conventional actions/ module (path-independent).
	rtkActionFiles := make(map[string]bool)
	// storeNodesBySvc records every synthetic `redux_store:<file>` node minted in
	// Phase 2b (combineReducers) so Phase 3's whole-state selectors
	// (`state => state`, `return state`, `{ ...state }`) have a target: the
	// combined store itself rather than one slice.
	storeNodesBySvc := make(map[string][]string)
	allStoreNodes := []string{}
	sliceNode := func(name, svc, rel string, line int) string {
		id := "redux_slice:" + name
		if !seenNode[id] {
			seenNode[id] = true
			newNodes = append(newNodes, graph.Node{
				ID: id, Type: graph.NodeTypeVariable, Label: name, Service: svc, File: rel,
				Line: line, EndLine: line, Language: "javascript",
				Meta: map[string]string{"redux": "slice", "tier": "jcm10", "synthetic": "true"},
			})
		}
		return id
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
	// rtkObjProp / the RTK case below back Phase RTK (Redux Toolkit:
	// createSlice / createAsyncThunk / configureStore) — content-gated and
	// path-independent, unlike Phase 2a's path-gated creator/thunk detection.
	// XM.20 cont'd merges Phase 2a's and Phase RTK's former separate loops
	// into one: neither phase reads state the other writes
	// (creatorNodeID/rtkActionFiles are write-only here, read only by Phase
	// 2b below), so a file matching both gates (isActions AND RTK-content)
	// no longer parses/walks its tree twice.
	rtkObjProp := func(obj *sitter.Node, key string, src []byte) *sitter.Node {
		if obj == nil || obj.Type() != "object" {
			return nil
		}
		for i := 0; i < int(obj.NamedChildCount()); i++ {
			p := obj.NamedChild(i)
			if p.Type() != "pair" {
				continue
			}
			if k := p.ChildByFieldName("key"); k != nil &&
				strings.Trim(k.Content(src), "\"'`") == key {
				return p.ChildByFieldName("value")
			}
		}
		return nil
	}
	for _, fe := range jsFiles {
		rel := patterns.RelativizeToCwd(fe.abs)
		_, isActionsFile, _ := reduxPathRole(rel)
		svc := fe.svc
		src, root, _, ok := jsParse(fe.abs)
		if !ok {
			continue
		}
		s := string(src)
		isRTKFile := strings.Contains(s, "createSlice") || strings.Contains(s, "createAsyncThunk") ||
			strings.Contains(s, "configureStore")
		if !isActionsFile && !isRTKFile {
			continue
		}
		imports := getImports(fe, root, src)
		resolveTypeRef := reduxTypeRefResolver(src, rel, imports, actionTypeID)
		attrFrom := func(line int) string {
			if id := nearestDecl(declsByFile[rel], line); id != "" {
				return id
			}
			return fileNodeID[svc+"\x00"+rel]
		}
		// A single walk dispatches the creator-registry detection (pair /
		// object), the JCM.11 thunk detection (function_declaration /
		// variable_declarator), and Phase RTK's createSlice / createAsyncThunk
		// / configureStore detection (call_expression) below — the matched
		// node-type sets are disjoint and none reads state another writes
		// mid-walk, so this is one tree traversal instead of two or three
		// (XM.20).
		reduxWalk(root, func(n *sitter.Node) {
			switch n.Type() {
			case "call_expression":
				if !isRTKFile {
					return
				}
				f := n.ChildByFieldName("function")
				a := n.ChildByFieldName("arguments")
				if f == nil || a == nil {
					return
				}
				callee := f.Content(src)
				line := int(n.StartPoint().Row) + 1
				bindName := ""
				if p := n.Parent(); p != nil && p.Type() == "variable_declarator" {
					if nm := p.ChildByFieldName("name"); nm != nil && nm.Type() == "identifier" {
						bindName = nm.Content(src)
					}
				}

				switch {
				case callee == "createSlice" || strings.HasSuffix(callee, ".createSlice"):
					if a.NamedChildCount() == 0 || a.NamedChild(0).Type() != "object" {
						return
					}
					cfg := a.NamedChild(0)
					name := ""
					if nv := rtkObjProp(cfg, "name", src); nv != nil {
						name = strings.Trim(nv.Content(src), "\"'`")
					}
					if name == "" {
						name = strings.TrimSuffix(bindName, "Slice")
					}
					if name == "" {
						return
					}
					knownSlices[name] = true
					rtkActionFiles[rel] = true
					slID := sliceNode(name, svc, rel, line)
					redID := mkFn(svc, rel, name+"Reducer", line, "reducer")
					addEdge10(graph.EdgeTypeContains, slID, redID, "slice_reducer")
					reducers := rtkObjProp(cfg, "reducers", src)
					if reducers == nil || reducers.Type() != "object" {
						return
					}
					for i := 0; i < int(reducers.NamedChildCount()); i++ {
						pr := reducers.NamedChild(i)
						var key *sitter.Node
						switch pr.Type() {
						case "pair":
							key = pr.ChildByFieldName("key")
						case "method_definition":
							key = pr.ChildByFieldName("name")
						}
						if key == nil {
							continue
						}
						cname := strings.Trim(key.Content(src), "\"'`")
						tid := mkVar(svc, rel, name+"/"+cname, line)
						cid := mkFn(svc, rel, cname, int(key.StartPoint().Row)+1, "creator")
						addEdge(graph.EdgeTypeReferences, cid, tid, "creator_type")
						addEdge(graph.EdgeTypeReferences, tid, redID, "type_handled_by")
						recordCreator(rel, cname, cid)
					}

				case callee == "createAsyncThunk" || strings.HasSuffix(callee, ".createAsyncThunk"):
					if bindName == "" {
						return
					}
					cid := declIndex[rel][bindName]
					if cid == "" {
						cid = mkFn(svc, rel, bindName, line, "creator")
					}
					rtkActionFiles[rel] = true
					recordCreator(rel, bindName, cid)
					if a.NamedChildCount() > 0 && a.NamedChild(0).Type() == "string" {
						if tn := strings.Trim(a.NamedChild(0).Content(src), "\"'`"); tn != "" {
							addEdge(graph.EdgeTypeReferences, cid, mkVar(svc, rel, tn, line), "creator_type")
						}
					}

				case callee == "configureStore" || strings.HasSuffix(callee, ".configureStore"):
					if a.NamedChildCount() == 0 || a.NamedChild(0).Type() != "object" {
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
						storeNodesBySvc[svc] = append(storeNodesBySvc[svc], storeID)
						allStoreNodes = append(allStoreNodes, storeID)
					}
					red := rtkObjProp(a.NamedChild(0), "reducer", src)
					if red == nil || red.Type() != "object" {
						return
					}
					for i := 0; i < int(red.NamedChildCount()); i++ {
						pr := red.NamedChild(i)
						if pr.Type() != "pair" {
							continue
						}
						k, v := pr.ChildByFieldName("key"), pr.ChildByFieldName("value")
						if k == nil || v == nil {
							continue
						}
						sliceName := strings.Trim(k.Content(src), "\"'`")
						knownSlices[sliceName] = true
						slID := sliceNode(sliceName, svc, rel, line)
						addEdge(graph.EdgeTypeContains, storeID, slID, "reducer_slice")
						if v.Type() == "identifier" {
							vn := v.Content(src)
							rid := declIndex[rel][vn]
							if rid == "" {
								if imp, ok := imports[vn]; ok {
									exp := imp.exported
									if exp == "" {
										exp = vn
									}
									rid = declIndex[imp.file][exp]
								}
							}
							if rid != "" {
								addEdge10(graph.EdgeTypeContains, slID, rid, "slice_reducer")
							}
						}
					}
				}

			case "pair":
				if !isActionsFile {
					return
				}
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
				if !isActionsFile {
					return
				}
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

			case "function_declaration", "variable_declarator":
				if !isActionsFile {
					return
				}
				// JCM.11: thunks. `export const load = () => (dispatch) => { … }` /
				// `function load() { return function(dispatch) { … } }` — attribute
				// the inner function's dispatch calls to the outer creator node.
				var nameNode, fnNode *sitter.Node
				switch n.Type() {
				case "function_declaration":
					nameNode, fnNode = n.ChildByFieldName("name"), n
				case "variable_declarator":
					if v := n.ChildByFieldName("value"); v != nil &&
						(v.Type() == "arrow_function" || v.Type() == "function_expression") {
						nameNode, fnNode = n.ChildByFieldName("name"), v
					}
				}
				if nameNode == nil || fnNode == nil || nameNode.Type() != "identifier" {
					return
				}
				inner := reduxThunkInner(fnNode, src)
				if inner == nil {
					return
				}
				cname := nameNode.Content(src)
				cid := declIndex[rel][cname]
				if cid == "" {
					cid = mkFn(svc, rel, cname, int(nameNode.StartPoint().Row)+1, "creator")
				}
				recordCreator(rel, cname, cid)
				ib := inner.ChildByFieldName("body")
				if ib == nil {
					return
				}
				reduxWalk(ib, func(d *sitter.Node) {
					if d.Type() != "call_expression" {
						return
					}
					df := d.ChildByFieldName("function")
					if df == nil || df.Content(src) != "dispatch" {
						return
					}
					da := d.ChildByFieldName("arguments")
					if da == nil || da.NamedChildCount() == 0 {
						return
					}
					arg0 := da.NamedChild(0)
					switch arg0.Type() {
					case "call_expression":
						acf := arg0.ChildByFieldName("function")
						if acf == nil {
							return
						}
						var to string
						switch acf.Type() {
						case "identifier":
							nm := acf.Content(src)
							if id := declIndex[rel][nm]; id != "" {
								to = id
							} else if imp, ok := imports[nm]; ok {
								exp := imp.exported
								if exp == "" {
									exp = nm
								}
								to = reduxCreatorNode(imp.file, exp, creatorNodeID, svcOfFile, mkFn)
							}
						case "member_expression":
							o, p := acf.ChildByFieldName("object"), acf.ChildByFieldName("property")
							if o != nil && p != nil && o.Type() == "identifier" {
								if imp, ok := imports[o.Content(src)]; ok {
									to = reduxCreatorNode(imp.file, p.Content(src), creatorNodeID, svcOfFile, mkFn)
								}
							}
						}
						addEdge11(graph.EdgeTypeCalls, cid, to, "thunk_dispatch")
					case "object":
						if tid := reduxTypeField(arg0, src, resolveTypeRef); tid != "" {
							addEdge11(graph.EdgeTypeReferences, cid, tid, "thunk_dispatch")
						}
					}
				})
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
		imports := getImports(fe, root, src)
		isActionsFile := func(frel string) bool {
			if _, ia, _ := reduxPathRole(frel); ia {
				return true
			}
			return rtkActionFiles[frel]
		}
		resolveTypeRef := reduxTypeRefResolver(src, rel, imports, actionTypeID)
		attrFrom := func(line int) string {
			if id := nearestDecl(declsByFile[rel], line); id != "" {
				return id
			}
			return fileNodeID[svc+"\x00"+rel]
		}

		// action-creator modules this file pulls in, plus the union of their
		// creator names → node IDs (for `props.<creator>()` / `dispatch(x())`).
		// acFiles is a list, in absorption order, rather than "the first one
		// seen": a component routinely imports several modules that all satisfy
		// isActionsFile, and `props.actions.X(…)` names one creator without
		// saying which module defines it. Resolving that against an arbitrary
		// single module sent the edge to a module that does not define X, where
		// reduxCreatorNode then minted a synthetic stub for it.
		var acFiles []string
		acSeen := make(map[string]bool)
		fileCreators := make(map[string]string)
		absorbAC := func(acRel string) {
			if !acSeen[acRel] {
				acSeen[acRel] = true
				acFiles = append(acFiles, acRel)
			}
			for nm, id := range creatorNodeID[acRel] {
				fileCreators[nm] = id
			}
		}
		// `imports` is a map, so absorbing straight out of a range over it made
		// both acFiles' order and fileCreators' collision winner depend on Go's
		// randomised map iteration — two cold indexes of the same tree disagreed
		// on where these edges pointed. Absorb by sorted local binding name.
		impNames := make([]string, 0, len(imports))
		for local := range imports {
			impNames = append(impNames, local)
		}
		sort.Strings(impNames)
		for _, local := range impNames {
			if imp := imports[local]; isActionsFile(imp.file) {
				absorbAC(imp.file)
			}
		}
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
						if isActionsFile(imp.file) {
							return reduxCreatorNode(imp.file, p.Content(src), creatorNodeID, svcOfFile, mkFn)
						}
					}
				}
			case "identifier":
				nm := cf.Content(src)
				if imp, ok := imports[nm]; ok {
					if isActionsFile(imp.file) {
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

		// Phase 2b's three former walks (bindActionCreators/connect,
		// combineReducers/switch/binary reducer detection, and the dispatch-
		// site detector) merge into one (XM.20 cont'd): switch_case/
		// binary_expression and combineReducers's call_expression case read
		// no acFiles/fileCreators state, so they still run immediately.
		// dispatch(…) and props.<creator>(…) call sites DO depend on
		// acFiles/fileCreators, which a bindActionCreators/connect call
		// elsewhere in the same file may populate later in source order — those
		// candidates are buffered and resolved in a second pass over the
		// (small) buffer once the walk finishes, rather than a second walk of
		// the whole tree.
		var dispatchCandidates []*sitter.Node
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
				f := n.ChildByFieldName("function")
				if f == nil {
					return
				}

				if f.Type() == "identifier" {
					switch f.Content(src) {
					case "bindActionCreators":
						a := n.ChildByFieldName("arguments")
						if a == nil || a.NamedChildCount() == 0 {
							return
						}
						if a0 := a.NamedChild(0); a0.Type() == "identifier" {
							if imp, ok := imports[a0.Content(src)]; ok {
								if isActionsFile(imp.file) {
									absorbAC(imp.file)
								}
							}
						}
						return

					case "connect":
						a := n.ChildByFieldName("arguments")
						if a == nil || a.NamedChildCount() < 2 {
							return
						}
						obj := a.NamedChild(1)
						if obj.Type() != "object" {
							return
						}
						for j := 0; j < int(obj.NamedChildCount()); j++ {
							p := obj.NamedChild(j)
							var key, valName string
							switch p.Type() {
							case "shorthand_property_identifier":
								key, valName = p.Content(src), p.Content(src)
							case "pair":
								k, v := p.ChildByFieldName("key"), p.ChildByFieldName("value")
								if k == nil || v == nil || v.Type() != "identifier" {
									continue
								}
								key, valName = strings.Trim(k.Content(src), "\"'`"), v.Content(src)
							default:
								continue
							}
							imp, ok := imports[valName]
							if !ok {
								continue
							}
							if !isActionsFile(imp.file) {
								continue
							}
							exp := imp.exported
							if exp == "" {
								exp = valName
							}
							if id := reduxCreatorNode(imp.file, exp, creatorNodeID, svcOfFile, mkFn); id != "" {
								fileCreators[key] = id
							}
						}
						return

					case "combineReducers":
						// combineReducers({ slice: ReducerFn, … }) → store --contains--> reducer
						line := int(n.StartPoint().Row) + 1
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
							storeNodesBySvc[svc] = append(storeNodesBySvc[svc], storeID)
							allStoreNodes = append(allStoreNodes, storeID)
						}
						for j := 0; j < int(obj.NamedChildCount()); j++ {
							p := obj.NamedChild(j)
							if p.Type() != "pair" {
								continue
							}
							v := p.ChildByFieldName("value")
							if v == nil {
								continue
							}
							sliceName := ""
							if k := p.ChildByFieldName("key"); k != nil {
								sliceName = strings.Trim(k.Content(src), "\"'`")
							}
							var rid string
							switch v.Type() {
							case "identifier":
								vn := v.Content(src)
								rid = declIndex[rel][vn]
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
							case "arrow_function", "function_expression":
								// JCM.11: inline-arrow slice reducer — mint a synthetic
								// function node and run action-type detection on its body.
								rname := sliceName
								if rname == "" {
									rname = fmt.Sprintf("slice%d", j)
								}
								rid = mkFn(svc, rel, rname+"Reducer", int(v.StartPoint().Row)+1, "reducer")
								if body := v.ChildByFieldName("body"); body != nil {
									reduxWalk(body, func(m *sitter.Node) {
										switch m.Type() {
										case "switch_case":
											if tid := resolveTypeRef(m.ChildByFieldName("value")); tid != "" {
												addEdge11(graph.EdgeTypeReferences, tid, rid, "type_handled_by")
											}
										case "binary_expression":
											op := m.ChildByFieldName("operator")
											if op == nil || (op.Content(src) != "===" && op.Content(src) != "==") {
												return
											}
											for _, side := range []*sitter.Node{m.ChildByFieldName("left"), m.ChildByFieldName("right")} {
												if tid := resolveTypeRef(side); tid != "" {
													addEdge11(graph.EdgeTypeReferences, tid, rid, "type_handled_by")
												}
											}
										}
									})
								}
							default:
								continue
							}
							addEdge(graph.EdgeTypeContains, storeID, rid, "reducer_slice")
							if sliceName != "" && rid != "" {
								knownSlices[sliceName] = true
								addEdge10(graph.EdgeTypeContains, sliceNode(sliceName, svc, rel, line), rid, "slice_reducer")
							}
						}
						return
					}
				}

				// dispatch(…) / props.actions.X(…) / props.X(…): resolved after
				// the walk, once acFiles/fileCreators above are complete.
				dispatchCandidates = append(dispatchCandidates, n)
			}
		})

		for _, n := range dispatchCandidates {
			line := int(n.StartPoint().Row) + 1
			f := n.ChildByFieldName("function")
			if f == nil {
				continue
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
				continue
			}

			// props.actions.X(…) / this.props.actions.X(…)
			// props.X(…) / this.props.X(…) where X is a bound action creator
			if f.Type() != "member_expression" {
				continue
			}
			prop, obj := f.ChildByFieldName("property"), f.ChildByFieldName("object")
			if prop == nil || obj == nil {
				continue
			}
			name := prop.Content(src)
			if obj.Type() == "member_expression" {
				op := obj.ChildByFieldName("property")
				if op != nil && op.Content(src) == "actions" && reduxIsPropsNode(obj.ChildByFieldName("object"), src) {
					if acRel := reduxCreatorHome(acFiles, name, creatorNodeID); acRel != "" {
						toID := reduxCreatorNode(acRel, name, creatorNodeID, svcOfFile, mkFn)
						addEdge(graph.EdgeTypeCalls, attrFrom(line), toID, "props_actions_dispatch")
					}
					continue
				}
			}
			if reduxIsPropsNode(obj, src) {
				if id := fileCreators[name]; id != "" {
					addEdge(graph.EdgeTypeCalls, attrFrom(line), id, "props_bound_dispatch")
				}
			}
		}
	}

	// === Phase 3 (JCM.10): selector / read side ===
	// mapStateToProps / useSelector / reselect → `consumer --reads--> redux_slice`.
	// Slice reads are gated on knownSlices (populated in Phase 2b) to stay precise;
	// `useSelector(namedSelector)` and `createSelector` inputs resolve by name.
	for _, fe := range jsFiles {
		rel := patterns.RelativizeToCwd(fe.abs)
		svc := fe.svc
		src, root, _, ok := jsParse(fe.abs)
		if !ok {
			continue
		}
		s := string(src)
		if !strings.Contains(s, "mapStateToProps") && !strings.Contains(s, "useSelector") &&
			!strings.Contains(s, "createSelector") {
			continue
		}
		imports := getImports(fe, root, src)
		attrFrom := func(line int) string {
			if id := nearestDecl(declsByFile[rel], line); id != "" {
				return id
			}
			return fileNodeID[svc+"\x00"+rel]
		}
		// emitWholeState wires a whole-store selector to the combined store
		// node(s) — same-service stores first, else every store in the graph.
		emitWholeState := func(from string) {
			stores := storeNodesBySvc[svc]
			if len(stores) == 0 {
				stores = allStoreNodes
			}
			for _, st := range stores {
				addEdge10(graph.EdgeTypeReads, from, st, "whole_state_read")
			}
		}
		resolveSelector := func(name string) string {
			if id := declIndex[rel][name]; id != "" {
				return id
			}
			if imp, ok := imports[name]; ok {
				exp := imp.exported
				if exp == "" {
					exp = name
				}
				if id := declIndex[imp.file][exp]; id != "" {
					return id
				}
			}
			return ""
		}
		firstParam := func(fn *sitter.Node) string {
			if p := fn.ChildByFieldName("parameter"); p != nil && p.Type() == "identifier" {
				return p.Content(src)
			}
			ps := fn.ChildByFieldName("parameters")
			if ps == nil || ps.NamedChildCount() == 0 {
				return ""
			}
			p0 := ps.NamedChild(0)
			if p0.Type() == "identifier" {
				return p0.Content(src)
			}
			if id := reduxFirstChild(p0, "identifier"); id != nil {
				return id.Content(src)
			}
			return ""
		}
		// firstParamPattern returns the object_pattern of a selector's first
		// parameter (`mapStateToProps({ foo, bar })`), or nil.
		firstParamPattern := func(fn *sitter.Node) *sitter.Node {
			ps := fn.ChildByFieldName("parameters")
			if ps == nil || ps.NamedChildCount() == 0 {
				return nil
			}
			p0 := ps.NamedChild(0)
			if p0.Type() == "object_pattern" {
				return p0
			}
			return reduxFirstChild(p0, "object_pattern")
		}
		emitPatternSlices := func(from string, pat *sitter.Node) {
			if from == "" || pat == nil {
				return
			}
			for i := 0; i < int(pat.NamedChildCount()); i++ {
				sp := pat.NamedChild(i)
				var key *sitter.Node
				switch sp.Type() {
				case "shorthand_property_identifier_pattern":
					key = sp
				case "pair_pattern":
					key = sp.ChildByFieldName("key")
				}
				if key == nil {
					continue
				}
				if sl := key.Content(src); knownSlices[sl] {
					addEdge10(graph.EdgeTypeReads, from, sliceNode(sl, svc, rel, 1), "selector_read")
				}
			}
		}
		// readState walks a selector body for `<param>.<slice>` member access and
		// `const { slice } = <param>` destructuring, emitting selector_read edges
		// to every known slice it touches.
		readState := func(from, param string, body *sitter.Node) {
			if from == "" || param == "" || body == nil {
				return
			}
			reduxWalk(body, func(m *sitter.Node) {
				switch m.Type() {
				case "member_expression":
					o, pr := m.ChildByFieldName("object"), m.ChildByFieldName("property")
					if o != nil && pr != nil && o.Type() == "identifier" && o.Content(src) == param {
						if sl := pr.Content(src); knownSlices[sl] {
							addEdge10(graph.EdgeTypeReads, from, sliceNode(sl, svc, rel, 1), "selector_read")
						}
					}
				case "variable_declarator":
					nm, val := m.ChildByFieldName("name"), m.ChildByFieldName("value")
					if val == nil || val.Type() != "identifier" || val.Content(src) != param ||
						nm == nil || nm.Type() != "object_pattern" {
						return
					}
					for i := 0; i < int(nm.NamedChildCount()); i++ {
						sp := nm.NamedChild(i)
						var key *sitter.Node
						switch sp.Type() {
						case "shorthand_property_identifier_pattern":
							key = sp
						case "pair_pattern":
							key = sp.ChildByFieldName("key")
						}
						if key == nil {
							continue
						}
						if sl := key.Content(src); knownSlices[sl] {
							addEdge10(graph.EdgeTypeReads, from, sliceNode(sl, svc, rel, 1), "selector_read")
						}
					}
				}
			})
		}
		reduxWalk(root, func(n *sitter.Node) {
			switch n.Type() {
			case "function_declaration", "function_expression", "arrow_function":
				var nameNode *sitter.Node
				if n.Type() == "function_declaration" {
					nameNode = n.ChildByFieldName("name")
				} else if p := n.Parent(); p != nil && p.Type() == "variable_declarator" {
					nameNode = p.ChildByFieldName("name")
				}
				if nameNode == nil ||
					!strings.Contains(strings.ToLower(nameNode.Content(src)), "mapstatetoprops") {
					return
				}
				from := attrFrom(int(n.StartPoint().Row) + 1)
				if pat := firstParamPattern(n); pat != nil {
					emitPatternSlices(from, pat)
				} else {
					p := firstParam(n)
					body := n.ChildByFieldName("body")
					if p != "" && reduxReadsWholeState(body, p, src) {
						emitWholeState(from)
					}
					readState(from, p, body)
				}

			case "call_expression":
				f := n.ChildByFieldName("function")
				a := n.ChildByFieldName("arguments")
				if f == nil || a == nil {
					return
				}
				callee := f.Content(src)
				switch {
				case callee == "useSelector" || strings.HasSuffix(callee, ".useSelector"):
					if a.NamedChildCount() == 0 {
						return
					}
					arg0 := a.NamedChild(0)
					from := attrFrom(int(n.StartPoint().Row) + 1)
					switch arg0.Type() {
					case "arrow_function", "function_expression":
						if pat := firstParamPattern(arg0); pat != nil {
							emitPatternSlices(from, pat)
						} else {
							p := firstParam(arg0)
							body := arg0.ChildByFieldName("body")
							if p != "" && reduxReadsWholeState(body, p, src) {
								emitWholeState(from)
							}
							readState(from, p, body)
						}
					case "identifier":
						if to := resolveSelector(arg0.Content(src)); to != "" {
							addEdge10(graph.EdgeTypeReads, from, to, "selector_read")
						}
					}
				case callee == "createSelector" || strings.HasSuffix(callee, ".createSelector"):
					selFrom := ""
					if p := n.Parent(); p != nil && p.Type() == "variable_declarator" {
						if nm := p.ChildByFieldName("name"); nm != nil {
							selFrom = declIndex[rel][nm.Content(src)]
						}
					}
					if selFrom == "" {
						selFrom = attrFrom(int(n.StartPoint().Row) + 1)
					}
					var inputs []*sitter.Node
					if a.NamedChildCount() > 0 && a.NamedChild(0).Type() == "array" {
						arr := a.NamedChild(0)
						for i := 0; i < int(arr.NamedChildCount()); i++ {
							inputs = append(inputs, arr.NamedChild(i))
						}
					} else {
						for i := 0; i+1 < int(a.NamedChildCount()); i++ {
							inputs = append(inputs, a.NamedChild(i))
						}
					}
					for _, in := range inputs {
						switch in.Type() {
						case "identifier":
							if to := resolveSelector(in.Content(src)); to != "" {
								addEdge10(graph.EdgeTypeReads, selFrom, to, "reselect_input")
							}
						case "arrow_function", "function_expression":
							p := firstParam(in)
							body := in.ChildByFieldName("body")
							if p != "" && reduxReadsWholeState(body, p, src) {
								emitWholeState(selFrom)
							}
							readState(selFrom, p, body)
						}
					}
				}
			}
		})
	}

	// === Phase 4 (JCM.11): effect-library coverage stub ===
	// redux-saga / redux-observable flow isn't modeled. Leave one visible
	// unknown-confidence edge per effect file so the gap surfaces in
	// `polyflow status --unknown-edges` / the unknown_edges MCP tool.
	for _, fe := range jsFiles {
		rel := patterns.RelativizeToCwd(fe.abs)
		src, _, _, ok := jsParse(fe.abs)
		if !ok {
			continue
		}
		s := string(src)
		lib := ""
		switch {
		case strings.Contains(s, "redux-saga"):
			lib = "redux-saga"
		case strings.Contains(s, "redux-observable"):
			lib = "redux-observable"
		default:
			continue
		}
		fid := fileNodeID[fe.svc+"\x00"+rel]
		if fid == "" {
			continue
		}
		gid := "redux_effect_gap:" + rel
		if !seenNode[gid] {
			seenNode[gid] = true
			newNodes = append(newNodes, graph.Node{
				ID: gid, Type: graph.NodeTypeVariable, Label: lib + " (unmodeled)",
				Service: fe.svc, File: rel, Line: 1, EndLine: 1, Language: "javascript",
				Meta: map[string]string{"redux": "effect_gap", "tier": "jcm11", "synthetic": "true"},
			})
		}
		eid := "references:" + fid + "->" + gid + "#jcm11"
		if !seenEdge[eid] {
			seenEdge[eid] = true
			newEdges = append(newEdges, graph.Edge{
				ID: eid, From: fid, To: gid, Type: graph.EdgeTypeReferences,
				Confidence: graph.ConfidenceUnknown,
				Meta:       map[string]string{"redux": "effect_gap", "tier": "jcm11"},
			})
		}
	}

	return newNodes, newEdges
}

// reduxThunkInner returns the inner `(dispatch, getState?) => …` function of a
// thunk creator, or nil when fn isn't a thunk. It accepts both the curried-arrow
// form (`() => (dispatch) => …`) and a `return function(dispatch){…}` body.
func reduxThunkInner(fn *sitter.Node, src []byte) *sitter.Node {
	body := fn.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	if (body.Type() == "arrow_function" || body.Type() == "function_expression") &&
		reduxFnFirstParam(body, src) == "dispatch" {
		return body
	}
	var found *sitter.Node
	reduxWalk(body, func(n *sitter.Node) {
		if found != nil || n.Type() != "return_statement" || n.NamedChildCount() == 0 {
			return
		}
		r := n.NamedChild(0)
		if (r.Type() == "arrow_function" || r.Type() == "function_expression") &&
			reduxFnFirstParam(r, src) == "dispatch" {
			found = r
		}
	})
	return found
}

// reduxFnFirstParam returns the identifier name of a function's first parameter,
// unwrapping the single-unparenthesized-arrow-param form.
func reduxFnFirstParam(fn *sitter.Node, src []byte) string {
	if p := fn.ChildByFieldName("parameter"); p != nil && p.Type() == "identifier" {
		return p.Content(src)
	}
	ps := fn.ChildByFieldName("parameters")
	if ps == nil || ps.NamedChildCount() == 0 {
		return ""
	}
	p0 := ps.NamedChild(0)
	if p0.Type() == "identifier" {
		return p0.Content(src)
	}
	if id := reduxFirstChild(p0, "identifier"); id != nil {
		return id.Content(src)
	}
	return ""
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

// reduxCreatorHome picks which of a component's imported action-creator modules
// owns `name`: the first one that actually declares a creator by that name,
// falling back to acFiles[0] so an undeclared creator still resolves (to a
// synthetic stub, as before) rather than dropping the edge. Returns "" when the
// file imports no action-creator module at all.
//
// The fallback is deliberately last, not first: `props.actions.X(…)` carries no
// module name, so preferring the module that declares X is the only evidence
// available about where the call really goes — and taking any other module
// means minting a stub for a creator that already has a real node elsewhere.
func reduxCreatorHome(acFiles []string, name string, creatorNodeID map[string]map[string]string) string {
	if len(acFiles) == 0 {
		return ""
	}
	for _, acRel := range acFiles {
		if m := creatorNodeID[acRel]; m != nil && m[name] != "" {
			return acRel
		}
	}
	return acFiles[0]
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

// reduxReadsWholeState reports whether a selector body depends on the ENTIRE
// store rather than a named slice — the shapes JCM.10's slice model can't
// pin: `state => state`, `return state`, `{ ...state }` / `[...state]`,
// `Object.assign({}, state)`, `f(...state)`, and `{ all: state }`. A
// `state.slice` / `state.a.b` access never trips this: the bare `state`
// identifier there sits under a member_expression, not any of these parents.
func reduxReadsWholeState(body *sitter.Node, param string, src []byte) bool {
	if body == nil || param == "" {
		return false
	}
	if body.Type() == "identifier" && body.Content(src) == param {
		return true // arrow expression body: `state => state`
	}
	found := false
	reduxWalk(body, func(n *sitter.Node) {
		if found || n.Type() != "identifier" || n.Content(src) != param {
			return
		}
		p := n.Parent()
		if p == nil {
			return
		}
		switch p.Type() {
		case "return_statement", "spread_element", "parenthesized_expression":
			found = true
		case "pair":
			if p.ChildByFieldName("value") == n { // `{ all: state }`
				found = true
			}
		case "arguments":
			if call := p.Parent(); call != nil && call.Type() == "call_expression" {
				if cf := call.ChildByFieldName("function"); cf != nil &&
					strings.Contains(cf.Content(src), "assign") {
					found = true // Object.assign(target, state)
				}
			}
		}
	})
	return found
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
