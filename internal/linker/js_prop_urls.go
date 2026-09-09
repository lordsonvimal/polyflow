package linker

import (
	"fmt"
	"sort"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// Tier UB.2 — the URL crosses a JSX prop.
//
// The parent component computes the endpoint and passes it down as a prop; the
// child performs the request. Tier CW (LinkJSPropClients) recognises the child's
// transport call — the receiver is a prop-injected transport — and finds the URL
// argument unreadable because it is a bare prop identifier, so it ledgers
// `prop_client_dynamic_url` and mints nothing. This pass supplies the missing
// half: it indexes every JSX attribute value in the service, keyed by
// (component, prop name), and joins it back to each ledger site.
//
// It mints one http_client node per DISTINCT resolved URL rather than one node
// with several http_call edges — a reusable modal genuinely has many endpoints,
// and Tier UL settled that shape: no single node may reach more than one
// handler. This deliberately differs from LinkReactPropURLs, which abstains when
// a prop resolves to two different paths across render sites. For the
// Rails→React boundary a prop disagreement usually means the resolver bound the
// wrong component; for the JSX→JSX boundary, disagreement is the normal and
// correct situation (one modal, many create URLs).
//
// Must run AFTER js_prop_clients (it consumes that pass's ledger) and after
// js_local_urls (it reuses the local-binding resolution for producer values).
// Producer values that are builder calls, member expressions or server-supplied
// (`schema.create_url`) are NOT resolved here — they ledger `prop_url_unresolved`
// and are Tier MS.3 / SPA.5 territory.

const (
	ledgerPropURLUnresolved = "prop_url_unresolved"
	ledgerPropURLHighFanout = "prop_url_high_fanout"

	// maxPropURLFanout caps how many distinct URLs one consumer prop may
	// resolve to. Cedar's widest genuine case is 18 (a create modal rendered
	// from 18 sites); 24 is slack above the observed maximum rather than a
	// working limit. A component above it is a generic transport, not a feature.
	maxPropURLFanout = 24
)

// propURLProducer is the set of resolved paths a (component, prop) pair carries
// across all its JSX render sites, plus the render sites whose value could not
// be resolved.
type propURLProducer struct {
	paths      map[string]string // path → "<file>:<line>" provenance (first writer wins)
	unresolved []propURLUnres
}

type propURLUnres struct {
	file string
	line int
	kind string // builder_call | member_expression | expression | ...
}

// PropURLRetractKey is the key LinkJSPropURLs' retract set uses and the caller
// must reconstruct to drop a now-resolved prop_client_dynamic_url row.
func PropURLRetractKey(file string, line int) string {
	return fmt.Sprintf("%s\x00%d", file, line)
}

// LinkJSPropURLs is the Tier UB.2 pass. It returns the synthetic http_client
// nodes, the `calls` edges wiring them to their enclosing functions, the
// prop_url_* ledger, and the set of prop_client_dynamic_url sites that resolved
// (keyed by PropURLRetractKey) so the caller can retract them.
func LinkJSPropURLs(nodes []graph.Node, ledger []graph.UnresolvedRef, serviceFiles map[string][]string) (newNodes []graph.Node, edges []graph.Edge, out []graph.UnresolvedRef, retract map[string]bool) {
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

	// --- component labels per file, fn label → id (for the calls edge) ---
	fnBySvcLabel := map[string]string{}
	compsByFile := map[string]map[string]bool{}
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
	addComp := func(f, sym string) {
		if f == "" || sym == "" {
			return
		}
		if compsByFile[f] == nil {
			compsByFile[f] = map[string]bool{}
		}
		compsByFile[f][sym] = true
	}
	svcs := sortedBoolKeys(svcSet)
	for _, svc := range svcs {
		ci := newComponentIndex(nodes, svc)
		for sym, ids := range ci.bySymbol {
			for _, id := range ids {
				addComp(nodeFile[id], sym)
			}
		}
	}
	// A component that only forwards a prop is often never registered on
	// `window`; fall back to a Capitalized top-level declaration in its file.
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeClass {
			continue
		}
		if l := n.Label; l != "" && l[0] >= 'A' && l[0] <= 'Z' {
			addComp(n.File, l)
		}
	}

	// --- producer index: (service, tag, attr) → resolved paths ---
	producers := map[string]*propURLProducer{}
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
			scanPropURLProducers(root, src, svc, rel, producers)
		}
	}

	// --- join: each ledger site → mint one node per distinct resolved URL ---
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
		prop := propURLConsumerProp(call, p.src)
		if prop == "" || !fileHasProp(p.root, p.src, prop) {
			continue
		}
		comps := compsByFile[row.File]
		if len(comps) == 0 {
			continue
		}

		pathProv := map[string]string{}
		var unres []propURLUnres
		anyProducer := false
		for _, comp := range sortedBoolKeys(comps) {
			pe := producers[row.Service+"\x00"+comp+"\x00"+prop]
			if pe == nil {
				continue
			}
			anyProducer = true
			for path, prov := range pe.paths {
				if _, ok := pathProv[path]; !ok {
					pathProv[path] = prov
				}
			}
			unres = append(unres, pe.unresolved...)
		}
		if !anyProducer {
			continue
		}

		// An unresolvable producer ledgers whether or not its siblings resolved —
		// suppressing it would hide a real render site behind the ones that did.
		for _, u := range unres {
			k := fmt.Sprintf("%s\x00%d\x00%s", u.file, u.line, u.kind)
			if unresEmitted[k] {
				continue
			}
			unresEmitted[k] = true
			out = append(out, graph.UnresolvedRef{
				Service: row.Service, File: u.file, Line: u.line,
				Name: prop, Kind: ledgerPropURLUnresolved, Targets: u.kind,
			})
		}
		if len(pathProv) == 0 {
			continue
		}
		if len(pathProv) > maxPropURLFanout {
			out = append(out, graph.UnresolvedRef{
				Service: row.Service, File: row.File, Line: row.Line,
				Name: prop, Kind: ledgerPropURLHighFanout,
			})
			continue
		}

		verb := propURLCallVerb(call, p.src)
		fnLabel := jsEnclosingFnName(call, p.src)
		for _, path := range sortedMapKeys(pathProv) {
			id := fmt.Sprintf("%s:%s:http_client:prop_url:%d:%s", row.Service, row.File, row.Line, path)
			if minted[id] {
				continue
			}
			minted[id] = true
			meta := map[string]string{
				"pattern":  "prop_url",
				"spa":      "prop_url",
				"method":   verb,
				"url":      path,
				"prop":     prop,
				"producer": pathProv[path],
			}
			newNodes = append(newNodes, graph.Node{
				ID:       id,
				Type:     graph.NodeTypeHTTPClient,
				Label:    verb + " " + path,
				Service:  row.Service,
				File:     row.File,
				Line:     row.Line,
				Language: "javascript",
				Meta:     meta,
			})
			if fnID := fnBySvcLabel[row.Service+"\x00"+fnLabel]; fnID != "" && fnID != id {
				edges = append(edges, graph.Edge{
					ID:         "calls:" + fnID + "->" + id,
					From:       fnID,
					To:         id,
					Type:       graph.EdgeTypeCalls,
					Confidence: graph.ConfidenceInferred,
					Meta:       map[string]string{"spa": "prop_url"},
				})
			}
		}
		retract[PropURLRetractKey(row.File, row.Line)] = true
	}
	return newNodes, edges, out, retract
}

// scanPropURLProducers walks every JSX element in a file and records each
// component-tag attribute whose value can be read as a request path.
func scanPropURLProducers(root *sitter.Node, src []byte, svc, rel string, out map[string]*propURLProducer) {
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		switch n.Type() {
		case "jsx_opening_element", "jsx_self_closing_element":
			if nameNode := n.ChildByFieldName("name"); nameNode != nil {
				tag := nameNode.Content(src)
				if i := strings.LastIndexByte(tag, '.'); i >= 0 {
					tag = tag[i+1:]
				}
				if tag != "" && tag[0] >= 'A' && tag[0] <= 'Z' {
					for i := 0; i < int(n.NamedChildCount()); i++ {
						if attr := n.NamedChild(i); attr.Type() == "jsx_attribute" {
							recordPropURLProducer(attr, n, src, svc, rel, tag, out)
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

func recordPropURLProducer(attr, jsxElem *sitter.Node, src []byte, svc, rel, tag string, out map[string]*propURLProducer) {
	if attr.NamedChildCount() < 2 {
		return
	}
	nameNode := attr.NamedChild(0)
	if nameNode.Type() != "property_identifier" {
		return
	}
	attrName := nameNode.Content(src)
	valNode := attr.NamedChild(1)
	var expr *sitter.Node
	switch valNode.Type() {
	case "string":
		expr = valNode
	case "jsx_expression":
		if valNode.NamedChildCount() == 0 {
			return
		}
		expr = valNode.NamedChild(0)
	default:
		return
	}

	line := int(jsxElem.StartPoint().Row) + 1
	key := svc + "\x00" + tag + "\x00" + attrName
	pe := out[key]
	if pe == nil {
		pe = &propURLProducer{paths: map[string]string{}}
		out[key] = pe
	}
	prov := fmt.Sprintf("%s:%d", rel, line)

	paths, kind := resolvePropURLProducerValue(expr, jsxElem, src)
	if len(paths) > 0 {
		for _, path := range paths {
			if _, ok := pe.paths[path]; !ok {
				pe.paths[path] = prov
			}
		}
		return
	}
	if kind != "" {
		pe.unresolved = append(pe.unresolved, propURLUnres{file: rel, line: line, kind: kind})
	}
}

// resolvePropURLProducerValue reads one JSX attribute value as request paths.
// A literal / template path resolves directly; a bare identifier is resolved
// through Tier UL's local-binding walk in the producing function; anything else
// is reported by kind and left for a later tier.
func resolvePropURLProducerValue(expr, jsxElem *sitter.Node, src []byte) (paths []string, unresolvedKind string) {
	switch expr.Type() {
	case "string", "template_string":
		if u, ok := resolveJSPropURL(expr.Content(src)); ok {
			return []string{u}, ""
		}
		return nil, "literal"
	case "identifier":
		fn := enclosingJSFunction(jsxElem)
		if fn == nil {
			return nil, "identifier"
		}
		if got, ok := ResolveLocalURLBinding(expr, fn, src); ok && len(got) > 0 {
			return got, ""
		}
		return nil, "identifier"
	case "call_expression":
		return nil, "builder_call"
	case "member_expression":
		return nil, "member_expression"
	case "binary_expression", "ternary_expression", "conditional_expression":
		return nil, "expression"
	}
	return nil, ""
}

// findPropClientCallAtLine returns the first `<recv>.<method>(...)` call
// starting on line whose method name is one a prop-client may expose.
func findPropClientCallAtLine(root *sitter.Node, src []byte, line int) *sitter.Node {
	var found *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != nil || n == nil {
			return
		}
		if n.Type() == "call_expression" && int(n.StartPoint().Row)+1 == line {
			if callee := n.ChildByFieldName("function"); callee != nil && callee.Type() == "member_expression" {
				if pr := callee.ChildByFieldName("property"); pr != nil && propClientMethodNames[pr.Content(src)] {
					found = n
					return
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return found
}

// propURLConsumerProp returns the bare prop identifier a call reads its URL
// from: a positional identifier argument, or the `url` key / shorthand of an
// options object argument.
func propURLConsumerProp(call *sitter.Node, src []byte) string {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return ""
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		a := args.NamedChild(i)
		switch a.Type() {
		case "identifier":
			return a.Content(src)
		case "object":
			if v := jsObjectKeyValue(a, src, "url"); v != nil {
				switch v.Type() {
				case "identifier", "shorthand_property_identifier", "property_identifier":
					return v.Content(src)
				}
			}
		}
	}
	return ""
}

// fileHasProp reports whether name is read as a prop somewhere in the file:
// `this.props.name`, `const { name } = this.props`, or a `{ name }` destructure
// in a function-component parameter.
func fileHasProp(root *sitter.Node, src []byte, name string) bool {
	if collectPropsBindings(root, src)[name] {
		return true
	}
	return jsDestructuresName(root, src, name)
}

// propURLCallVerb derives the HTTP verb from the transport call: the method name
// (`.post` → POST) or, for `.ajax`/`.request`, the `method`/`type` key of an
// options object argument. Defaults to GET.
func propURLCallVerb(call *sitter.Node, src []byte) string {
	if callee := call.ChildByFieldName("function"); callee != nil && callee.Type() == "member_expression" {
		if pr := callee.ChildByFieldName("property"); pr != nil {
			if v := propClientVerb(pr.Content(src)); v != "" {
				return v
			}
		}
	}
	if args := call.ChildByFieldName("arguments"); args != nil {
		for i := 0; i < int(args.NamedChildCount()); i++ {
			a := args.NamedChild(i)
			if a.Type() != "object" {
				continue
			}
			for _, k := range []string{"method", "type"} {
				if vn := jsObjectKeyValue(a, src, k); vn != nil && vn.Type() == "string" {
					return strings.ToUpper(strings.Trim(vn.Content(src), `"'`))
				}
			}
		}
	}
	return "GET"
}

// jsEnclosingFnName returns the name of the nearest enclosing function, method,
// or arrow bound to a declarator/field — for the `calls` edge lookup.
func jsEnclosingFnName(n *sitter.Node, src []byte) string {
	fn := enclosingJSFunction(n)
	if fn == nil {
		return ""
	}
	switch fn.Type() {
	case "function_declaration", "generator_function_declaration", "method_definition":
		if nm := fn.ChildByFieldName("name"); nm != nil {
			return nm.Content(src)
		}
	}
	for p := fn.Parent(); p != nil; p = p.Parent() {
		switch p.Type() {
		case "variable_declarator", "public_field_definition", "field_definition":
			if nm := p.ChildByFieldName("name"); nm != nil {
				return nm.Content(src)
			}
			return ""
		case "function_declaration", "method_definition":
			if nm := p.ChildByFieldName("name"); nm != nil {
				return nm.Content(src)
			}
			return ""
		}
	}
	return ""
}

func sortedBoolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
