package linker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// crIsTestFile reports whether rel is a JS test/spec/mock file. Moved here
// from the now-deleted js_client_routes.go (Tier FX FX.8.10, 2026-09-15) —
// this file and schema_url_link.go/schema_url_table.go/
// valuegraph_adapter.go are its remaining callers.
func crIsTestFile(rel string) bool {
	for _, s := range []string{"__tests__/", "__mocks__/", "/spec/", ".test.", ".spec.", "-test.", "-spec.", "/test-support/"} {
		if strings.Contains(rel, s) {
			return true
		}
	}
	return false
}

// SPA.4 — prop-injected HTTP-client wrapper.
//
// The dominant client-fetch pattern in a big Flow/React SPA is
// `this.props.ajaxStatus.get(msg, urlString)` — where `ajaxStatus` is a prop
// injected by a higher-order component whose method bodies ultimately reach
// `window.$.ajax(...)`. No producer pattern matches: the callee is a member
// access on `this.props.<name>` (not `$`/`axios`/`fetch`), and the URL literal
// is the *second* positional arg, or the `url` key of a second-arg options
// object.
//
// LinkJSPropClients detects the transport-forwarding HOC per service
// (detectPropClients), then resolves every call site of one of its methods on
// the injected prop to an `http_client` node — Meta["url"] holds the KeyWalked
// URL (template holes → `*`), Meta["method"] the verb. The contract engine's
// http.yaml rule then joins that node to a `config/routes.rb` handler exactly
// as it does for a jQuery `$.ajax` client. A `calls` edge from the enclosing
// function is emitted so a trace from the React component reaches the call.
//
// SPA.5 — dynamic URL-builder functions. When the URL argument is a call to a
// local/imported function (`getDataURL(type)`) whose body is a set of literal /
// template / switch returns, dynamicURLBuilder synthesises the distinct path
// shapes (`${p}` → `*`, ≤6) and mints one http_client per shape. An opaque
// builder is ledgered `dynamic_url_builder` (naming the function); a >6-way
// fan-out is ledgered `dynamic_url_fanout` and mints nothing. Anything else
// stays `prop_client_dynamic_url` — a visible blind spot, never a silent drop.

// propClientMethod records, for one method of a prop-client, where its URL
// argument sits: a positional string arg (URLArgIndex, URLOptKey == "") or the
// `url` key of an options object at position URLArgIndex (URLOptKey != "").
type propClientMethod struct {
	Verb        string
	URLArgIndex int
	URLOptKey   string
}

// propClientSpec describes one transport-forwarding HOC: its exported name, the
// prop it injects into the wrapped component, and its URL-carrying methods.
type propClientSpec struct {
	HOCExport    string
	InjectedProp string
	Methods      map[string]propClientMethod
}

// propClientMethodNames are the method names a prop-client may expose. Kept as a
// gate so a wrapper with an unrelated `compute`/`select` method never widens
// into an HTTP client.
var propClientMethodNames = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true, "delete": true,
	"del": true, "ajax": true, "request": true, "fetch": true, "head": true,
}

func propClientVerb(name string) string {
	switch name {
	case "get", "post", "put", "patch", "delete", "head":
		return strings.ToUpper(name)
	case "del":
		return "DELETE"
	}
	return ""
}

// LinkJSPropClients is the SPA.4 pass. Returns synthetic http_client nodes, the
// `calls` edges wiring them to their enclosing functions, and the
// prop_client_dynamic_url ledger.
func LinkJSPropClients(nodes []graph.Node, serviceFiles map[string][]string, sr *SchemaURLResolver) (newNodes []graph.Node, edges []graph.Edge, ledger []graph.UnresolvedRef) {
	// index function/method nodes for the enclosing-fn `calls` edge.
	fnBySvcLabel := make(map[string]string)
	svcOfFile := make(map[string]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Service != "" && n.File != "" {
			svcOfFile[n.File] = n.Service
		}
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeMethod:
			if k := n.Service + "\x00" + n.Label; fnBySvcLabel[k] == "" {
				fnBySvcLabel[k] = n.ID
			}
		}
	}

	walker := contract.KeyWalkerFor("javascript")

	// --- pass 1: discover prop-client specs + URL-builder functions per service ---
	specsBySvc := make(map[string]map[string]propClientSpec)
	fnDefsBySvc := make(map[string]map[string]fnDef)
	type parsedFile struct {
		rel  string
		svc  string
		src  []byte
		root *sitter.Node
	}
	var files []parsedFile
	seen := make(map[string]bool)
	for svcKey, fs := range serviceFiles {
		for _, abs := range fs {
			if !isJSFile(abs) {
				continue
			}
			rel := patterns.RelativizeToCwd(abs)
			if seen[rel] || crIsTestFile(rel) {
				continue
			}
			seen[rel] = true
			src, root, _, ok := jsParse(abs)
			if !ok {
				continue
			}
			svc := svcKey
			if svc == "" {
				svc = svcOfFile[rel]
			}
			files = append(files, parsedFile{rel: rel, svc: svc, src: src, root: root})
			indexFnDefs(root, src, func(name string, fn *sitter.Node) {
				m := fnDefsBySvc[svc]
				if m == nil {
					m = make(map[string]fnDef)
					fnDefsBySvc[svc] = m
				}
				if _, exists := m[name]; !exists {
					m[name] = fnDef{node: fn, src: src}
				}
			})
			if spec, ok := detectPropClientSpec(root, src, rel); ok {
				m := specsBySvc[svc]
				if m == nil {
					m = make(map[string]propClientSpec)
					specsBySvc[svc] = m
				}
				m[spec.InjectedProp] = spec
			}
		}
	}
	if len(specsBySvc) == 0 {
		return nil, nil, nil
	}

	// --- pass 2: resolve call sites ---
	mintSeen := make(map[string]bool)
	for _, pf := range files {
		specs := specsBySvc[pf.svc]
		if len(specs) == 0 {
			continue
		}
		fileText := string(pf.src)
		propsNames := collectPropsBindings(pf.root, pf.src)
		var walk func(n *sitter.Node, fn string, names map[string]bool)
		walk = func(n *sitter.Node, fn string, names map[string]bool) {
			// CW: a module-level helper that receives the transport as an
			// argument has no `this.props` anywhere in its file, so
			// collectPropsBindings vouches for nothing and every call in these
			// 23 files was dropped. Entering a function widens the vouched set
			// for that subtree only — unioned rather than replaced, since a
			// method can take a transport parameter *and* read this.props.
			extra := paramPropClientNames(n, pf.src, specs)
			for k := range paramDerivedPropClientNames(n, pf.src, specs) {
				if extra == nil {
					extra = make(map[string]bool, 1)
				}
				extra[k] = true
			}
			if len(extra) > 0 {
				merged := make(map[string]bool, len(names)+len(extra))
				for k := range names {
					merged[k] = true
				}
				for k := range extra {
					merged[k] = true
				}
				names = merged
			}
			switch n.Type() {
			case "function_declaration", "method_definition":
				if nm := n.ChildByFieldName("name"); nm != nil {
					fn = nm.Content(pf.src)
				}
			case "variable_declarator", "public_field_definition", "field_definition":
				if v := n.ChildByFieldName("value"); v != nil {
					switch v.Type() {
					case "arrow_function", "function_expression", "function":
						if nm := n.ChildByFieldName("name"); nm != nil {
							fn = nm.Content(pf.src)
						}
					}
				}
			case "call_expression":
				if _, method, urlNode, siteVerb, line, ok := propClientCallSite(n, pf.src, specs, fileText, names); !ok {
					// Recognised receiver + method, unreadable argument list:
					// the URL argument is absent, or it is an options object
					// with no `url` key. Ledger it. Closing CW's gap without
					// this row would only move the silence from "no node" to
					// "no node and still nothing said".
					if _, _, line, recognised := propClientRecognisedSite(n, pf.src, specs, fileText, names); recognised {
						ledger = append(ledger, graph.UnresolvedRef{
							Service: pf.svc, File: pf.rel, Line: line,
							Name: "(no url argument)", Kind: "prop_client_dynamic_url",
						})
					}
				} else {
					verb := method.Verb
					if siteVerb != "" {
						verb = siteVerb
					}
					verbKnown := verb != ""
					if verb == "" {
						verb = "GET"
					}

					cands, dyn := walkerKey(walker, urlNode, pf.src)

					// each mint request is one distinct path shape to emit.
					type mintReq struct {
						path         string
						cands        []string
						localBinding bool
						schemaMeta   map[string]string
					}
					var reqs []mintReq

					if !dyn && len(cands) > 0 &&
						(strings.HasPrefix(cands[0], "/") || strings.HasPrefix(cands[0], "*")) {
						reqs = append(reqs, mintReq{path: cands[0], cands: cands})
					} else if shapes, fname, kind := dynamicURLBuilder(urlNode, pf.src, fnDefsBySvc[pf.svc], walker); kind == "shapes" {
						for _, sh := range shapes {
							reqs = append(reqs, mintReq{path: sh, cands: []string{sh}})
						}
					} else if paths, reason, ok := resolveLocalURLBinding(urlNode, enclosingJSFunction(n), pf.src); ok {
						// Tier UL: the argument is a bare local (often the
						// `{ url }` shorthand of an options object) bound to one
						// or more literal paths in this function. One node per
						// distinct path — a switch selecting between four
						// endpoints is four flows, not one client with four
						// edges.
						for _, p := range paths {
							reqs = append(reqs, mintReq{path: p, cands: []string{p}, localBinding: true})
						}
					} else if hit, hok, hkind := sr.ResolveURLExpr(urlNode, enclosingJSFunction(n), pf.src, pf.svc); hok || hkind != "" {
						// Tier MS.1/MS.2: the URL argument reads a discovered data
						// asset. The asset supplies the path; the call site
						// supplies the verb — never guess one from the key name.
						if hok && verbKnown {
							reqs = append(reqs, mintReq{path: hit.Path, cands: []string{hit.Path}, schemaMeta: SchemaMintMeta(hit)})
						} else {
							kk := hkind
							if kk == "" {
								kk = "schema_entity_unresolved"
							}
							ledger = append(ledger, graph.UnresolvedRef{
								Service: pf.svc, File: pf.rel, Line: line,
								Name: "(schema)", Kind: kk,
							})
						}
					} else {
						name := "(dynamic)"
						k := "prop_client_dynamic_url"
						switch {
						case fname != "":
							name, k = fname, kind
						case len(cands) > 0:
							name = cands[0]
						case reason == ledgerLocalURLHighFanout:
							// A local carrying more than maxLocalURLBranches
							// URLs is a table or a loop, and saying so is more
							// useful than the generic dynamic row.
							k = reason
						}
						ledger = append(ledger, graph.UnresolvedRef{
							Service: pf.svc, File: pf.rel, Line: line,
							Name: name, Kind: k,
						})
					}

					for ri, req := range reqs {
						id := fmt.Sprintf("%s:%s:http_client:prop_client:%d", pf.svc, pf.rel, line)
						if len(reqs) > 1 {
							id = fmt.Sprintf("%s:%d", id, ri)
						}
						if mintSeen[id] {
							continue
						}
						mintSeen[id] = true
						meta := map[string]string{
							"pattern": "prop_client",
							"method":  verb,
							"url":     req.path,
							"spa":     "prop_client",
						}
						if len(req.cands) > 1 {
							meta["key_candidates"] = contract.MarshalKeyCandidates(req.cands)
						}
						if req.localBinding {
							meta["url_origin"] = localURLOriginLocalBinding
							meta["branch_index"] = strconv.Itoa(ri)
						}
						for k, v := range req.schemaMeta {
							meta[k] = v
						}
						newNodes = append(newNodes, graph.Node{
							ID:       id,
							Type:     graph.NodeTypeHTTPClient,
							Label:    verb + " " + req.path,
							Service:  pf.svc,
							File:     pf.rel,
							Line:     line,
							Language: "javascript",
							Meta:     meta,
						})
						if fnID := fnBySvcLabel[pf.svc+"\x00"+fn]; fnID != "" && fnID != id {
							edges = append(edges, graph.Edge{
								ID:         "calls:" + fnID + "->" + id,
								From:       fnID,
								To:         id,
								Type:       graph.EdgeTypeCalls,
								Confidence: graph.ConfidenceInferred,
								Meta:       map[string]string{"spa": "prop_client"},
							})
						}
					}
				}
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				walk(n.NamedChild(i), fn, names)
			}
		}
		walk(pf.root, "(module)", propsNames)
	}
	return newNodes, edges, ledger
}

// ── SPA.5: dynamic URL-builder functions ────────────────────────────────────

// fnDef is a parsed function/arrow definition kept for URL-builder resolution.
type fnDef struct {
	node *sitter.Node
	src  []byte
}

// indexFnDefs emits every named function definition in a file: `function f(){}`,
// `const f = () => {}`, and class-field arrows.
func indexFnDefs(root *sitter.Node, src []byte, emit func(name string, fn *sitter.Node)) {
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "function_declaration", "generator_function_declaration":
			if nm := n.ChildByFieldName("name"); nm != nil {
				emit(nm.Content(src), n)
			}
		case "variable_declarator", "public_field_definition", "field_definition":
			if v := n.ChildByFieldName("value"); v != nil {
				switch v.Type() {
				case "arrow_function", "function_expression", "function":
					if nm := n.ChildByFieldName("name"); nm != nil {
						emit(nm.Content(src), v)
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
}

// dynamicURLBuilder handles the SPA.5 case: the URL argument is `builder(args)`.
// Returns kind "shapes" with the distinct static path shapes the builder can
// return; "dynamic_url_fanout" (naming the fn) when there are >6; and
// "dynamic_url_builder" (naming the fn) when the builder is opaque or unknown.
// kind "" means the argument is not a plain function call at all.
func dynamicURLBuilder(urlNode *sitter.Node, src []byte, defs map[string]fnDef, w contract.KeyWalker) (shapes []string, fnName, kind string) {
	if urlNode == nil || urlNode.Type() != "call_expression" {
		return nil, "", ""
	}
	callee := urlNode.ChildByFieldName("function")
	if callee == nil || callee.Type() != "identifier" {
		return nil, "", ""
	}
	name := callee.Content(src)
	def, ok := defs[name]
	if !ok {
		return nil, name, "dynamic_url_builder"
	}
	sh, ok := synthURLBuilderShapes(w, def.node, def.src)
	if !ok || len(sh) == 0 {
		return nil, name, "dynamic_url_builder"
	}
	if len(sh) > 6 {
		return nil, name, "dynamic_url_fanout"
	}
	return sh, name, "shapes"
}

// synthURLBuilderShapes resolves a URL-builder function body to the set of
// distinct static path shapes it returns (template holes → `*`). ok=false when
// any returned expression is non-static or not root-relative.
func synthURLBuilderShapes(w contract.KeyWalker, fn *sitter.Node, src []byte) ([]string, bool) {
	body := fn.ChildByFieldName("body")
	if body == nil {
		return nil, false
	}
	var rets []*sitter.Node
	if body.Type() != "statement_block" {
		rets = append(rets, body) // arrow with expression body
	} else {
		var walk func(n *sitter.Node)
		walk = func(n *sitter.Node) {
			switch n.Type() {
			case "function_declaration", "function_expression", "arrow_function", "function":
				return // don't descend into nested functions
			case "return_statement":
				if n.NamedChildCount() > 0 {
					rets = append(rets, n.NamedChild(0))
				}
			}
			for i := 0; i < int(n.NamedChildCount()); i++ {
				walk(n.NamedChild(i))
			}
		}
		for i := 0; i < int(body.NamedChildCount()); i++ {
			walk(body.NamedChild(i))
		}
	}
	if len(rets) == 0 {
		return nil, false
	}
	set := make(map[string]bool)
	for _, r := range rets {
		cands, dyn := walkerKey(w, r, src)
		if dyn || len(cands) == 0 {
			return nil, false
		}
		for _, c := range cands {
			if !strings.HasPrefix(c, "/") && !strings.HasPrefix(c, "*") {
				return nil, false
			}
			set[c] = true
		}
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, true
}

// walkerKey runs the JS KeyWalker over a URL argument node, returning literal
// candidates (template holes already reduced to `*`) or dynamic=true.
func walkerKey(w contract.KeyWalker, node *sitter.Node, src []byte) ([]string, bool) {
	if w == nil || node == nil {
		return nil, true
	}
	cands, dyn := w.WalkKey(node, src, func(string) (string, bool) { return "", false })
	return cands, dyn
}

// propClientRecognisedSite reports whether call is `<recv>.<method>(...)` where
// recv names a discovered prop-client and method is one of that spec's
// URL-carrying methods — independently of whether the URL argument can be read.
//
// It exists as its own function because the two questions have different
// consumers. "Is this a prop-client call" is what decides whether a site owes
// the graph an entry at all; "can its URL be read" only decides whether that
// entry is a node or a ledger line. Folding them together is what made Tier CW's
// gap silent: a recognised call whose URL argument was absent or unreadable
// returned the same false as an unrelated `foo.get(x)`, so it produced no node,
// no ledger entry, and nothing anywhere in the graph saying a flow had been
// dropped. LinkJSPropClients calls this on the miss path for exactly that reason.
func propClientRecognisedSite(call *sitter.Node, src []byte, specs map[string]propClientSpec, fileText string, propsNames map[string]bool) (propClientSpec, propClientMethod, int, bool) {
	var zero propClientSpec
	callee := call.ChildByFieldName("function")
	if callee == nil || callee.Type() != "member_expression" {
		return zero, propClientMethod{}, 0, false
	}
	propNode := callee.ChildByFieldName("property")
	obj := callee.ChildByFieldName("object")
	if propNode == nil || obj == nil {
		return zero, propClientMethod{}, 0, false
	}
	prop, ok := propClientReceiverProp(obj, src)
	if !ok {
		return zero, propClientMethod{}, 0, false
	}
	spec, ok := specs[prop]
	if !ok {
		return zero, propClientMethod{}, 0, false
	}
	// bare-identifier receiver must be corroborated: either the file imports
	// the HOC, or the name is one propsNames vouches for — provably from
	// `this.props`, or (CW) a formal parameter of an enclosing function whose
	// name matches a discovered spec. `ajaxStatus` is far too generic to fire on
	// a bare `x.get(a, b)` with no such evidence.
	if obj.Type() == "identifier" {
		if !propsNames[prop] && (spec.HOCExport == "" || !strings.Contains(fileText, spec.HOCExport)) {
			return zero, propClientMethod{}, 0, false
		}
	}
	method, ok := spec.Methods[propNode.Content(src)]
	if !ok {
		return zero, propClientMethod{}, 0, false
	}
	return spec, method, int(call.StartPoint().Row) + 1, true
}

// paramPropClientNames returns the formal parameters of fn whose names match a
// known service-wide prop-client spec, so a module-level helper receiving the
// transport as an argument is treated the same as `this.props.<name>`.
//
// Deliberately narrow: matching a discovered spec name (not any identifier) is
// what keeps `get`/`post` from firing on unrelated objects in a type-free pass.
// The specs map is keyed by injected-prop name and is built from an actual
// HOC/transport definition found in the service (detectPropClientSpec), so a
// parameter only qualifies if the service really does inject a transport under
// that name somewhere.
func paramPropClientNames(fn *sitter.Node, src []byte, specs map[string]propClientSpec) map[string]bool {
	if fn == nil || len(specs) == 0 {
		return nil
	}
	var out map[string]bool
	for nm := range paramBindingNames(fn, src) {
		if _, ok := specs[nm]; !ok {
			continue
		}
		if out == nil {
			out = make(map[string]bool, 1)
		}
		out[nm] = true
	}
	return out
}

// paramBindingNames returns every name fn's parameter list binds, including the
// names a destructured parameter introduces: `({ ajaxStatus, canEdit }) => …`
// binds both, and a function component spelled that way is the single commonest
// receiver shape in the audit corpus's remaining misses.
func paramBindingNames(fn *sitter.Node, src []byte) map[string]bool {
	out := make(map[string]bool)
	var bind func(p *sitter.Node)
	bind = func(p *sitter.Node) {
		if p == nil {
			return
		}
		switch p.Type() {
		case "identifier", "shorthand_property_identifier_pattern", "shorthand_property_identifier":
			out[p.Content(src)] = true
			return
		case "object_pattern", "array_pattern":
			for i := 0; i < int(p.NamedChildCount()); i++ {
				bind(p.NamedChild(i))
			}
			return
		case "pair_pattern":
			// `{ ajaxStatus: transport }` binds the *value* side.
			bind(p.ChildByFieldName("value"))
			return
		}
		// required_parameter / optional_parameter / assignment_pattern /
		// rest_pattern: the binding hangs off `pattern`, or is the first
		// non-type child.
		if pat := p.ChildByFieldName("pattern"); pat != nil {
			bind(pat)
			return
		}
		if l := p.ChildByFieldName("left"); l != nil {
			bind(l)
			return
		}
		for i := 0; i < int(p.NamedChildCount()); i++ {
			switch c := p.NamedChild(i); c.Type() {
			case "identifier", "object_pattern", "array_pattern":
				bind(c)
				return
			}
		}
	}
	if params := fn.ChildByFieldName("parameters"); params != nil {
		for i := 0; i < int(params.NamedChildCount()); i++ {
			bind(params.NamedChild(i))
		}
		return out
	}
	// `ajaxStatus => ...`: an arrow with a single unparenthesised parameter has
	// no `parameters` node at all, only `parameter`.
	bind(fn.ChildByFieldName("parameter"))
	return out
}

// paramDerivedPropClientNames returns spec-matching names that fn's body unpacks
// out of one of fn's own parameters: `const { ajaxStatus } = props` in a function
// component, and `const { ajaxStatus } = component.props` where the component is
// handed in. Both are the same fact as `this.props.<name>` — the transport came
// from the caller — written without a `this`, which is exactly why
// collectPropsBindings, keyed on the literal text "this.props", saw nothing.
//
// Anchored on fn's parameter list rather than on the conventional name `props`:
// the guarantee that makes this safe is that the object being unpacked provably
// came from outside the function, and only the parameter list can say so. A
// `const { ajaxStatus } = getProps()` is deliberately not covered — its origin is
// a call return, and nothing here can see through it.
func paramDerivedPropClientNames(fn *sitter.Node, src []byte, specs map[string]propClientSpec) map[string]bool {
	if fn == nil || len(specs) == 0 {
		return nil
	}
	params := paramBindingNames(fn, src)
	if len(params) == 0 {
		return nil
	}
	body := fn.ChildByFieldName("body")
	if body == nil {
		return nil
	}
	var out map[string]bool
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "variable_declarator" {
			name := n.ChildByFieldName("name")
			val := n.ChildByFieldName("value")
			if name != nil && name.Type() == "object_pattern" && val != nil && fromParam(val, src, params) {
				for i := 0; i < int(name.NamedChildCount()); i++ {
					c := name.NamedChild(i)
					var nm string
					switch c.Type() {
					case "shorthand_property_identifier_pattern", "shorthand_property_identifier":
						nm = c.Content(src)
					case "pair_pattern":
						if k := c.ChildByFieldName("key"); k != nil {
							nm = k.Content(src)
						}
					}
					if nm == "" {
						continue
					}
					if _, ok := specs[nm]; !ok {
						continue
					}
					if out == nil {
						out = make(map[string]bool, 1)
					}
					out[nm] = true
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(body)
	return out
}

// fromParam reports whether val is one of params, or `<param>.props`.
func fromParam(val *sitter.Node, src []byte, params map[string]bool) bool {
	switch val.Type() {
	case "identifier":
		return params[val.Content(src)]
	case "member_expression":
		obj := val.ChildByFieldName("object")
		prop := val.ChildByFieldName("property")
		return obj != nil && prop != nil && obj.Type() == "identifier" &&
			prop.Content(src) == "props" && params[obj.Content(src)]
	}
	return false
}

// propClientCallSite reports whether call is `<recv>.<method>(...)` where recv
// is a prop-client receiver (`this.props.<prop>` or a bare `<prop>` in a file
// that imports the HOC) and method is one of the spec's URL methods. Returns
// the matched spec, the method descriptor, the URL argument node, and the call
// line.
func propClientCallSite(call *sitter.Node, src []byte, specs map[string]propClientSpec, fileText string, propsNames map[string]bool) (propClientSpec, propClientMethod, *sitter.Node, string, int, bool) {
	var zero propClientSpec
	miss := func() (propClientSpec, propClientMethod, *sitter.Node, string, int, bool) {
		return zero, propClientMethod{}, nil, "", 0, false
	}
	spec, method, _, ok := propClientRecognisedSite(call, src, specs, fileText, propsNames)
	if !ok {
		return miss()
	}
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return miss()
	}
	var argNodes []*sitter.Node
	for i := 0; i < int(args.NamedChildCount()); i++ {
		argNodes = append(argNodes, args.NamedChild(i))
	}
	if method.URLArgIndex >= len(argNodes) {
		return miss()
	}
	urlNode := argNodes[method.URLArgIndex]
	var siteVerb string
	if method.URLOptKey != "" {
		optsObj := urlNode
		// The options object is often built into a local — `const opts = { url:
		// …, method: "PUT" }; transport.ajax(msg, opts)`. Backtrack a bare
		// identifier to its object literal so the url/method keys are readable.
		if optsObj.Type() != "object" {
			if nm := localURLIdentName(optsObj, src); nm != "" {
				if efn := enclosingJSFunction(call); efn != nil {
					for _, rhs := range localURLAssignments(efn, optsObj.StartByte(), src, nm) {
						if rhs.Type() == "object" {
							optsObj = rhs
						}
					}
				}
			}
		}
		urlNode = jsObjectKeyValue(optsObj, src, method.URLOptKey)
		if urlNode == nil {
			return miss()
		}
		for _, k := range []string{"type", "method"} {
			if vn := jsObjectKeyValue(optsObj, src, k); vn != nil && vn.Type() == "string" {
				siteVerb = strings.ToUpper(strings.Trim(vn.Content(src), `"'`))
			}
		}
	}
	line := int(call.StartPoint().Row) + 1
	return spec, method, urlNode, siteVerb, line, true
}

// propClientReceiverProp returns the injected-prop name a receiver expression
// stands for: `this.props.<prop>` → "<prop>"; a bare identifier → its own name
// (corroborated by the caller). Anything else → ok=false.
func propClientReceiverProp(obj *sitter.Node, src []byte) (string, bool) {
	switch obj.Type() {
	case "identifier":
		return obj.Content(src), true
	case "member_expression":
		prop := obj.ChildByFieldName("property")
		inner := obj.ChildByFieldName("object")
		if prop == nil || inner == nil || inner.Type() != "member_expression" {
			return "", false
		}
		innerProp := inner.ChildByFieldName("property")
		innerObj := inner.ChildByFieldName("object")
		if innerProp == nil || innerObj == nil {
			return "", false
		}
		if innerProp.Content(src) == "props" && innerObj.Type() == "this" {
			return prop.Content(src), true
		}
	}
	return "", false
}

// collectPropsBindings returns every name that provably originates from
// `this.props` in a file: destructured (`const { a, b } = this.props`) or
// accessed (`this.props.a`).
func collectPropsBindings(root *sitter.Node, src []byte) map[string]bool {
	out := make(map[string]bool)
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "variable_declarator":
			name := n.ChildByFieldName("name")
			val := n.ChildByFieldName("value")
			if name != nil && name.Type() == "object_pattern" && val != nil &&
				val.Type() == "member_expression" &&
				strings.TrimSpace(val.Content(src)) == "this.props" {
				for i := 0; i < int(name.NamedChildCount()); i++ {
					c := name.NamedChild(i)
					switch c.Type() {
					case "shorthand_property_identifier_pattern", "shorthand_property_identifier":
						out[c.Content(src)] = true
					case "pair_pattern":
						if k := c.ChildByFieldName("key"); k != nil {
							out[k.Content(src)] = true
						}
					}
				}
			}
		case "member_expression":
			inner := n.ChildByFieldName("object")
			prop := n.ChildByFieldName("property")
			if inner != nil && prop != nil && inner.Type() == "member_expression" &&
				strings.TrimSpace(inner.Content(src)) == "this.props" {
				out[prop.Content(src)] = true
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return out
}

// jsObjectKeyValue returns the value node for key `key` in an object literal
// (`{ url: x }` or `{ url }` shorthand), or nil.
func jsObjectKeyValue(n *sitter.Node, src []byte, key string) *sitter.Node {
	if n == nil || n.Type() != "object" {
		return nil
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		switch c.Type() {
		case "pair":
			k := c.ChildByFieldName("key")
			if k != nil && strings.Trim(k.Content(src), `"'`) == key {
				return c.ChildByFieldName("value")
			}
		case "shorthand_property_identifier":
			if c.Content(src) == key {
				return c
			}
		}
	}
	return nil
}

// ── HOC detection ───────────────────────────────────────────────────────────

// detectPropClientSpec looks for a transport-forwarding HOC in one file: a
// function `Name(Wrapped)` whose body renders `<Wrapped <prop>={this} />` and
// declares a class with URL-carrying methods that reach a transport.
func detectPropClientSpec(root *sitter.Node, src []byte, rel string) (propClientSpec, bool) {
	var result propClientSpec
	found := false

	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found {
			return
		}
		switch n.Type() {
		case "function_declaration", "function_expression", "arrow_function":
			if spec, ok := propClientFromHOCBody(n, src); ok {
				spec.HOCExport = hocExportName(root, src, n)
				result = spec
				found = true
				return
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return result, found
}

// propClientFromHOCBody inspects a function that may be an HOC: its first
// parameter is the wrapped component; its body must render `<param prop={this}/>`
// and contain a class with URL methods reaching a transport.
func propClientFromHOCBody(fn *sitter.Node, src []byte) (propClientSpec, bool) {
	var zero propClientSpec
	params := fn.ChildByFieldName("parameters")
	body := fn.ChildByFieldName("body")
	if params == nil || body == nil || params.NamedChildCount() == 0 {
		return zero, false
	}
	wrapped := paramName(params.NamedChild(0), src)
	if wrapped == "" {
		return zero, false
	}
	injected := jsxSelfPropForElement(body, src, wrapped)
	if injected == "" {
		return zero, false
	}
	methods := scanClassTransportMethods(body, src)
	if len(methods) == 0 {
		return zero, false
	}
	return propClientSpec{InjectedProp: injected, Methods: methods}, true
}

// paramName returns the identifier name of a parameter node (handles
// `required_parameter`/`identifier`/typed forms).
func paramName(p *sitter.Node, src []byte) string {
	if p == nil {
		return ""
	}
	if p.Type() == "identifier" {
		return p.Content(src)
	}
	if pat := p.ChildByFieldName("pattern"); pat != nil && pat.Type() == "identifier" {
		return pat.Content(src)
	}
	for i := 0; i < int(p.NamedChildCount()); i++ {
		if c := p.NamedChild(i); c.Type() == "identifier" {
			return c.Content(src)
		}
	}
	return ""
}

// jsxSelfPropForElement scans body for a JSX element named `tag` and returns
// the name of the attribute whose value is `{this}` — the injected prop.
func jsxSelfPropForElement(body *sitter.Node, src []byte, tag string) string {
	var out string
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if out != "" {
			return
		}
		switch n.Type() {
		case "jsx_opening_element", "jsx_self_closing_element":
			name := n.ChildByFieldName("name")
			if name != nil && name.Content(src) == tag {
				for i := 0; i < int(n.NamedChildCount()); i++ {
					attr := n.NamedChild(i)
					if attr.Type() != "jsx_attribute" {
						continue
					}
					if attr.NamedChildCount() < 2 {
						continue
					}
					an := attr.NamedChild(0)
					av := attr.NamedChild(1)
					if av.Type() == "jsx_expression" && strings.TrimSpace(innerText(av, src)) == "this" {
						out = an.Content(src)
						return
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(body)
	return out
}

func innerText(n *sitter.Node, src []byte) string {
	if n.NamedChildCount() == 1 {
		return n.NamedChild(0).Content(src)
	}
	t := n.Content(src)
	t = strings.TrimPrefix(t, "{")
	t = strings.TrimSuffix(t, "}")
	return t
}

// scanClassTransportMethods finds the class inside body and returns its
// URL-carrying methods (name → descriptor) whose bodies reach a transport
// directly or via one sibling-method hop.
func scanClassTransportMethods(body *sitter.Node, src []byte) map[string]propClientMethod {
	var cls *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if cls != nil {
			return
		}
		switch n.Type() {
		case "class", "class_declaration":
			cls = n
			return
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(body)
	if cls == nil {
		return nil
	}
	var clsBody *sitter.Node
	for i := 0; i < int(cls.NamedChildCount()); i++ {
		if c := cls.NamedChild(i); c.Type() == "class_body" {
			clsBody = c
			break
		}
	}
	if clsBody == nil {
		return nil
	}

	type member struct {
		name    string
		fnNode  *sitter.Node
		bodyTxt string
	}
	var members []member
	for i := 0; i < int(clsBody.NamedChildCount()); i++ {
		c := clsBody.NamedChild(i)
		var name string
		var fnNode *sitter.Node
		switch c.Type() {
		case "method_definition":
			if nm := c.ChildByFieldName("name"); nm != nil {
				name = nm.Content(src)
			}
			fnNode = c
		case "public_field_definition", "field_definition":
			v := c.ChildByFieldName("value")
			if v == nil {
				continue
			}
			switch v.Type() {
			case "arrow_function", "function_expression", "function":
				if nm := c.ChildByFieldName("name"); nm != nil {
					name = nm.Content(src)
				}
				fnNode = v
			}
		}
		if name == "" || fnNode == nil {
			continue
		}
		members = append(members, member{name: name, fnNode: fnNode, bodyTxt: fnNode.Content(src)})
	}

	directReach := func(txt string) bool {
		for _, m := range []string{"$.ajax", "window.$", ".ajax(", "fetch(", "axios(", "XMLHttpRequest", "$.get(", "$.post("} {
			if strings.Contains(txt, m) {
				return true
			}
		}
		return false
	}
	transportMembers := make(map[string]bool)
	for _, m := range members {
		if directReach(m.bodyTxt) {
			transportMembers[m.name] = true
		}
	}

	out := make(map[string]propClientMethod)
	for _, m := range members {
		if !propClientMethodNames[m.name] {
			continue
		}
		reaches := transportMembers[m.name]
		if !reaches {
			for sib := range transportMembers {
				if strings.Contains(m.bodyTxt, "this."+sib+"(") {
					reaches = true
					break
				}
			}
		}
		if !reaches {
			continue
		}
		idx, optKey, ok := propClientURLArg(m.fnNode, src)
		if !ok {
			continue
		}
		out[m.name] = propClientMethod{
			Verb:        propClientVerb(m.name),
			URLArgIndex: idx,
			URLOptKey:   optKey,
		}
	}
	return out
}

// propClientURLArg decides which argument of a method carries the URL:
//   - a parameter literally named `url` → that positional index, string arg;
//   - otherwise the first non-message parameter that is handed to `$.ajax(...)`
//     / `fetch(...)` as its settings object, or read as `<param>.url` → that
//     index, options-key "url".
func propClientURLArg(fn *sitter.Node, src []byte) (int, string, bool) {
	params := fn.ChildByFieldName("parameters")
	if params == nil {
		return 0, "", false
	}
	var names []string
	for i := 0; i < int(params.NamedChildCount()); i++ {
		names = append(names, paramName(params.NamedChild(i), src))
	}
	for i, nm := range names {
		if nm == "url" {
			return i, "", true
		}
	}
	body := fn.ChildByFieldName("body")
	bodyTxt := ""
	if body != nil {
		bodyTxt = body.Content(src)
	}
	for i, nm := range names {
		if i == 0 || nm == "" {
			continue
		}
		if strings.Contains(bodyTxt, "$.ajax("+nm+")") ||
			strings.Contains(bodyTxt, "ajax("+nm+")") ||
			strings.Contains(bodyTxt, "fetch("+nm+")") ||
			strings.Contains(bodyTxt, nm+".url") {
			return i, "url", true
		}
	}
	return 0, "", false
}

// hocExportName resolves the exported name a HOC function is bound to:
// `const Name = function(...)` / `export default function Name(...)` /
// `export default Name`. Falls back to "" (bare-identifier receivers then never
// corroborate).
func hocExportName(root *sitter.Node, src []byte, fn *sitter.Node) string {
	for p := fn.Parent(); p != nil; p = p.Parent() {
		switch p.Type() {
		case "variable_declarator":
			if nm := p.ChildByFieldName("name"); nm != nil {
				return nm.Content(src)
			}
		case "function_declaration":
			if nm := p.ChildByFieldName("name"); nm != nil {
				return nm.Content(src)
			}
		}
	}
	if nm := fn.ChildByFieldName("name"); nm != nil {
		return nm.Content(src)
	}
	return ""
}
