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
//   - reducers/** (or any)    : switch(action.type){ case ActionTypes.A: },
//     if (action.type === ActionTypes.A)
//   - components              : this.props.actions.X(…) when the file binds an
//     action-creators module (bindActionCreators / an
//     actions/** import)
func LinkJSRedux(nodes []graph.Node, serviceFiles map[string][]string) (newNodes []graph.Node, newEdges []graph.Edge) {
	// --- index existing nodes (File is the cwd-relative form the parser mints) ---
	declsByFile := make(map[string][]lineNode)      // file → decls sorted by line, for attribution
	declIndex := make(map[string]map[string]string) // file → label → nodeID
	fileNodeID := make(map[string]string)           // "service\x00file" → NodeTypeFile ID
	svcOfFile := make(map[string]string)            // file → service
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
	for _, fe := range jsFiles {
		rel := patterns.RelativizeToCwd(fe.abs)
		isTypes, _, _ := reduxPathRole(rel)
		if !isTypes {
			continue
		}
		src, root, _, ok := jsParse(fe.abs)
		if !ok {
			continue
		}
		for name, line := range reduxCollectActionTypes(root, src) {
			if actionTypeID[rel] == nil {
				actionTypeID[rel] = make(map[string]string)
			}
			actionTypeID[rel][name] = mkVar(fe.svc, rel, name, line)
		}
	}

	// === Phase 2: creators, reducers, dispatch sites ===
	for _, fe := range jsFiles {
		rel := patterns.RelativizeToCwd(fe.abs)
		svc := fe.svc
		src, root, _, ok := jsParse(fe.abs)
		if !ok {
			continue
		}
		_, isActions, isReducers := reduxPathRole(rel)
		imports := reduxImports(root, src, fe.abs, indexed)

		// resolveTypeRef turns `ActionTypes.FOO` / a bare imported `FOO` into the
		// action-type node ID, or "" when it isn't one we know.
		resolveTypeRef := func(v *sitter.Node) string {
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
		attrFrom := func(line int) string {
			if id := nearestDecl(declsByFile[rel], line); id != "" {
				return id
			}
			return fileNodeID[svc+"\x00"+rel]
		}

		// which local identifiers name an action-creators bundle?
		boundIdents := make(map[string]bool)
		reduxWalk(root, func(n *sitter.Node) {
			if n.Type() != "call_expression" {
				return
			}
			f := n.ChildByFieldName("function")
			if f == nil || f.Type() != "identifier" || f.Content(src) != "bindActionCreators" {
				return
			}
			if a := n.ChildByFieldName("arguments"); a != nil && a.NamedChildCount() > 0 {
				if a0 := a.NamedChild(0); a0.Type() == "identifier" {
					boundIdents[a0.Content(src)] = true
				}
			}
		})
		for local, imp := range imports {
			if _, ia, _ := reduxPathRole(imp.file); ia {
				boundIdents[local] = true
			}
		}
		acFile := ""
		for bi := range boundIdents {
			if imp, ok := imports[bi]; ok {
				if _, ia, _ := reduxPathRole(imp.file); ia {
					acFile = imp.file
					break
				}
			}
		}

		reduxWalk(root, func(n *sitter.Node) {
			switch n.Type() {
			case "pair":
				// creator:  foo: actionCreator(ActionTypes.FOO, …)
				if !isActions {
					return
				}
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
				tid := resolveTypeRef(a.NamedChild(0))
				if tid == "" {
					return
				}
				cname := strings.Trim(key.Content(src), "\"'`")
				cid := mkFn(svc, rel, cname, int(key.StartPoint().Row)+1, "creator")
				addEdge(graph.EdgeTypeReferences, cid, tid, "creator_type")

			case "object":
				// creator:  return { type: ActionTypes.FOO, … }
				if !isActions {
					return
				}
				for j := 0; j < int(n.NamedChildCount()); j++ {
					p := n.NamedChild(j)
					if p.Type() != "pair" {
						continue
					}
					k := p.ChildByFieldName("key")
					if k == nil || strings.Trim(k.Content(src), "\"'`") != "type" {
						continue
					}
					if tid := resolveTypeRef(p.ChildByFieldName("value")); tid != "" {
						addEdge(graph.EdgeTypeReferences, attrFrom(int(n.StartPoint().Row)+1), tid, "creator_type")
					}
				}

			case "switch_case":
				// reducer:  case ActionTypes.FOO:
				if !isReducers {
					return
				}
				if tid := resolveTypeRef(n.ChildByFieldName("value")); tid != "" {
					addEdge(graph.EdgeTypeReferences, tid, attrFrom(int(n.StartPoint().Row)+1), "type_handled_by")
				}

			case "binary_expression":
				// reducer:  action.type === ActionTypes.FOO
				if !isReducers {
					return
				}
				op := n.ChildByFieldName("operator")
				if op == nil || (op.Content(src) != "===" && op.Content(src) != "==") {
					return
				}
				for _, side := range []*sitter.Node{n.ChildByFieldName("left"), n.ChildByFieldName("right")} {
					if tid := resolveTypeRef(side); tid != "" {
						addEdge(graph.EdgeTypeReferences, tid, attrFrom(int(n.StartPoint().Row)+1), "type_handled_by")
					}
				}

			case "call_expression":
				// dispatch:  this.props.actions.foo(…) / props.actions.foo(…)
				if acFile == "" {
					return
				}
				f := n.ChildByFieldName("function")
				if f == nil || f.Type() != "member_expression" {
					return
				}
				prop := f.ChildByFieldName("property")
				obj := f.ChildByFieldName("object")
				if prop == nil || obj == nil || obj.Type() != "member_expression" {
					return
				}
				op := obj.ChildByFieldName("property")
				if op == nil || op.Content(src) != "actions" {
					return
				}
				oo := obj.ChildByFieldName("object")
				isProps := false
				switch {
				case oo == nil:
				case oo.Type() == "identifier" && oo.Content(src) == "props":
					isProps = true
				case oo.Type() == "member_expression":
					if pp := oo.ChildByFieldName("property"); pp != nil && pp.Content(src) == "props" {
						isProps = true
					}
				}
				if !isProps {
					return
				}
				name := prop.Content(src)
				toID := mkFn(svcOfFile[acFile], acFile, name, 1, "creator")
				addEdge(graph.EdgeTypeCalls, attrFrom(int(n.StartPoint().Row)+1), toID, "props_actions_dispatch")
			}
		})
	}

	return newNodes, newEdges
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
