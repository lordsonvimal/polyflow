package linker

import (
	"fmt"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// Tier UB.3 — the transport function crosses a JSX prop.
//
// The mirror image of UB.2. Here the parent owns the transport: a wrapper
// function whose URL is one of its parameters is handed to a child as a prop,
// and the child supplies the argument at its own call site.
//
//	// parent
//	postToServer = (transport, data, updateURL) =>
//	  transport.ajax("Saving...", { url: updateURL, data, method: "PUT" });
//	// ...
//	<Grid postToServer={this.postToServer} />
//
//	// child
//	this.props.postToServer(this.props.transport, data, `/api/things/${id}`);
//
// Tier CW recognises the parent's transport call, finds its URL argument is a
// bare parameter, and ledgers `prop_client_dynamic_url`. This pass supplies the
// missing half: it indexes every JSX attribute whose value references a local
// function, keyed by the referenced symbol, then for each ledger row whose URL
// argument is a parameter it (1) finds which prop carries that function on which
// component, (2) reads the argument at that parameter's index at the child's
// `props.<prop>(…)` call sites, and (3) mints one http_client per distinct
// resolved URL at the parent's transport call — where the request is issued and
// where the ledger row already sits.
//
// Same resolution rule as UB.2: one node per distinct URL, fan-out exactly 1,
// never one node with several http_call edges; above a cap it ledgers instead.
// Argument expressions that are not a literal/template path or a bare identifier
// resolvable in their own function ledger `prop_transport_unresolved`.
//
// Must run AFTER js_prop_urls (it consumes the same ledger, already thinned by
// UB.2) and after js_local_urls (it reuses the local-binding walk).

const (
	ledgerPropTransportUnresolved = "prop_transport_unresolved"
	ledgerPropTransportHighFanout = "prop_transport_high_fanout"
)

// propFnPass records one JSX attribute whose value handed a local function to a
// child component: <tag attr={this.symbol} /> or <tag attr={symbol} />.
type propFnPass struct {
	tag        string
	attr       string
	file       string
	line       int
	thisScoped bool // value was `this.symbol` — join only within the same file
}

// LinkJSPropTransport is the Tier UB.3 pass. It returns the synthetic
// http_client nodes, the calls edges wiring them to the wrapper function, the
// prop_transport_* ledger, and the set of prop_client_dynamic_url sites that
// resolved (keyed by PropURLRetractKey) so the caller can retract them.
func LinkJSPropTransport(nodes []graph.Node, ledger []graph.UnresolvedRef, serviceFiles map[string][]string) (newNodes []graph.Node, edges []graph.Edge, out []graph.UnresolvedRef, retract map[string]bool) {
	retract = map[string]bool{}

	var rows []graph.UnresolvedRef
	for _, u := range ledger {
		if u.Kind == "prop_client_dynamic_url" {
			rows = append(rows, u)
		}
	}
	if len(rows) == 0 {
		return nil, nil, nil, retract
	}

	// --- component label → files, fn label → id (for the calls edge) ---
	fnBySvcLabel := map[string]string{}
	compFiles := map[string]map[string]bool{} // "svc\x00Label" → set(file)
	nodeFile := map[string]string{}
	svcSet := map[string]bool{}
	svcOfFile := map[string]string{}
	for i := range nodes {
		n := &nodes[i]
		nodeFile[n.ID] = n.File
		svcSet[n.Service] = true
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
	addComp := func(svc, label, file string) {
		if svc == "" || label == "" || file == "" {
			return
		}
		k := svc + "\x00" + label
		if compFiles[k] == nil {
			compFiles[k] = map[string]bool{}
		}
		compFiles[k][file] = true
	}
	for _, svc := range sortedBoolKeys(svcSet) {
		ci := newComponentIndex(nodes, svc)
		for sym, ids := range ci.bySymbol {
			for _, id := range ids {
				addComp(svc, sym, nodeFile[id])
			}
		}
	}
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeClass {
			continue
		}
		if l := n.Label; l != "" && l[0] >= 'A' && l[0] <= 'Z' {
			addComp(n.Service, l, n.File)
		}
	}

	// --- index: referenced function symbol → the JSX prop passes carrying it ---
	passBySymbol := map[string][]propFnPass{}
	seen := map[string]bool{}
	for _, svcKey := range sortedMapKeys(serviceFiles) {
		for _, abs := range serviceFiles[svcKey] {
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
			scanPropFnPasses(root, src, svc, rel, passBySymbol)
		}
	}

	// --- parse cache for consumer / child files ---
	type parsed struct {
		src  []byte
		root *sitter.Node
	}
	fileCache := map[string]*parsed{}
	parse := func(rel string) *parsed {
		if p, ok := fileCache[rel]; ok {
			return p
		}
		src, root, _, ok := jsParse(rel)
		var p *parsed
		if ok {
			p = &parsed{src: src, root: root}
		}
		fileCache[rel] = p
		return p
	}

	minted := map[string]bool{}
	unresEmitted := map[string]bool{}

	for _, row := range rows {
		p := parse(row.File)
		if p == nil {
			continue
		}
		call := findPropClientCallAtLine(p.root, p.src, row.Line)
		if call == nil {
			continue
		}
		urlID := propURLConsumerProp(call, p.src)
		if urlID == "" {
			continue
		}
		fn := enclosingJSFunction(call)
		if fn == nil {
			continue
		}
		pi, isParam := jsParamIndex(fn, urlID, p.src)
		if !isParam {
			continue // bucket B/C, not UB.3
		}
		sym := jsEnclosingFnName(call, p.src)
		if sym == "" {
			continue
		}
		passes := passBySymbol[row.Service+"\x00"+sym]
		if len(passes) == 0 {
			continue
		}

		verb := propURLCallVerb(call, p.src)

		pathProv := map[string]string{}
		var caller string
		var unresList []propCallUnres

		for _, ps := range passes {
			if ps.thisScoped && ps.file != row.File {
				continue
			}
			for _, childFile := range sortedBoolKeys(compFiles[row.Service+"\x00"+ps.tag]) {
				cp := parse(childFile)
				if cp == nil {
					continue
				}
				paths, us := scanPropCallArgURLs(cp.root, cp.src, childFile, ps.attr, pi)
				unresList = append(unresList, us...)
				for _, pv := range paths {
					if _, ok := pathProv[pv.path]; !ok {
						pathProv[pv.path] = pv.prov
					}
					site := fmt.Sprintf("%s:%d", pv.file, pv.line)
					if caller == "" || site < caller {
						caller = site
					}
				}
			}
		}

		for _, u := range unresList {
			k := fmt.Sprintf("%s\x00%d\x00%s", u.file, u.line, u.kind)
			if unresEmitted[k] {
				continue
			}
			unresEmitted[k] = true
			out = append(out, graph.UnresolvedRef{
				Service: row.Service, File: u.file, Line: u.line, Name: u.kind,
				Kind: ledgerPropTransportUnresolved, Targets: sym,
			})
		}

		if len(pathProv) == 0 {
			continue
		}
		if len(pathProv) > maxPropURLFanout {
			out = append(out, graph.UnresolvedRef{
				Service: row.Service, File: row.File, Line: row.Line,
				Name: sym, Kind: ledgerPropTransportHighFanout,
			})
			continue
		}

		fnID := fnBySvcLabel[row.Service+"\x00"+sym]
		for _, path := range sortedMapKeys(pathProv) {
			id := fmt.Sprintf("%s:%s:http_client:prop_transport:%d:%s", row.Service, row.File, row.Line, path)
			if minted[id] {
				continue
			}
			minted[id] = true
			newNodes = append(newNodes, graph.Node{
				ID:       id,
				Type:     graph.NodeTypeHTTPClient,
				Label:    verb + " " + path,
				Service:  row.Service,
				File:     row.File,
				Line:     row.Line,
				Language: "javascript",
				Meta: map[string]string{
					"pattern":  "prop_transport",
					"spa":      "prop_transport",
					"method":   verb,
					"url":      path,
					"via":      sym,
					"caller":   caller,
					"producer": pathProv[path],
				},
			})
			if fnID != "" && fnID != id {
				edges = append(edges, graph.Edge{
					ID:         "calls:" + fnID + "->" + id,
					From:       fnID,
					To:         id,
					Type:       graph.EdgeTypeCalls,
					Confidence: graph.ConfidenceInferred,
					Meta:       map[string]string{"spa": "prop_transport"},
				})
			}
		}
		retract[PropURLRetractKey(row.File, row.Line)] = true
	}
	return newNodes, edges, out, retract
}

// scanPropFnPasses walks every JSX element and records each component-tag
// attribute whose value hands over a local function reference.
func scanPropFnPasses(root *sitter.Node, src []byte, svc, rel string, out map[string][]propFnPass) {
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "jsx_opening_element", "jsx_self_closing_element":
			nameNode := n.ChildByFieldName("name")
			if nameNode != nil {
				tag := nameNode.Content(src)
				if i := lastDot(tag); i >= 0 {
					tag = tag[i+1:]
				}
				if tag != "" && tag[0] >= 'A' && tag[0] <= 'Z' {
					for i := 0; i < int(n.NamedChildCount()); i++ {
						if attr := n.NamedChild(i); attr.Type() == "jsx_attribute" {
							recordPropFnPass(attr, n, src, svc, rel, tag, out)
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
}

func recordPropFnPass(attr, jsxElem *sitter.Node, src []byte, svc, rel, tag string, out map[string][]propFnPass) {
	if attr.NamedChildCount() < 2 {
		return
	}
	nameNode := attr.NamedChild(0)
	if nameNode.Type() != "property_identifier" {
		return
	}
	attrName := nameNode.Content(src)
	valNode := attr.NamedChild(1)
	if valNode.Type() != "jsx_expression" || valNode.NamedChildCount() == 0 {
		return
	}
	expr := valNode.NamedChild(0)

	var sym string
	thisScoped := false
	switch expr.Type() {
	case "identifier":
		sym = expr.Content(src)
	case "member_expression":
		obj := expr.ChildByFieldName("object")
		prop := expr.ChildByFieldName("property")
		if obj == nil || prop == nil || prop.Type() != "property_identifier" {
			return
		}
		sym = prop.Content(src)
		if obj.Type() == "this" || (obj.Type() == "identifier" && obj.Content(src) == "this") {
			thisScoped = true
		}
	default:
		return
	}
	if sym == "" || !(sym[0] >= 'a' && sym[0] <= 'z' || sym[0] == '_') {
		// a Capitalized value is a component, not a transport helper.
		return
	}
	key := svc + "\x00" + sym
	line := int(jsxElem.StartPoint().Row) + 1
	for _, e := range out[key] {
		if e.tag == tag && e.attr == attrName && e.file == rel {
			return
		}
	}
	out[key] = append(out[key], propFnPass{tag: tag, attr: attrName, file: rel, line: line, thisScoped: thisScoped})
}

type propCallURL struct {
	path string
	file string
	line int
	prov string
}

type propCallUnres struct {
	file string
	line int
	kind string
}

// scanPropCallArgURLs finds every `props.<attr>(…)` / `this.props.<attr>(…)` /
// bare `<attr>(…)` (when attr is a prop) call in a child component file and
// reads the argument at position pi as a request path.
func scanPropCallArgURLs(root *sitter.Node, src []byte, rel, attr string, pi int) (urls []propCallURL, unres []propCallUnres) {
	propNames := map[string]bool{attr: true}
	bareOK := fileHasProp(root, src, attr)

	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n.Type() == "call_expression" && propCallHasAttr(n, src, propNames, bareOK) {
			args := n.ChildByFieldName("arguments")
			if args != nil {
				arg := positionalArg(args, pi)
				line := int(n.StartPoint().Row) + 1
				prov := fmt.Sprintf("%s:%d", rel, line)
				if arg == nil {
					unres = append(unres, propCallUnres{rel, line, "arity"})
				} else {
					switch arg.Type() {
					case "string", "template_string":
						if u, ok := resolveJSPropURL(arg.Content(src)); ok {
							urls = append(urls, propCallURL{path: u, file: rel, line: line, prov: prov})
						} else {
							unres = append(unres, propCallUnres{rel, line, "literal"})
						}
					case "identifier":
						if got, ok := ResolveLocalURLBinding(arg, enclosingJSFunction(n), src); ok && len(got) > 0 {
							for _, u := range got {
								urls = append(urls, propCallURL{path: u, file: rel, line: line, prov: prov})
							}
						} else {
							unres = append(unres, propCallUnres{rel, line, "identifier"})
						}
					case "member_expression":
						unres = append(unres, propCallUnres{rel, line, "member_expression"})
					case "call_expression":
						unres = append(unres, propCallUnres{rel, line, "builder_call"})
					default:
						unres = append(unres, propCallUnres{rel, line, "expression"})
					}
				}
			}
		}
		for i := 0; i < int(n.NamedChildCount()); i++ {
			walk(n.NamedChild(i))
		}
	}
	walk(root)
	return urls, unres
}

// propCallHasAttr reports whether call's callee reads prop `attr` off props:
// `this.props.attr(…)`, `props.attr(…)`, or `attr(…)` when attr is destructured.
func propCallHasAttr(call *sitter.Node, src []byte, propNames map[string]bool, bareOK bool) bool {
	callee := call.ChildByFieldName("function")
	if callee == nil {
		return false
	}
	switch callee.Type() {
	case "identifier":
		return bareOK && propNames[callee.Content(src)]
	case "member_expression":
		prop := callee.ChildByFieldName("property")
		if prop == nil || !propNames[prop.Content(src)] {
			return false
		}
		obj := callee.ChildByFieldName("object")
		if obj == nil {
			return false
		}
		if obj.Type() == "identifier" && obj.Content(src) == "props" {
			return true
		}
		// this.props.<attr>
		if obj.Type() == "member_expression" {
			o2 := obj.ChildByFieldName("object")
			p2 := obj.ChildByFieldName("property")
			if o2 != nil && p2 != nil && p2.Content(src) == "props" &&
				(o2.Type() == "this" || (o2.Type() == "identifier" && o2.Content(src) == "this")) {
				return true
			}
		}
	}
	return false
}

// positionalArg returns the i-th positional argument, or nil if a spread
// element appears at or before that position (which shifts every index).
func positionalArg(args *sitter.Node, i int) *sitter.Node {
	idx := 0
	for c := 0; c < int(args.NamedChildCount()); c++ {
		n := args.NamedChild(c)
		if n.Type() == "comment" {
			continue
		}
		if n.Type() == "spread_element" {
			return nil
		}
		if idx == i {
			return n
		}
		idx++
	}
	return nil
}

// jsParamIndex returns the positional index of the parameter named `name`.
func jsParamIndex(fn *sitter.Node, name string, src []byte) (int, bool) {
	if fn == nil {
		return 0, false
	}
	params := fn.ChildByFieldName("parameters")
	if params == nil {
		if p := fn.ChildByFieldName("parameter"); p != nil && paramName(p, src) == name {
			return 0, true
		}
		return 0, false
	}
	idx := 0
	for i := 0; i < int(params.NamedChildCount()); i++ {
		c := params.NamedChild(i)
		if c.Type() == "comment" {
			continue
		}
		if paramName(c, src) == name {
			return idx, true
		}
		idx++
	}
	return 0, false
}

func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}
