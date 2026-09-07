package linker

import (
	"fmt"
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

	addEdge := func(from, to string) {
		if from == "" || to == "" || from == to {
			return
		}
		eid := fmt.Sprintf("reads:%s->%s#mobx", from, to)
		if seenEdge[eid] {
			return
		}
		seenEdge[eid] = true
		newEdges = append(newEdges, graph.Edge{
			ID: eid, From: from, To: to, Type: graph.EdgeTypeReads, Confidence: graph.ConfidenceInferred,
			Meta: map[string]string{"mobx": "reactive_read", "tier": "jcm4"},
		})
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
		// cheap reject: no mobx surface at all
		low := string(src)
		if !strings.Contains(low, "makeObservable") && !strings.Contains(low, "makeAutoObservable") &&
			!strings.Contains(low, "autorun") && !strings.Contains(low, "reaction(") && !strings.Contains(low, "when(") {
			continue
		}

		attrFrom := func(line int) string {
			if id := nearestDecl(declsByFile[rel], line); id != "" {
				return id
			}
			return fileNodeID[svc+"\x00"+rel]
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

		// Pass A: annotate members from makeObservable / makeAutoObservable.
		mobxWalk(root, func(n *sitter.Node) {
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

		// Pass B: wire autorun / reaction / when callbacks to the observable
		// members they read (uses the tags from Pass A regardless of order).
		reactiveKind := func(cls, label string) string {
			if cls == "" {
				return ""
			}
			mm := classMembers[cls]
			if mm == nil {
				return ""
			}
			idx, ok := mm[label]
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
				o, p := m.ChildByFieldName("object"), m.ChildByFieldName("property")
				if o == nil || p == nil || o.Type() != "this" {
					return
				}
				if id := reactiveKind(cls, p.Content(src)); id != "" {
					addEdge(site, id)
				}
			})
		})
	}

	for _, n := range tagged {
		taggedNodes = append(taggedNodes, n)
	}
	return outNodes, taggedNodes, newEdges
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
