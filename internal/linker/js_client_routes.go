package linker

import (
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// featureComponentAccessors are the conventional "resolve a component by string
// key" lookup functions (SPA.3). A call `getFeatureComponent("PKPDTopLevel")`
// whose result is rendered maps that string key to a component node. Kept
// deliberately narrow — `getComponent` alone is far too generic (ECS, DOM, 3D
// engines all use it) and produced pure noise on real corpora.
var featureComponentAccessors = map[string]bool{
	"getFeatureComponent": true,
}

// crIsTestFile reports whether rel is a JS test/spec/mock file — feature-registry
// scanning skips these (a `getFeatureComponent("batCar")` in a unit test is not a
// real render site).
func crIsTestFile(rel string) bool {
	for _, s := range []string{"__tests__/", "__mocks__/", "/spec/", ".test.", ".spec.", "-test.", "-spec.", "/test-support/"} {
		if strings.Contains(rel, s) {
			return true
		}
	}
	return false
}

// LinkJSClientRoutes (SPA.2) models a single-page-app's client-side router as
// graph nodes and edges:
//
//   - A module-level route table — `export default { cdm: "/standards/:id#cdm/:m", … }`
//     (an object literal whose values are all URL-pattern strings) — mints one
//     `client_route` node per entry, labelled by the route name, with
//     Meta["path"] holding the normalised *pre-hash* path (`:seg` → `*`) and
//     Meta["hash"] recording whether the pattern carried a `#fragment`.
//   - A `switch (routeName)` / `switch (activeTabKey)` elsewhere in the service
//     maps each `case "<name>":` to the JSX page component rendered in that
//     block. Emits `client_route --renders--> <componentNode>`.
//   - Where a `client_route`'s normalised path equals an `http_handler`'s
//     normalised path, emits `http_handler --navigates_to--> client_route` so
//     "open `GET /standards/:id` → which SPA page" is answerable.
//
// No name allow-list for the route table or the switch discriminant beyond the
// two conventional discriminant identifiers; the table is recognised purely by
// shape. Runs after js_link so the render-target component nodes it resolves
// against are already stamped (SPA.1 synthetics included).
//
// SPA.3 rides along: a `getFeatureComponent("K")` / `<registry>["K"]` site whose
// result is rendered resolves the string key K to a component — a real node when
// K (or the registry's mapped identifier) matches one, else a synthetic
// `component` node marked Meta["external"]="true" (the impl lives in a plugin
// repo outside the corpus). The render edge is `client_route --renders-->` when K
// feeds a route-switch case, else `enclosingFn --renders-->`. Non-literal keys
// are ledgered `feature_component_dynamic`. `resolved` reports every (service,
// key) SPA.3 resolved so the caller can retract the matching
// `jsx_component_unresolved` ledger rows.
//
// RT.1 rides along: a route's render target that resolved to a `variable` node
// declaring a component — `const Foo = () => …`, `const Foo = connect(…)(Bar)`
// — is re-typed to `component` and returned in `tagged`. The edge already
// pointed at the right file and line; only the type was wrong, and it is
// load-bearing, because "which component does route `cdm` render" is asked as a
// type filter and returned nothing. Targets whose initialiser is not a
// component (an object, a string) keep their type and ledger as
// `client_route_target_not_component`.
//
// `tagged` holds re-typed *existing* nodes and `newNodes` genuinely new ones;
// the caller must replace the former in place by ID rather than appending. A
// second node with the same label is a second render target, and the render
// matcher mints fan-out from it.
//
// Deliberately narrow: only nodes already reached by a `client_route
// --renders-->` edge. React components typed `function` rather than `component`
// across the wider JSX graph are a labelling inconsistency with no query riding
// on it (see docs/cedar-monolith-gap-audit-plan.md), and re-typing those is a
// different and much larger decision.
func LinkJSClientRoutes(nodes []graph.Node, serviceFiles map[string][]string) (newNodes, tagged []graph.Node, edges []graph.Edge, ledger []graph.UnresolvedRef, resolved map[string]bool) {
	resolved = make(map[string]bool)
	// --- index existing nodes ---
	svcOfFile := make(map[string]string)
	comp := make(map[string]string) // service\x00label → component node id
	compRank := make(map[string]int)
	fnBySvcLabel := make(map[string]string)        // service\x00label → function/method node id
	handlersByPath := make(map[string][]crHandler) // normalised path → handlers
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
	nodeByID := make(map[string]*graph.Node, len(nodes))
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
			if startsUpperASCII(n.Label) {
				consider(n.Service, n.Label, n.ID, 2)
			}
		case graph.NodeTypeFunction:
			if startsUpperASCII(n.Label) {
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
			np := normRoutePath(p)
			if np == "/" || np == "" {
				break
			}
			handlersByPath[np] = append(handlersByPath[np], crHandler{
				id:     n.ID,
				method: strings.ToUpper(n.Meta["method"]),
				svc:    n.Service,
			})
		}
	}

	type crEntry struct {
		name, pattern, rel string
	}
	routesBySvc := make(map[string][]crEntry)
	seenRoute := make(map[string]bool)
	caseBySvc := make(map[string]map[string][]string) // svc → routeName → component tags

	// SPA.3 accumulators.
	featKeysBySvc := make(map[string]map[string]bool)     // svc → set of literal registry keys
	featVarKeyBySvc := make(map[string]map[string]string) // svc → local var name → registry key
	regBySvc := make(map[string]map[string]string)        // svc → registry key → component label
	rendersBySvc := make(map[string][]fcRender)           // svc → JSX render sites
	addFeatKey := func(svc, k string) {
		m := featKeysBySvc[svc]
		if m == nil {
			m = make(map[string]bool)
			featKeysBySvc[svc] = m
		}
		m[k] = true
	}

	// RT.1: file → variable name → whether its initialiser declares a component.
	varIsComponent := make(map[string]map[string]bool)

	seen := make(map[string]bool)
	for svcKey, files := range serviceFiles {
		for _, abs := range files {
			if !isJSFile(abs) {
				continue
			}
			src, root, _, ok := jsParse(abs)
			if !ok {
				continue
			}
			rel := patterns.RelativizeToCwd(abs)
			if seen[rel] {
				continue
			}
			seen[rel] = true
			svc := svcKey
			if svc == "" {
				svc = svcOfFile[rel]
			}

			for _, obj := range exportedObjectLiterals(root) {
				for _, e := range routeTableEntries(obj, src) {
					key := svc + "\x00" + rel + "\x00" + e.name
					if seenRoute[key] {
						continue
					}
					seenRoute[key] = true
					routesBySvc[svc] = append(routesBySvc[svc], crEntry{e.name, e.pattern, rel})
				}
			}

			if decls := componentVarDecls(root, src); len(decls) > 0 {
				varIsComponent[rel] = decls
			}

			for name, tags := range routeCaseComponents(root, src) {
				m := caseBySvc[svc]
				if m == nil {
					m = make(map[string][]string)
					caseBySvc[svc] = m
				}
				m[name] = append(m[name], tags...)
			}

			if sc := scanFeatureComponents(root, src, rel); sc != nil && !crIsTestFile(rel) {
				for k := range sc.keys {
					addFeatKey(svc, k)
				}
				for v, k := range sc.varToKey {
					m := featVarKeyBySvc[svc]
					if m == nil {
						m = make(map[string]string)
						featVarKeyBySvc[svc] = m
					}
					m[v] = k
				}
				for k, lbl := range sc.registry {
					m := regBySvc[svc]
					if m == nil {
						m = make(map[string]string)
						regBySvc[svc] = m
					}
					m[k] = lbl
				}
				rendersBySvc[svc] = append(rendersBySvc[svc], sc.renders...)
				for _, ln := range sc.dynamic {
					ledger = append(ledger, graph.UnresolvedRef{
						Service: svc, File: rel, Line: ln,
						Name: "(dynamic)", Kind: "feature_component_dynamic",
					})
				}
			}
		}
	}

	// --- SPA.3: resolve string-keyed feature components, minting an external
	// component node when the key names an impl outside the corpus. Injecting the
	// resolved id into `comp` lets the SPA.2 render loop below emit the
	// `client_route --renders-->` edge for a route-switch case unchanged. ---
	featTarget := make(map[string]map[string]string) // svc → key → node id
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
			newNodes = append(newNodes, graph.Node{
				ID:       id,
				Type:     graph.NodeTypeComponent,
				Label:    label,
				Service:  svc,
				File:     "(feature_registry)",
				Language: "javascript",
				Meta:     map[string]string{"spa": "feature_component", "external": "true", "key": key},
			})
		}
		if featTarget[svc] == nil {
			featTarget[svc] = make(map[string]string)
		}
		featTarget[svc][key] = id
		consider(svc, key, id, 2)
		if label != key {
			consider(svc, label, id, 2)
		}
		resolved[svc+"\x00"+key] = true
		return id
	}
	for svc, keys := range featKeysBySvc {
		for k := range keys {
			mintFeat(svc, k)
		}
	}

	seenEdge := make(map[string]bool)
	addEdge := func(e graph.Edge) {
		if seenEdge[e.ID] {
			return
		}
		seenEdge[e.ID] = true
		edges = append(edges, e)
	}

	// RT.1: re-type a route's render target, once per node however many routes
	// reach it. Ledger rows are keyed the same way — a target that is not a
	// component is one fact about one node, not one per route.
	retyped := make(map[string]bool)
	var rtLedger []graph.UnresolvedRef
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
			rtLedger = append(rtLedger, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: n.Label, Kind: "client_route_target_not_component",
			})
			return
		}
		out := *n
		out.Type = graph.NodeTypeComponent
		out.Meta = make(map[string]string, len(n.Meta)+2)
		for k, v := range n.Meta {
			out.Meta[k] = v
		}
		out.Meta["component"] = "true"
		out.Meta["retyped_from"] = string(graph.NodeTypeVariable)
		tagged = append(tagged, out)
	}

	for svc, entries := range routesBySvc {
		for _, e := range entries {
			path, hash, frag := normClientRoute(e.pattern)
			routeID := svc + ":" + e.rel + ":client_route:" + e.name
			meta := map[string]string{
				"path":    path,
				"hash":    boolStr(hash),
				"pattern": e.pattern,
			}
			if frag != "" {
				meta["hash_fragment"] = frag
			}
			newNodes = append(newNodes, graph.Node{
				ID:       routeID,
				Type:     graph.NodeTypeClientRoute,
				Label:    e.name,
				Service:  svc,
				File:     e.rel,
				Language: "javascript",
				Meta:     meta,
			})

			for _, tag := range dedupStrings(caseBySvc[svc][e.name]) {
				cid := comp[svc+"\x00"+tag]
				if cid == "" || cid == routeID {
					continue
				}
				retypeTarget(cid)
				addEdge(graph.Edge{
					ID:         "renders:" + routeID + "->" + cid,
					From:       routeID,
					To:         cid,
					Type:       graph.EdgeTypeRenders,
					Confidence: graph.ConfidenceInferred,
					Meta:       map[string]string{"spa": "route_component"},
				})
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
				addEdge(graph.Edge{
					ID:         "navigates_to:" + h.id + "->" + routeID,
					From:       h.id,
					To:         routeID,
					Type:       graph.EdgeTypeNavigatesTo,
					Confidence: graph.ConfidenceInferred,
					Meta:       map[string]string{"spa": "client_route", "via": "path_shape"},
				})
			}
		}
	}

	// --- SPA.3: `enclosingFn --renders--> featureComponent` for render sites that
	// are *not* inside a route-switch case (those are covered by the SPA.2 loop
	// above via the injected `comp` entry). ---
	for svc, rs := range rendersBySvc {
		for _, r := range rs {
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
			addEdge(graph.Edge{
				ID:         "renders:" + fnID + "->" + target,
				From:       fnID,
				To:         target,
				Type:       graph.EdgeTypeRenders,
				Confidence: graph.ConfidenceInferred,
				Meta:       map[string]string{"spa": "feature_component"},
			})
		}
	}

	// Route order comes from a map walk, so RT.1's outputs are sorted before
	// they leave: a re-typed node upserts idempotently either way, but a ledger
	// whose row order moves between two cold indexes is a determinism failure
	// the snapshot tests would report as a real diff.
	sort.Slice(tagged, func(i, j int) bool { return tagged[i].ID < tagged[j].ID })
	sort.Slice(rtLedger, func(i, j int) bool {
		if rtLedger[i].File != rtLedger[j].File {
			return rtLedger[i].File < rtLedger[j].File
		}
		return rtLedger[i].Name < rtLedger[j].Name
	})
	ledger = append(ledger, rtLedger...)

	return newNodes, tagged, edges, ledger, resolved
}

// isComponentHOC reports whether a call wrapper returns a React component. The
// list is JCM.6's `hocNames` (js_hoc.go) plus `connect`, which that tier handles
// through its curry-unwrapping path rather than by name. Recognising the wrapper
// is what separates `const Foo = connect(mapState)(Bar)` — a component
// declaration in every sense except the node type the parser gave it — from
// `const config = loadConfig()`. Treating any call as a component would re-type
// whatever a route happens to name, which is the failure the gate exists for.
func isComponentHOC(name string) bool {
	return name == "connect" || hocNames[name]
}

// componentVarDecls maps each variable name declared in the file to whether its
// initialiser declares a component: a function, an arrow, a class, or an HOC
// call. First declaration wins, so a later shadow in an inner scope cannot
// change the verdict for the exported binding.
func componentVarDecls(root *sitter.Node, src []byte) map[string]bool {
	out := make(map[string]bool)
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "variable_declarator" {
			nm := n.ChildByFieldName("name")
			if nm != nil && nm.Type() == "identifier" {
				if label := nm.Content(src); label != "" {
					if _, seen := out[label]; !seen {
						out[label] = declaresComponent(n.ChildByFieldName("value"), src)
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return out
}

// declaresComponent reports whether an initialiser expression produces a React
// component.
func declaresComponent(v *sitter.Node, src []byte) bool {
	for i := 0; v != nil && i < 8; i++ {
		switch v.Type() {
		case "arrow_function", "function_expression", "function",
			"class", "class_expression", "generator_function":
			return true
		case "parenthesized_expression":
			v = v.NamedChild(0)
		case "call_expression":
			// `connect(mapState)(Bar)` and `React.memo(X)` both resolve through
			// JCM.6's callee walk, which peels the curry and reads the member
			// property.
			return isComponentHOC(hocOutermostCallee(v, src))
		default:
			return false
		}
	}
	return false
}

type crHandler struct{ id, method, svc string }

// fcRender is one JSX render site seen while scanning for SPA.3 feature
// components: the tag name, its enclosing function label ("(module)" at top
// level), and whether it sits inside a route-switch case.
type fcRender struct {
	tag, fn, rel string
	line         int
	inRouteCase  bool
}

// looksLikeRegistryName recognises the conventional identifiers a string-keyed
// component registry is bound to.
func looksLikeRegistryName(s string) bool {
	switch s {
	case "registry", "REGISTRY", "featureComponents", "componentMap", "components",
		"componentRegistry", "registeredComponents", "reactExports":
		return true
	}
	return strings.HasSuffix(s, "Registry") || strings.HasSuffix(s, "Components")
}

type fcScanResult struct {
	keys     map[string]bool
	varToKey map[string]string
	registry map[string]string
	renders  []fcRender
	dynamic  []int
}

// scanFeatureComponents walks a JS file for SPA.3 signals: string-keyed
// component-registry accessor calls (`getFeatureComponent("K")`), registry
// object literals (`export default { Foo, Bar }` — every value an uppercase
// identifier), the local vars those calls bind to, and every JSX render site.
// Returns nil when the file has nothing of interest.
func scanFeatureComponents(root *sitter.Node, src []byte, rel string) *fcScanResult {
	res := &fcScanResult{
		keys:     make(map[string]bool),
		varToKey: make(map[string]string),
		registry: make(map[string]string),
	}
	for _, obj := range exportedObjectLiterals(root) {
		for k, v := range componentRegistryEntries(obj, src) {
			res.registry[k] = v
		}
	}

	var walk func(n *sitter.Node, fn string, inCase bool)
	walk = func(n *sitter.Node, fn string, inCase bool) {
		switch n.Type() {
		case "function_declaration":
			if nm := n.ChildByFieldName("name"); nm != nil {
				fn = nm.Content(src)
			}
		case "method_definition":
			if nm := n.ChildByFieldName("name"); nm != nil {
				fn = nm.Content(src)
			}
		case "variable_declarator":
			if v := n.ChildByFieldName("value"); v != nil {
				switch v.Type() {
				case "arrow_function", "function_expression", "function":
					if nm := n.ChildByFieldName("name"); nm != nil {
						fn = nm.Content(src)
					}
				}
			}
		case "switch_statement":
			if isRouteSwitchDiscriminant(n, src) {
				inCase = true
			}
		case "call_expression":
			if callee := n.ChildByFieldName("function"); callee != nil &&
				callee.Type() == "identifier" && featureComponentAccessors[callee.Content(src)] {
				var first *sitter.Node
				if args := n.ChildByFieldName("arguments"); args != nil && args.NamedChildCount() > 0 {
					first = args.NamedChild(0)
				}
				if k, ok := crStringLit(first, src); ok && k != "" {
					res.keys[k] = true
					for _, vn := range assignedNames(n, src) {
						res.varToKey[vn] = k
					}
				} else {
					res.dynamic = append(res.dynamic, int(n.StartPoint().Row)+1)
				}
			}
		case "subscript_expression":
			// `<registry>["K"]` — gated on the object identifier looking like a
			// registry (bare `obj["K"]` is far too common to treat as a lookup).
			obj := n.ChildByFieldName("object")
			if obj != nil && obj.Type() == "identifier" && looksLikeRegistryName(obj.Content(src)) {
				if k, ok := crStringLit(n.ChildByFieldName("index"), src); ok && startsUpperASCII(k) {
					res.keys[k] = true
					for _, vn := range assignedNames(n, src) {
						res.varToKey[vn] = k
					}
				}
			}
		case "jsx_self_closing_element", "jsx_opening_element":
			if nm := n.ChildByFieldName("name"); nm != nil && nm.Type() == "identifier" {
				if tag := nm.Content(src); startsUpperASCII(tag) {
					res.renders = append(res.renders, fcRender{
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

	if len(res.keys) == 0 && len(res.registry) == 0 && len(res.dynamic) == 0 {
		return nil
	}
	return res
}

// componentRegistryEntries reads an object literal that is a component registry —
// at least two entries, every value a PascalCase identifier (shorthand or
// explicit), no spreads — returning key → component label. Returns nil for
// anything else (a route table's string values, a config object, a spread).
func componentRegistryEntries(obj *sitter.Node, src []byte) map[string]string {
	if obj == nil || obj.Type() != "object" {
		return nil
	}
	out := make(map[string]string)
	for i := 0; i < int(obj.NamedChildCount()); i++ {
		c := obj.NamedChild(i)
		switch c.Type() {
		case "shorthand_property_identifier", "shorthand_property_identifier_pattern":
			nm := c.Content(src)
			if !startsUpperASCII(nm) {
				return nil
			}
			out[nm] = nm
		case "pair":
			k := crKeyName(c.ChildByFieldName("key"), src)
			v := c.ChildByFieldName("value")
			if k == "" || v == nil || v.Type() != "identifier" {
				return nil
			}
			vt := v.Content(src)
			if !startsUpperASCII(vt) {
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

// assignedNames returns the identifier names a call/subscript expression is bound
// to, walking out through parenthesised and nested-assignment wrappers:
// `var X = (Y = getFeatureComponent("K"))` yields ["Y", "X"].
func assignedNames(n *sitter.Node, src []byte) []string {
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

// exportedObjectLiterals returns object literals that are the value of an
// `export default …` or an exported `const … = { … }`.
func exportedObjectLiterals(root *sitter.Node) []*sitter.Node {
	var out []*sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "export_statement" {
			if v := n.ChildByFieldName("value"); v != nil {
				if o := unwrapToObject(v); o != nil {
					out = append(out, o)
				}
			}
			if d := n.ChildByFieldName("declaration"); d != nil {
				for i := 0; i < int(d.NamedChildCount()); i++ {
					vd := d.NamedChild(i)
					if vd.Type() != "variable_declarator" {
						continue
					}
					if o := unwrapToObject(vd.ChildByFieldName("value")); o != nil {
						out = append(out, o)
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return out
}

func unwrapToObject(n *sitter.Node) *sitter.Node {
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

type crPair struct{ name, pattern string }

// routeTableEntries validates that obj is a client-route table — at least three
// pairs, every value a string literal, at least one value carrying a `:param` —
// and returns the entries whose value looks like a URL pattern.
func routeTableEntries(obj *sitter.Node, src []byte) []crPair {
	if obj == nil || obj.Type() != "object" {
		return nil
	}
	var pairs []crPair
	total, strVals := 0, 0
	hasColon := false
	for i := 0; i < int(obj.NamedChildCount()); i++ {
		p := obj.NamedChild(i)
		if p.Type() != "pair" {
			continue
		}
		total++
		k, v := p.ChildByFieldName("key"), p.ChildByFieldName("value")
		name := crKeyName(k, src)
		val, ok := crStringLit(v, src)
		if name == "" || !ok {
			continue
		}
		strVals++
		if strings.Contains(val, ":") {
			hasColon = true
		}
		if strings.HasPrefix(val, "/") || strings.HasPrefix(val, "#") {
			pairs = append(pairs, crPair{name, val})
		}
	}
	if len(pairs) < 3 || total == 0 || strVals != total || !hasColon {
		return nil
	}
	return pairs
}

func crKeyName(k *sitter.Node, src []byte) string {
	if k == nil {
		return ""
	}
	switch k.Type() {
	case "property_identifier", "identifier":
		return k.Content(src)
	case "string":
		s, _ := crStringLit(k, src)
		return s
	}
	return ""
}

func crStringLit(n *sitter.Node, src []byte) (string, bool) {
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

// routeCaseComponents maps `case "<name>":` labels to the PascalCase JSX tags
// rendered in that block, for every `switch (routeName)` / `switch (activeTabKey)`
// in the file. Fall-through cases (a `case` with no JSX before the next) inherit
// the following block's components.
func routeCaseComponents(root *sitter.Node, src []byte) map[string][]string {
	res := make(map[string][]string)
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "switch_statement" && isRouteSwitchDiscriminant(n, src) {
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
					lbl, ok := crStringLit(c.ChildByFieldName("value"), src)
					if !ok {
						continue
					}
					tags := jsxTagsIn(c, src)
					if len(tags) == 0 {
						pending = append(pending, lbl)
						continue
					}
					for _, nm := range append(pending, lbl) {
						res[nm] = append(res[nm], tags...)
					}
					pending = pending[:0]
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return res
}

func isRouteSwitchDiscriminant(sw *sitter.Node, src []byte) bool {
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

func jsxTagsIn(n *sitter.Node, src []byte) []string {
	var out []string
	var walk func(*sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "jsx_self_closing_element", "jsx_opening_element":
			if nm := n.ChildByFieldName("name"); nm != nil && nm.Type() == "identifier" {
				if tag := nm.Content(src); startsUpperASCII(tag) {
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

// normClientRoute splits a url-pattern route into its normalised pre-hash path
// (`:seg` → `*`, optional `(…)` groups and query dropped), a hash flag, and the
// raw hash fragment name (up to its first optional group).
func normClientRoute(pattern string) (path string, hash bool, frag string) {
	raw := pattern
	if strings.HasPrefix(raw, "#") {
		// Leading-hash (hashbang) router: the fragment *is* the route path.
		return normRoutePath(strings.TrimPrefix(raw, "#")), true, ""
	}
	if i := strings.Index(raw, "#"); i >= 0 {
		hash = true
		frag = raw[i+1:]
		raw = raw[:i]
	}
	path = normRoutePath(raw)
	if j := strings.IndexAny(frag, "(?/"); j >= 0 {
		frag = frag[:j]
	}
	return path, hash, frag
}

func normRoutePath(p string) string {
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

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func dedupStrings(in []string) []string {
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
