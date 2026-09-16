package factpipe

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_js_mobx.go is the "js_mobx_sites" hub provider (see hub.go) — the Tier
// FX FX.8.9 migration of internal/linker/js_mobx.go's retired LinkJSMobx.
//
// Every piece of this stays hand-written Go, for the same reasons
// hub_js_hoc.go's package doc gives (FX.8.8): this is real, order-sensitive
// tree-sitter structural analysis across FOUR sequential passes that share
// live, mutating state (a same-run `tagged` overlay Pass B/C/D all consult
// to see Pass A's member-kind stamps, since a `.dl` fact set has no
// "overwrite and re-read within this same hub call" concept) plus a
// cross-file import-following resolver (mobxComputeBindings) no `.dl` join
// could express. rules/javascript/js_mobx.dl is pure pass-through.
//
// `patch:` for existing-node member tagging (Pass A → observable/action/
// computed), `mint:` for a member no parser captured (synthetic
// `variable` node, frozenNodeTypes already has it from FX.8.8). The three
// "reads" edge shapes (Pass B autorun/reaction/when, Pass C observer-render,
// Pass D computed→observable dependency) branch their Tier/Via meta in Go —
// the "no string-emptiness test in `.dl`" restriction FX.8.44/45 already
// established — landing in three separate relations distinguished by a
// literal `label:` (so their graph.Edge IDs, which fold in `from`/`to`/
// `label`, never collide the way three same-(from,to) reads from different
// tiers legitimately can and, in the retired Go's own per-pass `seenEdge`
// keying, were meant to coexist as distinct edges).
func init() { RegisterHub("js_mobx_sites", jsMobxSitesHub) }

const (
	jsMobxPatchPred = "js_mobx_patch"  // (TargetID, Kind)
	jsMobxMintPred  = "js_mobx_mint"   // (ID, Label, Svc, File, Line, EndLine, Class, Kind)
	jsMobxReadBPred = "js_mobx_read_b" // (From, To, Tier, Via) — Pass B: autorun/reaction/when
	jsMobxReadCPred = "js_mobx_read_c" // (From, To, Via) — Pass C: observer-render
	jsMobxReadDPred = "js_mobx_read_d" // (From, To) — Pass D: computed->observable dep
)

// jsmLineNode / jsmNearestDecl: ported from internal/linker's lineNode /
// nearestDecl (js_linker.go / js_type_relations.go) — the "nearest preceding
// declaration" idiom Pass B's attrFrom uses to attribute an autorun call
// site with no enclosing function to the nearest declaration above it.
type jsmLineNode struct {
	line int
	id   string
}

func jsmNearestDecl(fileDecls []jsmLineNode, refLine int) string {
	id := ""
	for _, d := range fileDecls {
		if d.line <= refLine {
			id = d.id
		} else {
			break
		}
	}
	return id
}

type jsmMint struct {
	label, svc, file, class, kind string
	line, endLine                 int
}

func jsMobxSitesHub(nodes []graph.Node, files []string, _ string, _ []graph.LinkHint, _ graph.SchemaConfig) []Fact {
	declsByFile := make(map[string][]jsmLineNode)
	declIndex := make(map[string]map[string]string)
	fileNodeID := make(map[string]string)
	svcOfFile := make(map[string]string)
	membersByFileClass := make(map[string]map[string]map[string]int)
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
			declsByFile[n.File] = append(declsByFile[n.File], jsmLineNode{line: n.Line, id: n.ID})
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

	var jsFiles []string
	for _, f := range files {
		if jhcIsJSFile(f) {
			jsFiles = append(jsFiles, f)
		}
	}
	if len(jsFiles) == 0 {
		return nil
	}
	sort.Strings(jsFiles)

	jsAbsSet := make(map[string]bool, len(jsFiles))
	for _, f := range jsFiles {
		jsAbsSet[f] = true
	}

	tagged := make(map[string]string) // node ID -> final mobx kind
	var taggedOrder []string
	seenNode := make(map[string]bool)
	seenEdge := make(map[string]bool)
	minted := make(map[string]*jsmMint)
	var mintedOrder []string

	var out []Fact

	tag := func(idx int, kind string) {
		id := nodes[idx].ID
		if tagged[id] == kind {
			return
		}
		if _, seen := tagged[id]; !seen {
			taggedOrder = append(taggedOrder, id)
		}
		tagged[id] = kind
	}

	type mobxBinds struct{ ident, thisProp map[string]string }
	bindCache := make(map[string]mobxBinds)
	getBinds := func(abs string, root *sitter.Node, src []byte) mobxBinds {
		if b, ok := bindCache[abs]; ok {
			return b
		}
		i, tp := jsmComputeBindings(abs, root, src, classLower, jsAbsSet)
		b := mobxBinds{i, tp}
		bindCache[abs] = b
		return b
	}
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
			k = t
		}
		if k == "observable" || k == "computed" {
			return nodes[idx].ID
		}
		return ""
	}

	// --- Pass A: makeObservable / makeAutoObservable / legacy decorators. ---
	for _, abs := range jsFiles {
		rel := jhcRelativize(abs)
		svc := svcOfFile[rel]
		src, root, ok := jhcParseJS(abs)
		if !ok || root == nil {
			continue
		}
		low := string(src)
		hasMake := strings.Contains(low, "makeObservable") || strings.Contains(low, "makeAutoObservable") ||
			strings.Contains(low, "makeSimpleObservable")
		hasDeco := strings.Contains(low, "@observable") || strings.Contains(low, "@action") ||
			strings.Contains(low, "@computed") || strings.Contains(low, "@flow")
		if !hasMake && !hasDeco {
			continue
		}
		classMembers := membersByFileClass[rel]

		memberID := func(cls, label string, line int) string {
			if m := classMembers[cls]; m != nil {
				if idx, ok := m[label]; ok {
					return nodes[idx].ID
				}
			}
			id := fmt.Sprintf("%s:%s:variable:%s:%d", svc, rel, label, line)
			if !seenNode[id] {
				seenNode[id] = true
				mintedOrder = append(mintedOrder, id)
				minted[id] = &jsmMint{
					label: label, svc: svc, file: rel, class: cls,
					line: line, endLine: line,
				}
			}
			return id
		}
		mobxTag := func(cls, label, kind string, line int) {
			if m := classMembers[cls]; m != nil {
				if idx, ok := m[label]; ok {
					tag(idx, kind)
					return
				}
			}
			id := memberID(cls, label, line)
			if m := minted[id]; m != nil {
				m.kind = kind
			}
		}

		jsmWalk(root, func(n *sitter.Node) {
			if n.Type() == "decorator" {
				if !hasDeco || n.NamedChildCount() == 0 {
					return
				}
				kind := jsmNormalizeAnnotation(jsmBaseAnnotation(n.NamedChild(0), src))
				if kind == "" {
					return
				}
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
				cls := jsmEnclosingClass(owner, src)
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
				cls := jsmEnclosingClass(n, src)
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
					kind := jsmNormalizeAnnotation(jsmBaseAnnotation(v, src))
					if kind == "" {
						continue
					}
					label := strings.Trim(k.Content(src), "\"'`")
					mobxTag(cls, label, kind, int(k.StartPoint().Row)+1)
				}

			case "makeAutoObservable", "makeSimpleObservable":
				cls := jsmEnclosingClass(n, src)
				if cls == "" {
					return
				}
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
						overrides[strings.Trim(k.Content(src), "\"'`")] = jsmBaseAnnotation(v, src)
					}
				}
				for label, idx := range classMembers[cls] {
					if label == "constructor" {
						continue
					}
					if ov, ok := overrides[label]; ok {
						if ov == "false" {
							continue
						}
						if kind := jsmNormalizeAnnotation(ov); kind != "" {
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

	// computedFiles backs Pass D's gate below — built from Pass A's `tagged`
	// overlay, so it must be computed after Pass A finishes and before the
	// merged B/C/D walk starts.
	computedFiles := make(map[string]bool)
	for id, kind := range tagged {
		if kind == "computed" {
			for i := range nodes {
				if nodes[i].ID == id {
					computedFiles[nodes[i].File] = true
					break
				}
			}
		}
	}

	// --- Passes B (JCM.4+8) / C (JCM.7) / D (JCM.9) merged. ---
	// Each of these only reads Pass A's `tagged` overlay (via
	// memberReactiveID / computedFiles) — none reads state either of the
	// other two writes — so they combine into a single per-file walk instead
	// of three (XM.20). B and C both match only call_expression (disjoint
	// callee names: autorun/reaction/when/autorunAsync vs. observer/
	// React.observer); D matches only method_definition — disjoint node
	// types, so a single top-level switch dispatches all three.
	for _, abs := range jsFiles {
		rel := jhcRelativize(abs)
		svc := svcOfFile[rel]
		src, root, ok := jhcParseJS(abs)
		if !ok || root == nil {
			continue
		}
		low := string(src)
		hasB := strings.Contains(low, "autorun") || strings.Contains(low, "reaction(") || strings.Contains(low, "when(")
		hasC := strings.Contains(low, "observer")
		hasD := computedFiles[rel]
		if !hasB && !hasC && !hasD {
			continue
		}
		bnd := getBinds(abs, root, src)
		attrFrom := func(line int) string {
			if id := jsmNearestDecl(declsByFile[rel], line); id != "" {
				return id
			}
			return fileNodeID[svc+"\x00"+rel]
		}
		perComp := map[string]int{}

		jsmWalk(root, func(n *sitter.Node) {
			switch n.Type() {
			case "call_expression":
				if hasB {
					if f := n.ChildByFieldName("function"); f != nil && f.Type() == "identifier" {
						switch f.Content(src) {
						case "autorun", "reaction", "when", "autorunAsync":
							args := n.ChildByFieldName("arguments")
							if args == nil || args.NamedChildCount() == 0 {
								return
							}
							cb := args.NamedChild(0)
							if cb.Type() != "arrow_function" && cb.Type() != "function_expression" && cb.Type() != "function" {
								return
							}
							cls := jsmEnclosingClass(n, src)
							site := attrFrom(int(n.StartPoint().Row) + 1)
							jsmWalk(cb, func(m *sitter.Node) {
								if m.Type() != "member_expression" {
									return
								}
								canon, obs, via := jsmReadResolve(m, src, cls, classLower, bnd.ident, bnd.thisProp)
								if canon == "" {
									return
								}
								id := memberReactiveID(canon, obs)
								if id == "" {
									return
								}
								if site == "" || id == "" || site == id {
									return
								}
								eid := "reads:" + site + "->" + id + "#mobx"
								if seenEdge[eid] {
									return
								}
								seenEdge[eid] = true
								tier, viaOut := "jcm4", ""
								if via != "" && via != "this" {
									tier, viaOut = "jcm8", via
								}
								out = append(out, Fact{
									Pred:   jsMobxReadBPred,
									Args:   []Atom{Str(site), Str(id), Str(tier), Str(viaOut)},
									Origin: Origin{Kind: OriginPrimitive, Pattern: jsMobxReadBPred},
								})
							})
							return
						}
					}
				}
				if hasC && jhcCalleeName(n, src) == "observer" {
					arg0 := jhcFirstArg(n)
					if arg0 == nil {
						return
					}
					wrapperName, _ := jhcBinding(n, src)
					var compID, compClass string
					var bodies []*sitter.Node
					switch arg0.Type() {
					case "identifier":
						compID = declIndex[rel][arg0.Content(src)]
						if sub := jsmDeclSubtree(root, src, arg0.Content(src)); sub != nil {
							bodies = jsmRenderBodies(sub, src)
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
						bodies = jsmRenderBodies(arg0, src)
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
						jsmWalk(body, func(m *sitter.Node) {
							if m.Type() != "member_expression" || perComp[compID] >= 64 {
								return
							}
							canon, obs, via := jsmReadResolve(m, src, compClass, classLower, bnd.ident, bnd.thisProp)
							if canon == "" {
								return
							}
							id := memberReactiveID(canon, obs)
							if id == "" {
								return
							}
							eid := "reads:" + compID + "->" + id + "#mobx_obs"
							if seenEdge[eid] {
								return
							}
							seenEdge[eid] = true
							perComp[compID]++
							viaOut := ""
							if via != "" && via != "this" && via != "props" {
								viaOut = via
							}
							out = append(out, Fact{
								Pred:   jsMobxReadCPred,
								Args:   []Atom{Str(compID), Str(id), Str(viaOut)},
								Origin: Origin{Kind: OriginPrimitive, Pattern: jsMobxReadCPred},
							})
						})
					}
				}

			case "method_definition":
				if !hasD {
					return
				}
				nm := n.ChildByFieldName("name")
				body := n.ChildByFieldName("body")
				if nm == nil || body == nil {
					return
				}
				cls := jsmEnclosingClass(n, src)
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
					kind = t
				}
				if kind != "computed" {
					return
				}
				from := nodes[idx].ID
				deps := 0
				jsmWalk(body, func(m *sitter.Node) {
					if m.Type() != "member_expression" || deps >= 64 {
						return
					}
					canon, obs, _ := jsmReadResolve(m, src, cls, classLower, bnd.ident, bnd.thisProp)
					if canon == "" {
						return
					}
					to := memberReactiveID(canon, obs)
					if to == "" || to == from {
						return
					}
					eid := "reads:" + from + "->" + to + "#mobx_cdep"
					if seenEdge[eid] {
						return
					}
					seenEdge[eid] = true
					deps++
					out = append(out, Fact{
						Pred:   jsMobxReadDPred,
						Args:   []Atom{Str(from), Str(to)},
						Origin: Origin{Kind: OriginPrimitive, Pattern: jsMobxReadDPred},
					})
				})
			}
		})
	}

	for _, id := range taggedOrder {
		out = append(out, Fact{
			Pred:   jsMobxPatchPred,
			Args:   []Atom{Str(id), Str(tagged[id])},
			Origin: Origin{Kind: OriginPrimitive, Pattern: jsMobxPatchPred},
		})
	}
	for _, id := range mintedOrder {
		m := minted[id]
		out = append(out, Fact{
			Pred: jsMobxMintPred,
			Args: []Atom{
				Str(id), Str(m.label), Str(m.svc), Str(m.file),
				Int(int64(m.line)), Int(int64(m.endLine)), Str(m.class), Str(m.kind),
			},
			Origin: Origin{Kind: OriginPrimitive, File: m.file, Line: m.line, Pattern: jsMobxMintPred},
		})
	}
	return out
}

// --- ported structural helpers (jsm-prefixed, from js_mobx.go) ---

func jsmRenderBodies(decl *sitter.Node, src []byte) []*sitter.Node {
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
		var outB []*sitter.Node
		for i := 0; i < int(body.NamedChildCount()); i++ {
			md := body.NamedChild(i)
			if md.Type() != "method_definition" {
				continue
			}
			if nm := md.ChildByFieldName("name"); nm != nil && nm.Content(src) == "render" {
				if b := md.ChildByFieldName("body"); b != nil {
					outB = append(outB, b)
				}
			}
		}
		return outB
	case "arrow_function", "function", "function_expression":
		if b := decl.ChildByFieldName("body"); b != nil {
			return []*sitter.Node{b}
		}
	}
	return nil
}

func jsmDeclSubtree(root *sitter.Node, src []byte, name string) *sitter.Node {
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

func jsmReadResolve(m *sitter.Node, src []byte, sameClass string, classLower, identStore, thisPropStore map[string]string) (canon, obs, via string) {
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

func jsmNewExprClass(n *sitter.Node, src []byte, classLower map[string]string) string {
	if n.Type() != "new_expression" {
		return ""
	}
	c := n.ChildByFieldName("constructor")
	if c == nil || c.Type() != "identifier" {
		return ""
	}
	return classLower[strings.ToLower(c.Content(src))]
}

func jsmNewBindingName(n *sitter.Node, src []byte) (ident, thisProp string) {
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

func jsmModuleStoreExports(root *sitter.Node, src []byte, classLower map[string]string) (def string, named map[string]string) {
	named = map[string]string{}
	local := map[string]string{}
	var collect func(n *sitter.Node)
	collect = func(n *sitter.Node) {
		if n.Type() == "new_expression" {
			if canon := jsmNewExprClass(n, src, classLower); canon != "" {
				if id, _ := jsmNewBindingName(n, src); id != "" {
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
					if canon := jsmNewExprClass(v, src, classLower); canon != "" {
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
							if canon := jsmNewExprClass(val, src, classLower); canon != "" {
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
					outName := nm.Content(src)
					if al := sp.ChildByFieldName("alias"); al != nil {
						outName = al.Content(src)
					}
					if canon := local[nm.Content(src)]; canon != "" {
						named[outName] = canon
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

func jsmComputeBindings(fileAbs string, root *sitter.Node, src []byte, classLower map[string]string, jsAbs map[string]bool) (identStore, thisPropStore map[string]string) {
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
			if canon := jsmNewExprClass(n, src, classLower); canon != "" {
				id, tp := jsmNewBindingName(n, src)
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
			tsrc, troot, ok := jhcParseJS(target)
			if !ok || troot == nil {
				break
			}
			def, namedExp := jsmModuleStoreExports(troot, tsrc, classLower)
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

func jsmBaseAnnotation(v *sitter.Node, src []byte) string {
	switch v.Type() {
	case "identifier":
		return v.Content(src)
	case "false", "true":
		return v.Type()
	case "member_expression":
		if o := v.ChildByFieldName("object"); o != nil {
			return jsmBaseAnnotation(o, src)
		}
	case "call_expression":
		if f := v.ChildByFieldName("function"); f != nil {
			return jsmBaseAnnotation(f, src)
		}
	}
	return ""
}

func jsmNormalizeAnnotation(base string) string {
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

func jsmEnclosingClass(n *sitter.Node, src []byte) string {
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

func jsmWalk(n *sitter.Node, fn func(*sitter.Node)) {
	fn(n)
	for i := 0; i < int(n.NamedChildCount()); i++ {
		jsmWalk(n.NamedChild(i), fn)
	}
}
