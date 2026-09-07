package linker

import (
	"fmt"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

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
// Non-literal URLs (`getDataURL(type)`) are ledgered `prop_client_dynamic_url`
// — a visible blind spot for SPA.5, never a silent drop.

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
func LinkJSPropClients(nodes []graph.Node, serviceFiles map[string][]string) (newNodes []graph.Node, edges []graph.Edge, ledger []graph.UnresolvedRef) {
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

	// --- pass 1: discover prop-client specs per service ---
	specsBySvc := make(map[string]map[string]propClientSpec)
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
		var walk func(n *sitter.Node, fn string)
		walk = func(n *sitter.Node, fn string) {
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
				if spec, method, urlNode, siteVerb, line, ok := propClientCallSite(n, pf.src, specs, fileText, propsNames); ok {
					_ = spec
					cands, dyn := walkerKey(walker, urlNode, pf.src)
					if dyn || len(cands) == 0 {
						ledger = append(ledger, graph.UnresolvedRef{
							Service: pf.svc, File: pf.rel, Line: line,
							Name: "(dynamic)", Kind: "prop_client_dynamic_url",
						})
						break
					}
					path := cands[0]
					if !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "*") {
						ledger = append(ledger, graph.UnresolvedRef{
							Service: pf.svc, File: pf.rel, Line: line,
							Name: path, Kind: "prop_client_dynamic_url",
						})
						break
					}
					verb := method.Verb
					if siteVerb != "" {
						verb = siteVerb
					}
					if verb == "" {
						verb = "GET"
					}
					id := fmt.Sprintf("%s:%s:http_client:prop_client:%d", pf.svc, pf.rel, line)
					if !mintSeen[id] {
						mintSeen[id] = true
						meta := map[string]string{
							"pattern": "prop_client",
							"method":  verb,
							"url":     path,
							"spa":     "prop_client",
						}
						if len(cands) > 1 {
							meta["key_candidates"] = contract.MarshalKeyCandidates(cands)
						}
						newNodes = append(newNodes, graph.Node{
							ID:       id,
							Type:     graph.NodeTypeHTTPClient,
							Label:    verb + " " + path,
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
				walk(n.NamedChild(i), fn)
			}
		}
		walk(pf.root, "(module)")
	}
	return newNodes, edges, ledger
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
	callee := call.ChildByFieldName("function")
	if callee == nil || callee.Type() != "member_expression" {
		return miss()
	}
	propNode := callee.ChildByFieldName("property")
	obj := callee.ChildByFieldName("object")
	if propNode == nil || obj == nil {
		return miss()
	}
	methodName := propNode.Content(src)
	prop, ok := propClientReceiverProp(obj, src)
	if !ok {
		return miss()
	}
	spec, ok := specs[prop]
	if !ok {
		return miss()
	}
	// bare-identifier receiver must be corroborated: either the file imports
	// the HOC, or the name provably originates from `this.props` (a destructure
	// or a `this.props.<prop>` access somewhere in the file). `ajaxStatus` is
	// far too generic to fire on a bare `x.get(a, b)` with no such evidence.
	if obj.Type() == "identifier" {
		if !propsNames[prop] && (spec.HOCExport == "" || !strings.Contains(fileText, spec.HOCExport)) {
			return miss()
		}
	}
	method, ok := spec.Methods[methodName]
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
