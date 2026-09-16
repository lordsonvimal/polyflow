package factpipe

import (
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_js_client_routes.go is the "js_client_routes_sites" hub provider (see
// hub.go) — the Tier FX FX.8.10 migration of
// internal/linker/js_client_routes.go's retired LinkJSClientRoutes.
//
// Same shape as js_hoc/js_mobx (FX.8.8/9): a route table + `switch
// (routeName)` case-to-component map + SPA.3 feature-registry resolution +
// RT.1 render-target retyping, all built from a single pass over every JS
// file plus a same-run `comp`/`featTarget`/`retyped` index a `.dl` fact set
// cannot express (SPA.3's mintFeat injects its result back into the SAME
// `comp` index the route-switch loop reads, and RT.1's retypeTarget must
// run exactly once per node however many routes reach it). Stays hand-
// written Go; rules/javascript/js_client_routes.dl is pure pass-through.
//
// `patch:` (with FX.8.10's new `node:` Type-override field) does RT.1's
// variable->component retype-in-place. `mint:` builds the route nodes and
// SPA.3's external feature-registry component nodes — both brand new, so a
// full rebuild is safe (nothing pre-existing to preserve), unlike the
// retype case. `resolved:` (FX.8.10's other new primitive) reports which
// (service, key) pairs SPA.3 resolved, riding on the same relation as the
// feature-registry mint since one row already carries both columns — the
// caller retracts matching `jsx_component_unresolved` ledger rows, the
// same "resolved retraction" shape the retired Go's own indexer glue
// already did ad hoc for two OTHER passes (js_link/js_globals).
func init() { RegisterHub("js_client_routes_sites", jsClientRoutesSitesHub) }

const (
	jsCRRouteMintPred    = "js_cr_route_mint"     // (ID, Label, Svc, File, Path, Hash, Pattern, HashFragment)
	jsCRRenderEdgePred   = "js_cr_render_edge"     // (From, To, Spa)
	jsCRNavigateEdgePred = "js_cr_navigate_edge"   // (From, To)
	jsCRRetypePred       = "js_cr_retype"          // (TargetID)
	jsCRNotComponentPred = "js_cr_not_component"   // (Svc, File, Line, Name)
	jsCRFeatMintPred     = "js_cr_feat_mint"       // (ID, Label, Svc, Key)
	jsCRDynamicPred      = "js_cr_dynamic"         // (Svc, File, Line)
)

// jcrHandler / jcrEntry / jcrRender: ported from js_client_routes.go's
// crHandler / crEntry / fcRender.
type jcrHandler struct{ id, method, svc string }
type jcrEntry struct{ name, pattern, rel string }
type jcrRender struct {
	tag, fn, rel string
	line         int
	inRouteCase  bool
}

func jsClientRoutesSitesHub(nodes []graph.Node, files []string, _ string, _ []graph.LinkHint, _ graph.SchemaConfig) []Fact {
	svcOfFile := make(map[string]string)
	comp := make(map[string]string)
	compRank := make(map[string]int)
	fnBySvcLabel := make(map[string]string)
	handlersByPath := make(map[string][]jcrHandler)
	nodeByID := make(map[string]*graph.Node, len(nodes))
	consider := func(svc, label, id string, rank int) {
		if label == "" || svc == "" {
			return
		}
		key := svc + "\x00" + label
		if cur, ok := compRank[key]; !ok || rank > cur {
			comp[key] = id
			compRank[key] = rank
		}
	}
	for i := range nodes {
		n := &nodes[i]
		nodeByID[n.ID] = n
		if n.Service != "" && n.File != "" {
			svcOfFile[n.File] = n.Service
		}
		switch n.Type {
		case graph.NodeTypeComponent:
			consider(n.Service, n.Label, n.ID, 3)
		case graph.NodeTypeVariable:
			if n.Meta["component"] == "true" {
				consider(n.Service, n.Label, n.ID, 2)
			}
		case graph.NodeTypeClass:
			if jhcStartsUpperASCII(n.Label) {
				consider(n.Service, n.Label, n.ID, 2)
			}
		case graph.NodeTypeFunction:
			if jhcStartsUpperASCII(n.Label) {
				consider(n.Service, n.Label, n.ID, 1)
			}
			if k := n.Service + "\x00" + n.Label; fnBySvcLabel[k] == "" {
				fnBySvcLabel[k] = n.ID
			}
		case graph.NodeTypeMethod:
			if k := n.Service + "\x00" + n.Label; fnBySvcLabel[k] == "" {
				fnBySvcLabel[k] = n.ID
			}
		case graph.NodeTypeHTTPHandler:
			p := n.Meta["path"]
			if p == "" {
				break
			}
			np := jcrNormRoutePath(p)
			if np == "/" || np == "" {
				break
			}
			handlersByPath[np] = append(handlersByPath[np], jcrHandler{
				id: n.ID, method: strings.ToUpper(n.Meta["method"]), svc: n.Service,
			})
		}
	}

	// defaultSvc: svcOfFile only resolves a file that owns at least one
	// function/class/variable/method/file node — a pure route-table file
	// (`export default {...}`, no declarations at all) or a pure
	// registry file has none, so it would otherwise fall through to "".
	// The caller (internal/indexer/link_passes.go) runs this hub once per
	// service (the ruby_job_inherit/pusher_producer convention), so every
	// node in `nodes` already shares one service — falling back to it
	// mirrors the retired Go's own primary source (`serviceFiles`'s outer
	// map key WAS the service, svcOfFile was only its own fallback).
	defaultSvc := ""
	if len(nodes) > 0 {
		defaultSvc = nodes[0].Service
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

	routesBySvc := make(map[string][]jcrEntry)
	seenRoute := make(map[string]bool)
	caseBySvc := make(map[string]map[string][]string)

	featKeysBySvc := make(map[string]map[string]bool)
	featVarKeyBySvc := make(map[string]map[string]string)
	regBySvc := make(map[string]map[string]string)
	rendersBySvc := make(map[string][]jcrRender)
	addFeatKey := func(svc, k string) {
		m := featKeysBySvc[svc]
		if m == nil {
			m = make(map[string]bool)
			featKeysBySvc[svc] = m
		}
		m[k] = true
	}

	varIsComponent := make(map[string]map[string]bool)

	var out []Fact

	seen := make(map[string]bool)
	for _, abs := range jsFiles {
		rel := jhcRelativize(abs)
		if seen[rel] {
			continue
		}
		seen[rel] = true
		svc := svcOfFile[rel]
		if svc == "" {
			svc = defaultSvc
		}
		src, root, ok := jhcParseJS(abs)
		if !ok || root == nil {
			continue
		}

		scan := jcrScanFile(root, src, rel)

		for _, e := range scan.routeEntries {
			key := svc + "\x00" + rel + "\x00" + e.name
			if seenRoute[key] {
				continue
			}
			seenRoute[key] = true
			routesBySvc[svc] = append(routesBySvc[svc], jcrEntry{e.name, e.pattern, rel})
		}

		if len(scan.componentVarDecls) > 0 {
			varIsComponent[rel] = scan.componentVarDecls
		}

		for name, tags := range scan.routeCaseComps {
			m := caseBySvc[svc]
			if m == nil {
				m = make(map[string][]string)
				caseBySvc[svc] = m
			}
			m[name] = append(m[name], tags...)
		}

		if !jcrIsTestFile(rel) && (len(scan.keys) > 0 || len(scan.registry) > 0 || len(scan.dynamic) > 0) {
			for k := range scan.keys {
				addFeatKey(svc, k)
			}
			for v, k := range scan.varToKey {
				m := featVarKeyBySvc[svc]
				if m == nil {
					m = make(map[string]string)
					featVarKeyBySvc[svc] = m
				}
				m[v] = k
			}
			for k, lbl := range scan.registry {
				m := regBySvc[svc]
				if m == nil {
					m = make(map[string]string)
					regBySvc[svc] = m
				}
				m[k] = lbl
			}
			rendersBySvc[svc] = append(rendersBySvc[svc], scan.renders...)
			for _, ln := range scan.dynamic {
				out = append(out, Fact{
					Pred:   jsCRDynamicPred,
					Args:   []Atom{Str(svc), Str(rel), Int(int64(ln))},
					Origin: Origin{Kind: OriginPrimitive, File: rel, Line: ln, Pattern: jsCRDynamicPred},
				})
			}
		}
	}

	// --- SPA.3: resolve string-keyed feature components. ---
	featTarget := make(map[string]map[string]string)
	mintFeat := func(svc, key string) string {
		if m := featTarget[svc]; m != nil {
			if id := m[key]; id != "" {
				return id
			}
		}
		id, label := "", key
		if lbl := regBySvc[svc][key]; lbl != "" {
			if rid := comp[svc+"\x00"+lbl]; rid != "" {
				id = rid
			} else {
				label = lbl
			}
		}
		if id == "" {
			if rid := comp[svc+"\x00"+key]; rid != "" {
				id = rid
			}
		}
		if id == "" {
			id = svc + ":(feature_registry):component:" + key
		}
		if featTarget[svc] == nil {
			featTarget[svc] = make(map[string]string)
		}
		featTarget[svc][key] = id
		consider(svc, key, id, 2)
		if label != key {
			consider(svc, label, id, 2)
		}
		out = append(out, Fact{
			Pred:   jsCRFeatMintPred,
			Args:   []Atom{Str(id), Str(label), Str(svc), Str(key)},
			Origin: Origin{Kind: OriginPrimitive, Pattern: jsCRFeatMintPred},
		})
		return id
	}
	for _, svc := range jcrSortedKeys(featKeysBySvc) {
		keys := featKeysBySvc[svc]
		ks := make([]string, 0, len(keys))
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			mintFeat(svc, k)
		}
	}

	seenEdge := make(map[string]bool)
	addRenderEdge := func(from, to, spa string) {
		if from == "" || to == "" || from == to {
			return
		}
		eid := "renders:" + from + "->" + to
		if seenEdge[eid] {
			return
		}
		seenEdge[eid] = true
		out = append(out, Fact{
			Pred:   jsCRRenderEdgePred,
			Args:   []Atom{Str(from), Str(to), Str(spa)},
			Origin: Origin{Kind: OriginPrimitive, Pattern: jsCRRenderEdgePred},
		})
	}

	retyped := make(map[string]bool)
	retypeTarget := func(id string) {
		if retyped[id] {
			return
		}
		retyped[id] = true
		n := nodeByID[id]
		if n == nil || n.Type != graph.NodeTypeVariable {
			return
		}
		if !varIsComponent[n.File][n.Label] {
			out = append(out, Fact{
				Pred: jsCRNotComponentPred,
				Args: []Atom{Str(n.Service), Str(n.File), Int(int64(n.Line)), Str(n.Label)},
				Origin: Origin{Kind: OriginPrimitive, File: n.File, Line: n.Line, Pattern: jsCRNotComponentPred},
			})
			return
		}
		out = append(out, Fact{
			Pred:   jsCRRetypePred,
			Args:   []Atom{Str(id)},
			Origin: Origin{Kind: OriginPrimitive, Pattern: jsCRRetypePred},
		})
	}

	for _, svc := range jcrSortedKeys(routesBySvc) {
		for _, e := range routesBySvc[svc] {
			path, hash, frag := jcrNormClientRoute(e.pattern)
			routeID := svc + ":" + e.rel + ":client_route:" + e.name
			out = append(out, Fact{
				Pred: jsCRRouteMintPred,
				Args: []Atom{
					Str(routeID), Str(e.name), Str(svc), Str(e.rel),
					Str(path), Str(jcrBoolStr(hash)), Str(e.pattern), Str(frag),
				},
				Origin: Origin{Kind: OriginPrimitive, File: e.rel, Pattern: jsCRRouteMintPred},
			})

			for _, tag := range jcrDedupStrings(caseBySvc[svc][e.name]) {
				cid := comp[svc+"\x00"+tag]
				if cid == "" || cid == routeID {
					continue
				}
				retypeTarget(cid)
				addRenderEdge(routeID, cid, "route_component")
			}

			hs := handlersByPath[path]
			if path == "/" || path == "" || len(hs) > 6 {
				continue
			}
			for _, h := range hs {
				if h.method != "" && h.method != "GET" {
					continue
				}
				if h.svc != "" && svc != "" && h.svc != svc {
					continue
				}
				eid := "navigates_to:" + h.id + "->" + routeID
				if seenEdge[eid] {
					continue
				}
				seenEdge[eid] = true
				out = append(out, Fact{
					Pred:   jsCRNavigateEdgePred,
					Args:   []Atom{Str(h.id), Str(routeID)},
					Origin: Origin{Kind: OriginPrimitive, Pattern: jsCRNavigateEdgePred},
				})
			}
		}
	}

	for _, svc := range jcrSortedKeys(rendersBySvc) {
		for _, r := range rendersBySvc[svc] {
			if r.inRouteCase {
				continue
			}
			key := r.tag
			if k := featVarKeyBySvc[svc][r.tag]; k != "" {
				key = k
			}
			if !featKeysBySvc[svc][key] {
				continue
			}
			fnID := fnBySvcLabel[svc+"\x00"+r.fn]
			if fnID == "" {
				continue
			}
			target := mintFeat(svc, key)
			if target == "" || target == fnID {
				continue
			}
			addRenderEdge(fnID, target, "feature_component")
		}
	}

	return out
}

// jcrSortedKeys returns a map's string keys sorted, for deterministic
// iteration order over the per-service accumulators above.
func jcrSortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- ported structural helpers (jcr-prefixed, from js_client_routes.go) ---

func jcrIsTestFile(rel string) bool {
	for _, s := range []string{"__tests__/", "__mocks__/", "/spec/", ".test.", ".spec.", "-test.", "-spec.", "/test-support/"} {
		if strings.Contains(rel, s) {
			return true
		}
	}
	return false
}

func jcrIsComponentHOC(name string) bool {
	return name == "connect" || jhcHOCNames[name]
}

func jcrDeclaresComponent(v *sitter.Node, src []byte) bool {
	for i := 0; v != nil && i < 8; i++ {
		switch v.Type() {
		case "arrow_function", "function_expression", "function",
			"class", "class_expression", "generator_function":
			return true
		case "parenthesized_expression":
			v = v.NamedChild(0)
		case "call_expression":
			return jcrIsComponentHOC(jhcOutermostCallee(v, src))
		default:
			return false
		}
	}
	return false
}

func jcrLooksLikeRegistryName(s string) bool {
	switch s {
	case "registry", "REGISTRY", "featureComponents", "componentMap", "components",
		"componentRegistry", "registeredComponents", "reactExports":
		return true
	}
	return strings.HasSuffix(s, "Registry") || strings.HasSuffix(s, "Components")
}

// jcrFileScan holds everything a single per-file tree-sitter walk can
// produce for js_client_routes: exported route-table entries, which
// variable_declarators declare a component, switch(routeName)-case component
// tags, and SPA.3's feature-registry scan (keys / var-to-key bindings /
// registry entries / JSX renders / dynamic-key sites). XM.20: these were 4
// independent full-tree walks (jcrExportedObjectLiterals,
// jcrComponentVarDecls, jcrRouteCaseComponents, jcrScanFeatureComponents,
// the last of which also called jcrExportedObjectLiterals a second time
// internally) — none reads state another writes, so one walk with a single
// top-level switch produces all of them.
type jcrFileScan struct {
	routeEntries      []jcrPair
	componentVarDecls map[string]bool
	routeCaseComps    map[string][]string
	keys              map[string]bool
	varToKey          map[string]string
	registry          map[string]string
	renders           []jcrRender
	dynamic           []int
}

func jcrScanFile(root *sitter.Node, src []byte, rel string) *jcrFileScan {
	res := &jcrFileScan{
		componentVarDecls: make(map[string]bool),
		routeCaseComps:    make(map[string][]string),
		keys:              make(map[string]bool),
		varToKey:          make(map[string]string),
		registry:          make(map[string]string),
	}

	scanExportedObject := func(o *sitter.Node) {
		res.routeEntries = append(res.routeEntries, jcrRouteTableEntries(o, src)...)
		for k, v := range jcrComponentRegistryEntries(o, src) {
			res.registry[k] = v
		}
	}

	var walk func(n *sitter.Node, fn string, inCase bool)
	walk = func(n *sitter.Node, fn string, inCase bool) {
		switch n.Type() {
		case "export_statement":
			if v := n.ChildByFieldName("value"); v != nil {
				if o := jcrUnwrapToObject(v); o != nil {
					scanExportedObject(o)
				}
			}
			if d := n.ChildByFieldName("declaration"); d != nil {
				for i := 0; i < int(d.NamedChildCount()); i++ {
					vd := d.NamedChild(i)
					if vd.Type() != "variable_declarator" {
						continue
					}
					if o := jcrUnwrapToObject(vd.ChildByFieldName("value")); o != nil {
						scanExportedObject(o)
					}
				}
			}

		case "function_declaration":
			if nm := n.ChildByFieldName("name"); nm != nil {
				fn = nm.Content(src)
			}
		case "method_definition":
			if nm := n.ChildByFieldName("name"); nm != nil {
				fn = nm.Content(src)
			}
		case "variable_declarator":
			nmNode := n.ChildByFieldName("name")
			if nmNode == nil {
				break
			}
			v := n.ChildByFieldName("value")
			if nmNode.Type() == "identifier" {
				if label := nmNode.Content(src); label != "" {
					if _, seen := res.componentVarDecls[label]; !seen {
						res.componentVarDecls[label] = jcrDeclaresComponent(v, src)
					}
				}
			}
			if v != nil {
				switch v.Type() {
				case "arrow_function", "function_expression", "function":
					fn = nmNode.Content(src)
				}
			}

		case "switch_statement":
			if jcrIsRouteSwitchDiscriminant(n, src) {
				inCase = true
				// SPA.1: collect this route switch's case→JSX-tag map directly
				// (ported from the retired jcrRouteCaseComponents).
				body := n.ChildByFieldName("body")
				if body == nil {
					for i := 0; i < int(n.NamedChildCount()); i++ {
						if c := n.NamedChild(i); c.Type() == "switch_body" {
							body = c
							break
						}
					}
				}
				if body != nil {
					var pending []string
					for i := 0; i < int(body.NamedChildCount()); i++ {
						c := body.NamedChild(i)
						if c.Type() != "switch_case" {
							continue
						}
						lbl, ok := jcrStringLit(c.ChildByFieldName("value"), src)
						if !ok {
							continue
						}
						tags := jcrJSXTagsIn(c, src)
						if len(tags) == 0 {
							pending = append(pending, lbl)
							continue
						}
						for _, name := range append(pending, lbl) {
							res.routeCaseComps[name] = append(res.routeCaseComps[name], tags...)
						}
						pending = pending[:0]
					}
				}
			}

		case "call_expression":
			if callee := n.ChildByFieldName("function"); callee != nil &&
				callee.Type() == "identifier" && featureComponentAccessors[callee.Content(src)] {
				var first *sitter.Node
				if args := n.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() > 0 {
					first = args.NamedChild(0)
				}
				if k, ok := jcrStringLit(first, src); ok && k != "" {
					res.keys[k] = true
					for _, vn := range jcrAssignedNames(n, src) {
						res.varToKey[vn] = k
					}
				} else {
					res.dynamic = append(res.dynamic, int(n.StartPoint().Row)+1)
				}
			}
		case "subscript_expression":
			obj := n.ChildByFieldName("object")
			if obj != nil && obj.Type() == "identifier" && jcrLooksLikeRegistryName(obj.Content(src)) {
				if k, ok := jcrStringLit(n.ChildByFieldName("index"), src); ok && jhcStartsUpperASCII(k) {
					res.keys[k] = true
					for _, vn := range jcrAssignedNames(n, src) {
						res.varToKey[vn] = k
					}
				}
			}
		case "jsx_self_closing_element", "jsx_opening_element":
			if nm := n.ChildByFieldName("name"); nm != nil && nm.Type() == "identifier" {
				if tag := nm.Content(src); jhcStartsUpperASCII(tag) {
					res.renders = append(res.renders, jcrRender{
						tag: tag, fn: fn, rel: rel,
						line: int(n.StartPoint().Row) + 1, inRouteCase: inCase,
					})
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i), fn, inCase)
		}
	}
	walk(root, "(module)", false)
	return res
}

// featureComponentAccessors is js_client_routes.go's own package-level var
// (SPA.3) — internal/linker keeps it since crIsTestFile also lives there
// now (moved to js_prop_client.go); duplicated here rather than shared,
// since internal/factpipe cannot import internal/linker.
var featureComponentAccessors = map[string]bool{"getFeatureComponent": true}

func jcrComponentRegistryEntries(obj *sitter.Node, src []byte) map[string]string {
	if obj == nil || obj.Type() != "object" {
		return nil
	}
	out := make(map[string]string)
	for i := 0; i < int(obj.NamedChildCount()); i++ {
		c := obj.NamedChild(i)
		switch c.Type() {
		case "shorthand_property_identifier", "shorthand_property_identifier_pattern":
			nm := c.Content(src)
			if !jhcStartsUpperASCII(nm) {
				return nil
			}
			out[nm] = nm
		case "pair":
			k := jcrKeyName(c.ChildByFieldName("key"), src)
			v := c.ChildByFieldName("value")
			if k == "" || v == nil || v.Type() != "identifier" {
				return nil
			}
			vt := v.Content(src)
			if !jhcStartsUpperASCII(vt) {
				return nil
			}
			out[k] = vt
		default:
			return nil
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

func jcrAssignedNames(n *sitter.Node, src []byte) []string {
	var out []string
	for p := n.Parent(); p != nil; p = p.Parent() {
		switch p.Type() {
		case "parenthesized_expression":
		case "assignment_expression":
			if l := p.ChildByFieldName("left"); l != nil && l.Type() == "identifier" {
				out = append(out, l.Content(src))
			}
		case "variable_declarator":
			if nm := p.ChildByFieldName("name"); nm != nil && nm.Type() == "identifier" {
				out = append(out, nm.Content(src))
			}
			return out
		default:
			return out
		}
	}
	return out
}

func jcrUnwrapToObject(n *sitter.Node) *sitter.Node {
	for n != nil {
		switch n.Type() {
		case "object":
			return n
		case "parenthesized_expression":
			n = n.NamedChild(0)
		default:
			return nil
		}
	}
	return nil
}

type jcrPair struct{ name, pattern string }

func jcrRouteTableEntries(obj *sitter.Node, src []byte) []jcrPair {
	if obj == nil || obj.Type() != "object" {
		return nil
	}
	var pairs []jcrPair
	total, strVals := 0, 0
	hasColon := false
	for i := 0; i < int(obj.NamedChildCount()); i++ {
		p := obj.NamedChild(i)
		if p.Type() != "pair" {
			continue
		}
		total++
		k, v := p.ChildByFieldName("key"), p.ChildByFieldName("value")
		name := jcrKeyName(k, src)
		val, ok := jcrStringLit(v, src)
		if name == "" || !ok {
			continue
		}
		strVals++
		if strings.Contains(val, ":") {
			hasColon = true
		}
		if strings.HasPrefix(val, "/") || strings.HasPrefix(val, "#") {
			pairs = append(pairs, jcrPair{name, val})
		}
	}
	if len(pairs) < 3 || total == 0 || strVals != total || !hasColon {
		return nil
	}
	return pairs
}

func jcrKeyName(k *sitter.Node, src []byte) string {
	if k == nil {
		return ""
	}
	switch k.Type() {
	case "property_identifier", "identifier":
		return k.Content(src)
	case "string":
		s, _ := jcrStringLit(k, src)
		return s
	}
	return ""
}

func jcrStringLit(n *sitter.Node, src []byte) (string, bool) {
	if n == nil {
		return "", false
	}
	switch n.Type() {
	case "string":
		t := n.Content(src)
		if len(t) < 2 {
			return "", false
		}
		return t[1 : len(t)-1], true
	case "template_string":
		t := n.Content(src)
		if strings.Contains(t, "${") || len(t) < 2 {
			return "", false
		}
		return t[1 : len(t)-1], true
	}
	return "", false
}

func jcrIsRouteSwitchDiscriminant(sw *sitter.Node, src []byte) bool {
	v := sw.ChildByFieldName("value")
	if v != nil && v.Type() == "parenthesized_expression" && v.NamedChildCount() > 0 {
		v = v.NamedChild(0)
	}
	if v == nil {
		return false
	}
	t := strings.TrimSpace(strings.Trim(v.Content(src), "()"))
	return t == "routeName" || t == "activeTabKey"
}

func jcrJSXTagsIn(n *sitter.Node, src []byte) []string {
	var out []string
	var walk func(*sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "jsx_self_closing_element", "jsx_opening_element":
			if nm := n.ChildByFieldName("name"); nm != nil && nm.Type() == "identifier" {
				if tag := nm.Content(src); jhcStartsUpperASCII(tag) {
					out = append(out, tag)
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(n)
	return out
}

func jcrNormClientRoute(pattern string) (path string, hash bool, frag string) {
	raw := pattern
	if strings.HasPrefix(raw, "#") {
		return jcrNormRoutePath(strings.TrimPrefix(raw, "#")), true, ""
	}
	if i := strings.Index(raw, "#"); i >= 0 {
		hash = true
		frag = raw[i+1:]
		raw = raw[:i]
	}
	path = jcrNormRoutePath(raw)
	if j := strings.IndexAny(frag, "(?/"); j >= 0 {
		frag = frag[:j]
	}
	return path, hash, frag
}

func jcrNormRoutePath(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.IndexAny(p, "?#("); i >= 0 {
		p = p[:i]
	}
	segs := strings.Split(strings.Trim(p, "/"), "/")
	out := segs[:0]
	for _, s := range segs {
		if s == "" {
			continue
		}
		if s[0] == ':' || s[0] == '*' || strings.HasPrefix(s, "{") {
			out = append(out, "*")
		} else {
			out = append(out, s)
		}
	}
	return "/" + strings.Join(out, "/")
}

func jcrBoolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func jcrDedupStrings(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
