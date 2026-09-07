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

// LinkJSMobx models MobX reactivity (Tier JCM.4) that the structural JS parser
// cannot see: which class members are observable / action / computed, and which
// observable members an autorun / reaction / when callback re-runs on.
//
//   - makeObservable(this, { count: observable, tick: action, total: computed })
//     stamps the matching member nodes with Meta["mobx"]=<kind>.
//   - makeAutoObservable(this) applies MobX's own default rule: data fields →
//     observable, methods → action, getters → computed (honouring an overrides
//     object's `key: false` / `key: <annotation>`).
//   - autorun(() => … this.x …) / reaction(() => this.x, …) / when(…) emit
//     callSite --reads--> <observable member> with Meta["mobx"]="reactive_read"
//     so "what recomputes when x changes" is a reverse-reads query.
//
// Member tags are delivered by re-emitting the existing member node with the
// extra Meta key (UpsertNode overwrites by ID). Reactive-read edges are
// ConfidenceInferred and Meta["tier"]="jcm4" so verification filters can drop
// the tier wholesale. Members named in a makeObservable map that no parser
// captured are minted as synthetic `variable` nodes.
//
// It returns freshly minted synthetic nodes (newNodes), existing member nodes
// re-emitted with an added Meta["mobx"] tag (taggedNodes), and the reactive-read
// edges. The caller upserts every node but only appends newNodes to its working
// set — taggedNodes already live there.
func LinkJSMobx(nodes []graph.Node, serviceFiles map[string][]string) (newNodes, taggedNodes []graph.Node, newEdges []graph.Edge) {
	var outNodes []graph.Node // synthetic nodes minted below
	// --- index existing nodes ---
	declsByFile := make(map[string][]lineNode)
	declIndex := make(map[string]map[string]string)
	fileNodeID := make(map[string]string)
	svcOfFile := make(map[string]string)
	// membersByFileClass: file → className → memberLabel → node index into `nodes`
	membersByFileClass := make(map[string]map[string]map[string]int)
	// JCM.7 cross-file lookups: className → memberLabel → node index, and a
	// case-insensitive class-name index (a `this.props.grid` prop resolves to a
	// `Grid` store class by name).
	membersByClass := make(map[string]map[string]int)
	classLower := make(map[string]string)
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeVariable, graph.NodeTypeClass, graph.NodeTypeMethod:
			if n.Label == "(module)" {
				continue
			}
			if n.Type == graph.NodeTypeClass {
				if _, ok := classLower[strings.ToLower(n.Label)]; !ok {
					classLower[strings.ToLower(n.Label)] = n.Label
				}
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
			if cls := n.Meta["class"]; cls != "" {
				if membersByFileClass[n.File] == nil {
					membersByFileClass[n.File] = make(map[string]map[string]int)
				}
				if membersByFileClass[n.File][cls] == nil {
					membersByFileClass[n.File][cls] = make(map[string]int)
				}
				if _, ok := membersByFileClass[n.File][cls][n.Label]; !ok {
					membersByFileClass[n.File][cls][n.Label] = i
				}
				if membersByClass[cls] == nil {
					membersByClass[cls] = make(map[string]int)
				}
				if _, ok := membersByClass[cls][n.Label]; !ok {
					membersByClass[cls][n.Label] = i
				}
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

	indexed := make(map[string]bool)
	type fileEnt struct{ abs, svc string }
	var jsFiles []fileEnt
	for svc, files := range serviceFiles {
		for _, f := range files {
			indexed[f] = true
			if isJSFile(f) {
				jsFiles = append(jsFiles, fileEnt{abs: f, svc: svc})
			}
		}
	}

	// nodes we re-emit with an added Meta["mobx"] tag, keyed by ID
	tagged := make(map[string]graph.Node)
	seenNode := make(map[string]bool)
	seenEdge := make(map[string]bool)

	// JCM.8: cross-store reactive-read resolution. jsAbsSet bounds import
	// following to the indexed workspace; bindCache memoises per-file
	// identifier/`this.<prop>` → store-class-name bindings.
	jsAbsSet := make(map[string]bool, len(jsFiles))
	for _, fe := range jsFiles {
		jsAbsSet[fe.abs] = true
	}
	type mobxBinds struct{ ident, thisProp map[string]string }
	bindCache := make(map[string]mobxBinds)
	getBinds := func(abs string, root *sitter.Node, src []byte) mobxBinds {
		if b, ok := bindCache[abs]; ok {
			return b
		}
		i, tp := mobxComputeBindings(abs, root, src, classLower, jsAbsSet)
		b := mobxBinds{i, tp}
		bindCache[abs] = b
		return b
	}
	// memberReactiveID resolves <class>.<member> to a node ID iff that member is
	// tagged observable|computed (consulting the live `tagged` overlay).
	memberReactiveID := func(canon, member string) string {
		mm := membersByClass[canon]
		if mm == nil {
			return ""
		}
		idx, ok := mm[member]
		if !ok {
			return ""
		}
		k := nodes[idx].Meta["mobx"]
		if t, ok2 := tagged[nodes[idx].ID]; ok2 {
			k = t.Meta["mobx"]
		}
		if k == "observable" || k == "computed" {
			return nodes[idx].ID
		}
		return ""
	}
	// addReactiveEdge emits callSite --reads--> observable. Same-class `this.x`
	// reads keep tier jcm4 (JCM.4 Pass B); cross-store hops are tier jcm8 and
	// carry Meta["mobx_resolve"]=<via>.
	addReactiveEdge := func(from, to, via string) {
		if from == "" || to == "" || from == to {
			return
		}
		eid := fmt.Sprintf("reads:%s->%s#mobx", from, to)
		if seenEdge[eid] {
			return
		}
		seenEdge[eid] = true
		meta := map[string]string{"mobx": "reactive_read"}
		if via == "" || via == "this" {
			meta["tier"] = "jcm4"
		} else {
			meta["tier"] = "jcm8"
			meta["mobx_resolve"] = via
		}
		newEdges = append(newEdges, graph.Edge{
			ID: eid, From: from, To: to, Type: graph.EdgeTypeReads, Confidence: graph.ConfidenceInferred,
			Meta: meta,
		})
	}

	tag := func(idx int, kind string) {
		n := nodes[idx]
		if n.Meta != nil && n.Meta["mobx"] == kind {
			return
		}
		m := make(map[string]string, len(n.Meta)+2)
		for k, v := range n.Meta {
			m[k] = v
		}
		m["mobx"] = kind
		m["tier"] = "jcm4"
		n.Meta = m
		tagged[n.ID] = n
	}

	for _, fe := range jsFiles {
		rel := patterns.RelativizeToCwd(fe.abs)
		svc := fe.svc
		if svc == "" {
			svc = svcOfFile[rel]
		}
		src, root, _, ok := jsParse(fe.abs)
		if !ok {
			continue
		}
		// cheap reject: no makeObservable surface and no legacy decorator surface
		low := string(src)
		hasMake := strings.Contains(low, "makeObservable") || strings.Contains(low, "makeAutoObservable") ||
			strings.Contains(low, "makeSimpleObservable")
		hasDeco := strings.Contains(low, "@observable") || strings.Contains(low, "@action") ||
			strings.Contains(low, "@computed") || strings.Contains(low, "@flow")
		if !hasMake && !hasDeco {
			continue
		}
		classMembers := membersByFileClass[rel]

		// resolve a member label within a class to its node ID, minting a
		// synthetic variable node when the parser never captured the field.
		memberID := func(cls, label string, line int) string {
			if m := classMembers[cls]; m != nil {
				if idx, ok := m[label]; ok {
					return nodes[idx].ID
				}
			}
			id := fmt.Sprintf("%s:%s:variable:%s:%d", svc, rel, label, line)
			if !seenNode[id] {
				seenNode[id] = true
				outNodes = append(outNodes, graph.Node{
					ID: id, Type: graph.NodeTypeVariable, Label: label, Service: svc, File: rel,
					Line: line, EndLine: line, Language: "javascript",
					Meta: map[string]string{"class": cls, "scope": "class_field", "tier": "jcm4", "synthetic": "true"},
				})
			}
			return id
		}
		_ = memberID

		mobxTag := func(cls, label, kind string, line int) {
			if m := classMembers[cls]; m != nil {
				if idx, ok := m[label]; ok {
					tag(idx, kind)
					return
				}
			}
			id := memberID(cls, label, line)
			// stamp the freshly minted synthetic node
			for i := range outNodes {
				if outNodes[i].ID == id {
					outNodes[i].Meta["mobx"] = kind
				}
			}
		}

		// Pass A: annotate members from makeObservable / makeAutoObservable, and
		// from legacy `@observable` / `@action` / `@computed` / `@flow` decorators
		// (MobX 4/5 and mid-migration codebases).
		mobxWalk(root, func(n *sitter.Node) {
			if n.Type() == "decorator" {
				if !hasDeco || n.NamedChildCount() == 0 {
					return
				}
				kind := mobxNormalizeAnnotation(mobxBaseAnnotation(n.NamedChild(0), src))
				if kind == "" {
					return
				}
				// A field decorator is a child of the field def; a method
				// decorator is a preceding sibling of the method_definition.
				owner := n.Parent()
				if owner != nil && owner.Type() == "class_body" {
					owner = n.NextNamedSibling()
				}
				if owner == nil {
					return
				}
				switch owner.Type() {
				case "method_definition", "public_field_definition", "field_definition":
				default:
					return
				}
				nm := owner.ChildByFieldName("name")
				if nm == nil {
					return
				}
				cls := mobxEnclosingClass(owner, src)
				if cls == "" {
					return
				}
				mobxTag(cls, nm.Content(src), kind, int(owner.StartPoint().Row)+1)
				return
			}
			if n.Type() != "call_expression" {
				return
			}
			f := n.ChildByFieldName("function")
			if f == nil || f.Type() != "identifier" {
				return
			}
			callee := f.Content(src)
			args := n.ChildByFieldName("arguments")
			switch callee {
			case "makeObservable":
				if args == nil || args.NamedChildCount() < 2 {
					return
				}
				cls := mobxEnclosingClass(n, src)
				if cls == "" {
					return
				}
				obj := args.NamedChild(1)
				if obj.Type() != "object" {
					return
				}
				for j := 0; j < int(obj.NamedChildCount()); j++ {
					p := obj.NamedChild(j)
					if p.Type() != "pair" {
						continue
					}
					k, v := p.ChildByFieldName("key"), p.ChildByFieldName("value")
					if k == nil || v == nil {
						continue
					}
					kind := mobxNormalizeAnnotation(mobxBaseAnnotation(v, src))
					if kind == "" {
						continue
					}
					label := strings.Trim(k.Content(src), "\"'`")
					mobxTag(cls, label, kind, int(k.StartPoint().Row)+1)
				}

			case "makeAutoObservable", "makeSimpleObservable":
				cls := mobxEnclosingClass(n, src)
				if cls == "" {
					return
				}
				// overrides: makeAutoObservable(this, { key: false | annotation })
				overrides := map[string]string{}
				if args != nil && args.NamedChildCount() >= 2 && args.NamedChild(1).Type() == "object" {
					o := args.NamedChild(1)
					for j := 0; j < int(o.NamedChildCount()); j++ {
						p := o.NamedChild(j)
						if p.Type() != "pair" {
							continue
						}
						k, v := p.ChildByFieldName("key"), p.ChildByFieldName("value")
						if k == nil || v == nil {
							continue
						}
						overrides[strings.Trim(k.Content(src), "\"'`")] = mobxBaseAnnotation(v, src)
					}
				}
				for label, idx := range classMembers[cls] {
					if label == "constructor" {
						continue // MobX never annotates the constructor
					}
					if ov, ok := overrides[label]; ok {
						if ov == "false" {
							continue
						}
						if kind := mobxNormalizeAnnotation(ov); kind != "" {
							tag(idx, kind)
							continue
						}
					}
					mn := nodes[idx]
					switch {
					case mn.Meta["js_accessor"] == "true":
						tag(idx, "computed")
					case mn.Type == graph.NodeTypeFunction || mn.Type == graph.NodeTypeMethod:
						tag(idx, "action")
					default:
						tag(idx, "observable")
					}
				}
			}
		})

	}

	// Pass B (JCM.4 + JCM.8): wire autorun / reaction / when callbacks to the
	// observable members they read. Runs as its own loop after Pass A so every
	// file's member tags are final before cross-file store resolution.
	for _, fe := range jsFiles {
		rel := patterns.RelativizeToCwd(fe.abs)
		svc := fe.svc
		if svc == "" {
			svc = svcOfFile[rel]
		}
		src, root, _, ok := jsParse(fe.abs)
		if !ok {
			continue
		}
		low := string(src)
		if !strings.Contains(low, "autorun") && !strings.Contains(low, "reaction(") && !strings.Contains(low, "when(") {
			continue
		}
		attrFrom := func(line int) string {
			if id := nearestDecl(declsByFile[rel], line); id != "" {
				return id
			}
			return fileNodeID[svc+"\x00"+rel]
		}
		bnd := getBinds(fe.abs, root, src)
		mobxWalk(root, func(n *sitter.Node) {
			if n.Type() != "call_expression" {
				return
			}
			f := n.ChildByFieldName("function")
			if f == nil || f.Type() != "identifier" {
				return
			}
			switch f.Content(src) {
			case "autorun", "reaction", "when", "autorunAsync":
			default:
				return
			}
			args := n.ChildByFieldName("arguments")
			if args == nil || args.NamedChildCount() == 0 {
				return
			}
			cb := args.NamedChild(0)
			if cb.Type() != "arrow_function" && cb.Type() != "function_expression" && cb.Type() != "function" {
				return
			}
			cls := mobxEnclosingClass(n, src)
			site := attrFrom(int(n.StartPoint().Row) + 1)
			mobxWalk(cb, func(m *sitter.Node) {
				if m.Type() != "member_expression" {
					return
				}
				canon, obs, via := mobxReadResolve(m, src, cls, classLower, bnd.ident, bnd.thisProp)
				if canon == "" {
					return
				}
				if id := memberReactiveID(canon, obs); id != "" {
					addReactiveEdge(site, id, via)
				}
			})
		})
	}

	// Pass C (JCM.7): an `observer(Component)` re-renders when the observables its
	// render body reads change. Walk every `observer(...)` wrapper, find the
	// render scope of its argument, resolve `this.props.<store>.<obs>` /
	// `this.<store>.<obs>` / `<store>.<obs>` reads to an observable|computed
	// member of a store class (matched case-insensitively by name), and emit
	// componentNode --reads--> member with Meta["mobx_via"]="observer_render".
	for _, fe := range jsFiles {
		rel := patterns.RelativizeToCwd(fe.abs)
		src, root, _, ok := jsParse(fe.abs)
		if !ok || !strings.Contains(string(src), "observer") {
			continue
		}
		perComp := map[string]int{}
		bnd := getBinds(fe.abs, root, src)
		hocWalk(root, func(call *sitter.Node) {
			if hocCalleeName(call, src) != "observer" {
				return
			}
			arg0 := hocFirstArg(call)
			if arg0 == nil {
				return
			}
			wrapperName, _ := hocBinding(call, src)
			var compID, compClass string
			var bodies []*sitter.Node
			switch arg0.Type() {
			case "identifier":
				compID = declIndex[rel][arg0.Content(src)]
				if sub := mobxDeclSubtree(root, src, arg0.Content(src)); sub != nil {
					bodies = mobxRenderBodies(sub, src)
					if nm := sub.ChildByFieldName("name"); nm != nil {
						compClass = nm.Content(src)
					}
				}
			case "class", "class_declaration":
				if nm := arg0.ChildByFieldName("name"); nm != nil {
					compID = declIndex[rel][nm.Content(src)]
					compClass = nm.Content(src)
				}
				if compID == "" && wrapperName != "" {
					compID = declIndex[rel][wrapperName]
				}
				bodies = mobxRenderBodies(arg0, src)
			case "arrow_function", "function", "function_expression":
				if wrapperName != "" {
					compID = declIndex[rel][wrapperName]
				}
				if compID == "" {
					if nm := arg0.ChildByFieldName("name"); nm != nil {
						compID = declIndex[rel][nm.Content(src)]
					}
				}
				if b := arg0.ChildByFieldName("body"); b != nil {
					bodies = append(bodies, b)
				}
			}
			if compID == "" || len(bodies) == 0 {
				return
			}
			for _, body := range bodies {
				mobxWalk(body, func(m *sitter.Node) {
					if m.Type() != "member_expression" || perComp[compID] >= 64 {
						return
					}
					canon, obs, via := mobxReadResolve(m, src, compClass, classLower, bnd.ident, bnd.thisProp)
					if canon == "" {
						return
					}
					id := memberReactiveID(canon, obs)
					if id == "" {
						return
					}
					eid := fmt.Sprintf("reads:%s->%s#mobx_obs", compID, id)
					if seenEdge[eid] {
						return
					}
					seenEdge[eid] = true
					perComp[compID]++
					meta := map[string]string{"mobx": "reactive_read", "mobx_via": "observer_render", "tier": "jcm7"}
					if via != "" && via != "this" && via != "props" {
						meta["mobx_resolve"] = via
					}
					newEdges = append(newEdges, graph.Edge{
						ID: eid, From: compID, To: id, Type: graph.EdgeTypeReads,
						Confidence: graph.ConfidenceInferred, Meta: meta,
					})
				})
			}
		})
	}

	// Pass D (JCM.9): computed → observable dependency edges. For every member
	// tagged mobx=computed, walk its getter/method body for `this.<obs>` reads
	// (and cross-store reads via the JCM.8 resolver) that resolve to an
	// observable|computed sibling, and emit computedMember --reads--> observable
	// Meta["mobx"]="computed_dep". Reverse-`reads` on an observable then
	// transitively reaches `observer` consumers through their computeds.
	computedFiles := make(map[string]bool)
	for _, n := range tagged {
		if n.Meta["mobx"] == "computed" {
			computedFiles[n.File] = true
		}
	}
	for _, fe := range jsFiles {
		rel := patterns.RelativizeToCwd(fe.abs)
		if !computedFiles[rel] {
			continue
		}
		src, root, _, ok := jsParse(fe.abs)
		if !ok {
			continue
		}
		bnd := getBinds(fe.abs, root, src)
		mobxWalk(root, func(n *sitter.Node) {
			if n.Type() != "method_definition" {
				return
			}
			nm := n.ChildByFieldName("name")
			body := n.ChildByFieldName("body")
			if nm == nil || body == nil {
				return
			}
			cls := mobxEnclosingClass(n, src)
			if cls == "" {
				return
			}
			mm := membersByClass[cls]
			if mm == nil {
				return
			}
			idx, ok := mm[nm.Content(src)]
			if !ok {
				return
			}
			kind := nodes[idx].Meta["mobx"]
			if t, ok2 := tagged[nodes[idx].ID]; ok2 {
				kind = t.Meta["mobx"]
			}
			if kind != "computed" {
				return
			}
			from := nodes[idx].ID
			deps := 0
			mobxWalk(body, func(m *sitter.Node) {
				if m.Type() != "member_expression" || deps >= 64 {
					return
				}
				canon, obs, _ := mobxReadResolve(m, src, cls, classLower, bnd.ident, bnd.thisProp)
				if canon == "" {
					return
				}
				to := memberReactiveID(canon, obs)
				if to == "" || to == from {
					return
				}
				eid := fmt.Sprintf("reads:%s->%s#mobx_cdep", from, to)
				if seenEdge[eid] {
					return
				}
				seenEdge[eid] = true
				deps++
				newEdges = append(newEdges, graph.Edge{
					ID: eid, From: from, To: to, Type: graph.EdgeTypeReads,
					Confidence: graph.ConfidenceInferred,
					Meta:       map[string]string{"mobx": "computed_dep", "tier": "jcm9"},
				})
			})
		})
	}

	for _, n := range tagged {
		taggedNodes = append(taggedNodes, n)
	}
	return outNodes, taggedNodes, newEdges
}

// mobxRenderBodies returns the reactive render scope(s) of a component: a class's
// `render` method body, or a function/arrow component's body.
func mobxRenderBodies(decl *sitter.Node, src []byte) []*sitter.Node {
	switch decl.Type() {
	case "class", "class_declaration":
		var body *sitter.Node
		for i := 0; i < int(decl.NamedChildCount()); i++ {
			if c := decl.NamedChild(i); c.Type() == "class_body" {
				body = c
				break
			}
		}
		if body == nil {
			return nil
		}
		var out []*sitter.Node
		for i := 0; i < int(body.NamedChildCount()); i++ {
			md := body.NamedChild(i)
			if md.Type() != "method_definition" {
				continue
			}
			if nm := md.ChildByFieldName("name"); nm != nil && nm.Content(src) == "render" {
				if b := md.ChildByFieldName("body"); b != nil {
					out = append(out, b)
				}
			}
		}
		return out
	case "arrow_function", "function", "function_expression":
		if b := decl.ChildByFieldName("body"); b != nil {
			return []*sitter.Node{b}
		}
	}
	return nil
}

// mobxDeclSubtree finds the class/function/arrow subtree bound to `name` at file
// scope, so an `observer(Name)` by identifier can be followed to its render body.
func mobxDeclSubtree(root *sitter.Node, src []byte, name string) *sitter.Node {
	var found *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != nil {
			return
		}
		switch n.Type() {
		case "class_declaration", "function_declaration":
			if nm := n.ChildByFieldName("name"); nm != nil && nm.Content(src) == name {
				found = n
				return
			}
		case "variable_declarator":
			if nm := n.ChildByFieldName("name"); nm != nil && nm.Content(src) == name {
				if v := n.ChildByFieldName("value"); v != nil {
					switch v.Type() {
					case "arrow_function", "function", "function_expression", "class":
						found = v
						return
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return found
}

// mobxReadResolve classifies a `member_expression` read `<recv>.<obs>` as
// targeting an observable member of a store class, and reports which resolution
// rule matched (via):
//
//   - "this"       — same-class `this.<obs>` (JCM.4 Pass B)
//   - "import"     — `<ident>.<obs>` where <ident> is an imported/module-local
//     store instance (`import s from "./s"` / `const s = new S()`)
//   - "this_field" — `this.<prop>.<obs>` where <prop> = `new StoreClass()`
//   - "props"      — `this.props.<store>.<obs>` (store class matched by name)
//   - "root_store" — `<root>.<sub>.<obs>` / `this.<root>.<sub>.<obs>` where
//     <root> is a store instance and <sub> names a store class (one hop)
//
// classLower maps a lowercased class name to its canonical label; identStore /
// thisPropStore come from mobxComputeBindings.
func mobxReadResolve(m *sitter.Node, src []byte, sameClass string, classLower, identStore, thisPropStore map[string]string) (canon, obs, via string) {
	prop := m.ChildByFieldName("property")
	obj := m.ChildByFieldName("object")
	if prop == nil || obj == nil || prop.Type() != "property_identifier" {
		return "", "", ""
	}
	obs = prop.Content(src)
	switch obj.Type() {
	case "this":
		if sameClass != "" {
			return sameClass, obs, "this"
		}
	case "identifier":
		if c := identStore[obj.Content(src)]; c != "" {
			return c, obs, "import"
		}
		// fallback: a bare `grid.total` where the receiver name *is* a store
		// class (destructured / same-name singleton the import walk missed).
		if c := classLower[strings.ToLower(obj.Content(src))]; c != "" {
			return c, obs, "name"
		}
	case "member_expression":
		p2 := obj.ChildByFieldName("property")
		o2 := obj.ChildByFieldName("object")
		if p2 == nil || o2 == nil || p2.Type() != "property_identifier" {
			return "", "", ""
		}
		switch o2.Type() {
		case "this":
			if c := thisPropStore[p2.Content(src)]; c != "" {
				return c, obs, "this_field"
			}
			if c := classLower[strings.ToLower(p2.Content(src))]; c != "" {
				return c, obs, "name"
			}
		case "identifier":
			if identStore[o2.Content(src)] != "" {
				if c := classLower[strings.ToLower(p2.Content(src))]; c != "" {
					return c, obs, "root_store"
				}
			}
		case "member_expression":
			pp := o2.ChildByFieldName("property")
			oo := o2.ChildByFieldName("object")
			if pp == nil || oo == nil || oo.Type() != "this" {
				return "", "", ""
			}
			if pp.Content(src) == "props" {
				if c := classLower[strings.ToLower(p2.Content(src))]; c != "" {
					return c, obs, "props"
				}
			} else if thisPropStore[pp.Content(src)] != "" {
				if c := classLower[strings.ToLower(p2.Content(src))]; c != "" {
					return c, obs, "root_store"
				}
			}
		}
	}
	return "", "", ""
}

// mobxNewExprClass returns the canonical store-class label a `new_expression`
// constructs, or "" when the constructor isn't a known class.
func mobxNewExprClass(n *sitter.Node, src []byte, classLower map[string]string) string {
	if n.Type() != "new_expression" {
		return ""
	}
	c := n.ChildByFieldName("constructor")
	if c == nil || c.Type() != "identifier" {
		return ""
	}
	return classLower[strings.ToLower(c.Content(src))]
}

// mobxNewBindingName reports what a `new_expression` is bound to: a plain
// identifier (`const s = new S()`) or a `this.<prop>` field
// (`this.s = new S()` / class field `s = new S()`).
func mobxNewBindingName(n *sitter.Node, src []byte) (ident, thisProp string) {
	for p, hops := n.Parent(), 0; p != nil && hops < 4; p, hops = p.Parent(), hops+1 {
		switch p.Type() {
		case "variable_declarator":
			if nm := p.ChildByFieldName("name"); nm != nil && nm.Type() == "identifier" {
				return nm.Content(src), ""
			}
			return "", ""
		case "assignment_expression":
			l := p.ChildByFieldName("left")
			if l == nil {
				return "", ""
			}
			if l.Type() == "identifier" {
				return l.Content(src), ""
			}
			if l.Type() == "member_expression" {
				o, pr := l.ChildByFieldName("object"), l.ChildByFieldName("property")
				if o != nil && pr != nil && o.Type() == "this" {
					return "", pr.Content(src)
				}
			}
			return "", ""
		case "public_field_definition", "field_definition":
			if nm := p.ChildByFieldName("name"); nm != nil {
				return "", nm.Content(src)
			}
			return "", ""
		}
	}
	return "", ""
}

// mobxModuleStoreExports reports which store class a module's default export is
// an instance of, and a name→class map for its named exports of store
// instances (`export default new S()`, `export const s = new S()`,
// `const s = new S(); export default s`, `export { s }`).
func mobxModuleStoreExports(root *sitter.Node, src []byte, classLower map[string]string) (def string, named map[string]string) {
	named = map[string]string{}
	local := map[string]string{}
	var collect func(n *sitter.Node)
	collect = func(n *sitter.Node) {
		if n.Type() == "new_expression" {
			if canon := mobxNewExprClass(n, src, classLower); canon != "" {
				if id, _ := mobxNewBindingName(n, src); id != "" {
					local[id] = canon
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			collect(n.NamedChild(i))
		}
	}
	collect(root)

	var visit func(n *sitter.Node)
	visit = func(n *sitter.Node) {
		if n.Type() == "export_statement" {
			if v := n.ChildByFieldName("value"); v != nil {
				switch v.Type() {
				case "new_expression":
					if canon := mobxNewExprClass(v, src, classLower); canon != "" {
						def = canon
					}
				case "identifier":
					if canon := local[v.Content(src)]; canon != "" {
						def = canon
					}
				}
			}
			if d := n.ChildByFieldName("declaration"); d != nil {
				var dw func(m *sitter.Node)
				dw = func(m *sitter.Node) {
					if m.Type() == "variable_declarator" {
						nm, val := m.ChildByFieldName("name"), m.ChildByFieldName("value")
						if nm != nil && val != nil && val.Type() == "new_expression" {
							if canon := mobxNewExprClass(val, src, classLower); canon != "" {
								named[nm.Content(src)] = canon
							}
						}
					}
					for i := 0; i < int(m.NamedChildCount()); i++ {
						dw(m.NamedChild(i))
					}
				}
				dw(d)
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				ec := n.NamedChild(i)
				if ec.Type() != "export_clause" {
					continue
				}
				for j := 0; j < int(ec.NamedChildCount()); j++ {
					sp := ec.NamedChild(j)
					if sp.Type() != "export_specifier" {
						continue
					}
					nm := sp.ChildByFieldName("name")
					if nm == nil {
						continue
					}
					out := nm.Content(src)
					if al := sp.ChildByFieldName("alias"); al != nil {
						out = al.Content(src)
					}
					if canon := local[nm.Content(src)]; canon != "" {
						named[out] = canon
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			visit(n.NamedChild(i))
		}
	}
	visit(root)
	return def, named
}

// mobxComputeBindings resolves, for one file, which local identifiers and
// `this.<prop>` fields refer to a store-class instance — via module-local
// `new S()`, `this.x = new S()`, and imported store singletons (following
// relative imports one level, bounded by jsAbs).
func mobxComputeBindings(fileAbs string, root *sitter.Node, src []byte, classLower map[string]string, jsAbs map[string]bool) (identStore, thisPropStore map[string]string) {
	identStore = map[string]string{}
	thisPropStore = map[string]string{}

	resolveImport := func(spec string) string {
		if !strings.HasPrefix(spec, ".") {
			return ""
		}
		base := filepath.Join(filepath.Dir(fileAbs), spec)
		for _, cand := range []string{
			base, base + ".js", base + ".jsx", base + ".ts", base + ".tsx", base + ".mjs", base + ".es6",
			filepath.Join(base, "index.js"), filepath.Join(base, "index.jsx"),
			filepath.Join(base, "index.ts"), filepath.Join(base, "index.tsx"),
		} {
			if jsAbs[cand] {
				return cand
			}
		}
		return ""
	}

	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "new_expression":
			if canon := mobxNewExprClass(n, src, classLower); canon != "" {
				id, tp := mobxNewBindingName(n, src)
				if id != "" {
					identStore[id] = canon
				}
				if tp != "" {
					thisPropStore[tp] = canon
				}
			}
		case "import_statement":
			spec := ""
			if s := n.ChildByFieldName("source"); s != nil {
				spec = strings.Trim(s.Content(src), "\"'`")
			}
			target := resolveImport(spec)
			if target == "" {
				break
			}
			tsrc, troot, _, ok := jsParse(target)
			if !ok {
				break
			}
			def, namedExp := mobxModuleStoreExports(troot, tsrc, classLower)
			for i := 0; i < int(n.NamedChildCount()); i++ {
				ic := n.NamedChild(i)
				if ic.Type() != "import_clause" {
					continue
				}
				for j := 0; j < int(ic.NamedChildCount()); j++ {
					cc := ic.NamedChild(j)
					switch cc.Type() {
					case "identifier":
						if def != "" {
							identStore[cc.Content(src)] = def
						}
					case "named_imports":
						for k := 0; k < int(cc.NamedChildCount()); k++ {
							sp := cc.NamedChild(k)
							if sp.Type() != "import_specifier" {
								continue
							}
							nm := sp.ChildByFieldName("name")
							if nm == nil {
								continue
							}
							localName := nm.Content(src)
							if al := sp.ChildByFieldName("alias"); al != nil {
								localName = al.Content(src)
							}
							if canon := namedExp[nm.Content(src)]; canon != "" {
								identStore[localName] = canon
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
	return identStore, thisPropStore
}

// mobxBaseAnnotation returns the leftmost identifier of a MobX annotation
// expression: `observable` / `observable.ref` / `action.bound` / `computed()` /
// `false` → "observable"/"action"/"computed"/"false".
func mobxBaseAnnotation(v *sitter.Node, src []byte) string {
	switch v.Type() {
	case "identifier":
		return v.Content(src)
	case "false", "true":
		return v.Type()
	case "member_expression":
		if o := v.ChildByFieldName("object"); o != nil {
			return mobxBaseAnnotation(o, src)
		}
	case "call_expression":
		if f := v.ChildByFieldName("function"); f != nil {
			return mobxBaseAnnotation(f, src)
		}
	}
	return ""
}

func mobxNormalizeAnnotation(base string) string {
	switch base {
	case "observable":
		return "observable"
	case "action", "flow":
		return "action"
	case "computed":
		return "computed"
	}
	return ""
}

// mobxEnclosingClass walks up from a node to the nearest class and returns its
// name, or "".
func mobxEnclosingClass(n *sitter.Node, src []byte) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.Type() == "class_declaration" || p.Type() == "class" {
			if nm := p.ChildByFieldName("name"); nm != nil {
				return nm.Content(src)
			}
			return ""
		}
	}
	return ""
}

func mobxWalk(n *sitter.Node, fn func(*sitter.Node)) {
	fn(n)
	for i := 0; i < int(n.NamedChildCount()); i++ {
		mobxWalk(n.NamedChild(i), fn)
	}
}
