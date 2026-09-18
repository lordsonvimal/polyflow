package factpipe

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/jsast"
	"github.com/lordsonvimal/polyflow/internal/schemaurl"
	"github.com/lordsonvimal/polyflow/internal/valuegraph"
	"github.com/lordsonvimal/polyflow/internal/valuegraphfacts"
)

// hub_js_prop_crossings.go is the Tier FX/RC.3 migration
// (docs/js-declarative-composition-cluster-plan.md) of
// internal/linker/js_prop_urls.go's LinkJSPropURLs (Tier UB.2) and
// internal/linker/js_prop_transport.go's LinkJSPropTransport (Tier UB.3).
// Both consume the SAME "prop_client_dynamic_url" ledger row
// (internal/factpipe/hub_schema_url_link.go's SPA.4 mint pass leaves it
// when a prop-injected transport call's URL argument is unreadable) and
// resolve it through the crossing rules internal/valuegraph/javascript.yaml
// already declares (`jsx_attribute` / `jsx_attribute_callback`) — RC.1's
// valuegraphfacts.Resolve + ComponentIndex is what makes the JOIN itself
// (which render site names this prop on which component) generic; what
// stays hand-written here is real per-expression recognition (which call is
// a transport, which argument is the URL, which verb it carries, whether a
// name is a parameter) and the per-producer-site failure/fan-out policy —
// same "hub carries the whole algorithm" shape as schema_url_link_props.
//
// UB.2 and UB.3 are one hub, not two: they process the exact same ledger
// row set, in the fixed order UB.2-then-UB.3 the retired indexer passes
// enforced by running as two sequential Go calls — here it is two
// sequential loops in one Go call instead, which is simpler and equally
// deterministic, and lets svcPath/resolver/ComponentIndex be built once for
// both. A row UB.2 resolves is retracted before UB.3 ever sees it, the same
// "ledger already thinned by js_prop_urls" invariant.
func init() {
	RegisterHub("js_prop_crossings", jpxHub)
}

const (
	jpxURLMintPred         = "jpx_url_mint"         // (ID, Service, File, Line, Label, Method, URL, Prop, Producer, SchemaFile, SchemaEntity, SchemaKey, SchemaURLRaw, RetractKey)
	jpxURLEdgePred         = "jpx_url_edge"         // (FnID, ID)
	jpxURLLedgerPred       = "jpx_url_ledger"       // (Service, File, Line, Name, Kind)
	jpxTransportMintPred   = "jpx_transport_mint"   // (ID, Service, File, Line, Label, Method, URL, Via, Caller, Producer, SchemaFile, SchemaEntity, SchemaKey, SchemaURLRaw, RetractKey)
	jpxTransportEdgePred   = "jpx_transport_edge"   // (FnID, ID)
	jpxTransportLedgerPred = "jpx_transport_ledger" // (Service, File, Line, Name, Kind)

	jpxLedgerPropURLUnresolved       = "prop_url_unresolved"
	jpxLedgerPropURLHighFanout       = "prop_url_high_fanout"
	jpxLedgerPropTransportUnresolved = "prop_transport_unresolved"
	jpxLedgerPropTransportHighFanout = "prop_transport_high_fanout"

	// jpxMaxPropURLFanout caps how many distinct URLs one consumer prop may
	// resolve to — ported verbatim from internal/linker/js_prop_urls.go's
	// maxPropURLFanout (cedar's widest genuine case is 18; 24 is slack
	// above the observed maximum, not a working limit).
	jpxMaxPropURLFanout = 24
	// jpxVGPropMaxStrings is the engine's union-width ceiling, deliberately
	// wider than jpxMaxPropURLFanout: the cap is the mint site's decision.
	jpxVGPropMaxStrings = 2 * jpxMaxPropURLFanout

	// jpxCrossPropURL/jpxCrossPropTransport are the two crossing kinds
	// internal/valuegraph/javascript.yaml declares — the vg_rule strings
	// stamped onto minted nodes (patterns/generic/js_prop_crossings.yaml's
	// `meta.vg_rule`) are these names verbatim, prefixed "valuegraph/javascript#".
	jpxCrossPropURL       = "jsx_attribute"
	jpxCrossPropTransport = "jsx_attribute_callback"
)

func jpxHub(nodes []graph.Node, files []string, svcPath string, _ []graph.LinkHint, schema graph.SchemaConfig, unresolved []graph.UnresolvedRef) []Fact {
	svc, resolver := sulBuildResolver(nodes, files, svcPath, schema)
	if svc == "" {
		return nil
	}
	rows := jpxPropClientDynamicRows(unresolved)
	if len(rows) == 0 {
		return nil
	}

	fnBySvcLabel := map[string]string{}
	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeMethod:
			if k := n.Service + "\x00" + n.Label; fnBySvcLabel[k] == "" {
				fnBySvcLabel[k] = n.ID
			}
		}
	}

	cross := valuegraphfacts.BuildComponentIndex(nodes, svc)
	spec := jpxValuegraphSpec()

	var jsFiles []string
	seen := map[string]bool{}
	for _, abs := range files {
		if !jsast.IsJSFile(abs) {
			continue
		}
		rel := sulRelativize(abs)
		if seen[rel] || jsast.IsTestFile(rel) {
			continue
		}
		seen[rel] = true
		jsFiles = append(jsFiles, rel)
	}
	sort.Strings(jsFiles)

	parseCache := map[string]*jpxParsedFile{}
	parse := func(rel string) *jpxParsedFile {
		if p, ok := parseCache[rel]; ok {
			return p
		}
		var p *jpxParsedFile
		if src, root, _, ok := jsast.Parse(rel); ok {
			p = &jpxParsedFile{src: src, root: root}
		}
		parseCache[rel] = p
		return p
	}

	// valuegraphfacts.Resolve builds a fresh valuegraph.Engine (and re-derives
	// its crossing index) on every call — reusing one engine per service the
	// way newJSPropScope used to requires batching every site into ONE call
	// per phase rather than one call per ledger row (a 40-row service went
	// from ~9ms to ~113ms before this batching — the RC.3 perf gate this
	// comment exists to keep from regressing again).
	resolveBatch := func(sites []valuegraphfacts.Site) []valuegraph.Value {
		if len(sites) == 0 {
			return nil
		}
		results := valuegraphfacts.Resolve(spec, jsFiles, cross, sites,
			valuegraph.Options{MaxUnionWidth: jpxVGPropMaxStrings, MaxFiles: len(jsFiles) + 8})
		out := make([]valuegraph.Value, len(results))
		for i, r := range results {
			out[i] = r.Value
		}
		return out
	}

	var out []Fact
	retracted := map[string]bool{} // "file\x00line" rows already resolved by UB.2
	minted := map[string]bool{}

	// ── UB.2: the URL crosses a JSX prop ────────────────────────────────────
	type urlCandidate struct {
		row  graph.UnresolvedRef
		call *sitter.Node
		src  []byte
		prop string
	}
	var urlCands []urlCandidate
	var urlSites []valuegraphfacts.Site
	for _, row := range rows {
		p := parse(row.File)
		if p == nil {
			continue
		}
		call := jpxFindPropClientCallAtLine(p.root, p.src, row.Line)
		if call == nil {
			continue
		}
		urlNode := jpxPropURLConsumerPropNode(call, p.src)
		if urlNode == nil {
			continue
		}
		prop := urlNode.Content(p.src)
		if !jpxFileHasProp(p.root, p.src, prop) {
			continue
		}
		urlCands = append(urlCands, urlCandidate{row: row, call: call, src: p.src, prop: prop})
		urlSites = append(urlSites, valuegraphfacts.Site{File: row.File, Expr: urlNode})
	}
	urlValues := resolveBatch(urlSites)

	unresEmittedURL := map[string]bool{}
	for ci, cand := range urlCands {
		row, call, csrc, prop := cand.row, cand.call, cand.src, cand.prop

		v := urlValues[ci]
		producers := jpxPropProducers(v, jpxCrossPropURL, resolver, svc)
		if len(producers) == 0 {
			continue
		}

		pathProv := map[string]string{}
		schemaMeta := map[string]map[string]string{}
		for _, pr := range producers {
			if pr.fail != "" {
				k := fmt.Sprintf("%s\x00%d\x00%s", pr.file, pr.line, pr.fail)
				if !unresEmittedURL[k] {
					unresEmittedURL[k] = true
					out = append(out, jpxLedgerFact(jpxURLLedgerPred, svc, pr.file, pr.line, prop, jpxLedgerPropURLUnresolved, pr.fail))
				}
				continue
			}
			for _, path := range pr.paths {
				if _, s := pathProv[path]; !s {
					pathProv[path] = fmt.Sprintf("%s:%d", pr.file, pr.line)
				}
				if pr.schemaMeta != nil {
					schemaMeta[path] = pr.schemaMeta
				}
			}
		}
		if len(pathProv) == 0 {
			continue
		}
		if len(pathProv) > jpxMaxPropURLFanout {
			out = append(out, jpxLedgerFact(jpxURLLedgerPred, svc, row.File, row.Line, prop, jpxLedgerPropURLHighFanout, ""))
			continue
		}

		verb := jpxPropURLCallVerb(call, csrc)
		fnLabel := jpxEnclosingFnName(call, csrc)
		retractKey := row.File + "\x00" + fmt.Sprint(row.Line)
		for _, path := range jpxSortedMapKeys(pathProv) {
			id := fmt.Sprintf("%s:%s:http_client:prop_url:%d:%s", svc, row.File, row.Line, path)
			if minted[id] {
				continue
			}
			minted[id] = true
			sm := schemaMeta[path]
			out = append(out, Fact{
				Pred: jpxURLMintPred,
				Args: []Atom{
					Node(id), Str(svc), Str(row.File), Int(int64(row.Line)), Str(verb + " " + path),
					Str(verb), Str(path), Str(prop), Str(pathProv[path]),
					Str(sm["schema_file"]), Str(sm["schema_entity"]), Str(sm["schema_key"]), Str(sm["schema_url_raw"]),
					Str(retractKey),
				},
				Origin: Origin{Kind: OriginPrimitive, File: row.File, Line: row.Line, Pattern: jpxURLMintPred},
			})
			if fnID := fnBySvcLabel[svc+"\x00"+fnLabel]; fnID != "" && fnID != id {
				out = append(out, Fact{
					Pred:   jpxURLEdgePred,
					Args:   []Atom{Node(fnID), Node(id)},
					Origin: Origin{Kind: OriginPrimitive, File: row.File, Line: row.Line, Pattern: jpxURLEdgePred},
				})
			}
		}
		retracted[retractKey] = true
	}

	// ── UB.3: the transport function crosses a JSX prop ─────────────────────
	type transportCandidate struct {
		row  graph.UnresolvedRef
		call *sitter.Node
		src  []byte
		sym  string
	}
	var tCands []transportCandidate
	var tSites []valuegraphfacts.Site
	for _, row := range rows {
		retractKey := row.File + "\x00" + fmt.Sprint(row.Line)
		if retracted[retractKey] {
			continue
		}
		p := parse(row.File)
		if p == nil {
			continue
		}
		call := jpxFindPropClientCallAtLine(p.root, p.src, row.Line)
		if call == nil {
			continue
		}
		urlNode := jpxPropURLConsumerPropNode(call, p.src)
		if urlNode == nil {
			continue
		}
		fn := jsast.EnclosingFunction(call)
		if fn == nil {
			continue
		}
		if _, isParam := jpxParamIndex(fn, urlNode.Content(p.src), p.src); !isParam {
			continue // bucket B/C, not UB.3
		}
		sym := jpxEnclosingFnName(call, p.src)
		if sym == "" {
			continue
		}
		tCands = append(tCands, transportCandidate{row: row, call: call, src: p.src, sym: sym})
		tSites = append(tSites, valuegraphfacts.Site{File: row.File, Expr: urlNode})
	}
	tValues := resolveBatch(tSites)

	unresEmittedTransport := map[string]bool{}
	for ci, cand := range tCands {
		row, call, csrc, sym := cand.row, cand.call, cand.src, cand.sym

		v := tValues[ci]
		producers := jpxPropProducers(v, jpxCrossPropTransport, resolver, svc)
		if len(producers) == 0 {
			continue
		}

		pathProv := map[string]string{}
		schemaMeta := map[string]map[string]string{}
		var caller string
		for _, pr := range producers {
			site := fmt.Sprintf("%s:%d", pr.file, pr.line)
			if pr.fail != "" {
				k := fmt.Sprintf("%s\x00%d\x00%s", pr.file, pr.line, pr.fail)
				if !unresEmittedTransport[k] {
					unresEmittedTransport[k] = true
					out = append(out, jpxLedgerFact(jpxTransportLedgerPred, svc, pr.file, pr.line, pr.fail, jpxLedgerPropTransportUnresolved, sym))
				}
				continue
			}
			for _, path := range pr.paths {
				if _, s := pathProv[path]; !s {
					pathProv[path] = site
				}
				if pr.schemaMeta != nil {
					schemaMeta[path] = pr.schemaMeta
				}
				if caller == "" || site < caller {
					caller = site
				}
			}
		}
		if len(pathProv) == 0 {
			continue
		}
		if len(pathProv) > jpxMaxPropURLFanout {
			out = append(out, jpxLedgerFact(jpxTransportLedgerPred, svc, row.File, row.Line, sym, jpxLedgerPropTransportHighFanout, ""))
			continue
		}

		verb := jpxPropURLCallVerb(call, csrc)
		retractKey := row.File + "\x00" + fmt.Sprint(row.Line)
		fnID := fnBySvcLabel[svc+"\x00"+sym]
		for _, path := range jpxSortedMapKeys(pathProv) {
			id := fmt.Sprintf("%s:%s:http_client:prop_transport:%d:%s", svc, row.File, row.Line, path)
			if minted[id] {
				continue
			}
			minted[id] = true
			sm := schemaMeta[path]
			out = append(out, Fact{
				Pred: jpxTransportMintPred,
				Args: []Atom{
					Node(id), Str(svc), Str(row.File), Int(int64(row.Line)), Str(verb + " " + path),
					Str(verb), Str(path), Str(sym), Str(caller), Str(pathProv[path]),
					Str(sm["schema_file"]), Str(sm["schema_entity"]), Str(sm["schema_key"]), Str(sm["schema_url_raw"]),
					Str(retractKey),
				},
				Origin: Origin{Kind: OriginPrimitive, File: row.File, Line: row.Line, Pattern: jpxTransportMintPred},
			})
			if fnID != "" && fnID != id {
				out = append(out, Fact{
					Pred:   jpxTransportEdgePred,
					Args:   []Atom{Node(fnID), Node(id)},
					Origin: Origin{Kind: OriginPrimitive, File: row.File, Line: row.Line, Pattern: jpxTransportEdgePred},
				})
			}
		}
		retracted[retractKey] = true
	}

	return out
}

func jpxLedgerFact(pred, svc, file string, line int, name, kind, targets string) Fact {
	return Fact{
		Pred:   pred,
		Args:   []Atom{Str(svc), Str(file), Int(int64(line)), Str(name), Str(kind), Str(targets)},
		Origin: Origin{Kind: OriginPrimitive, File: file, Line: line, Pattern: pred},
	}
}

func jpxPropClientDynamicRows(ledger []graph.UnresolvedRef) []graph.UnresolvedRef {
	var rows []graph.UnresolvedRef
	for _, u := range ledger {
		if u.Kind == "prop_client_dynamic_url" {
			rows = append(rows, u)
		}
	}
	return rows
}

func jpxSortedMapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var (
	jpxSpecOnce sync.Once
	jpxSpec     *valuegraph.Spec
)

// jpxValuegraphSpec returns the embedded JavaScript/TypeScript binding spec,
// loaded once. A load failure yields a nil spec, which the engine treats as
// "resolve everything to Opaque".
func jpxValuegraphSpec() *valuegraph.Spec {
	jpxSpecOnce.Do(func() {
		if s, err := valuegraph.EmbeddedSpec("javascript"); err == nil {
			jpxSpec = s
		}
	})
	return jpxSpec
}

// ── recognition: which call is a transport, which argument is the URL ──────
// Ported from internal/linker/js_prop_urls.go/js_prop_transport.go —
// internal/factpipe cannot import internal/linker (import cycle through
// internal/patterns), same precedent as hub_schema_url_link.go's sul-prefixed
// port of internal/linker/js_prop_client.go.

type jpxParsedFile struct {
	src  []byte
	root *sitter.Node
}

// jpxFindPropClientCallAtLine returns the first `<recv>.<method>(...)` call
// starting on line whose method name is one a prop-client may expose.
func jpxFindPropClientCallAtLine(root *sitter.Node, src []byte, line int) *sitter.Node {
	var found *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != nil || n == nil {
			return
		}
		if n.Type() == "call_expression" && int(n.StartPoint().Row)+1 == line {
			if callee := n.ChildByFieldName("function"); callee != nil && callee.Type() == "member_expression" {
				if pr := callee.ChildByFieldName("property"); pr != nil && sulPropClientMethodNames[pr.Content(src)] {
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

// jpxPropURLConsumerPropNode returns the bare prop identifier a call reads
// its URL from: a positional identifier argument, or the `url` key /
// shorthand of an options object argument.
func jpxPropURLConsumerPropNode(call *sitter.Node, src []byte) *sitter.Node {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return nil
	}
	for i := 0; i < int(args.NamedChildCount()); i++ {
		a := args.NamedChild(i)
		switch a.Type() {
		case "identifier":
			return a
		case "object":
			if v := jsast.ObjectKeyValue(a, src, "url"); v != nil {
				switch v.Type() {
				case "identifier", "shorthand_property_identifier", "property_identifier":
					return v
				}
			}
		}
	}
	return nil
}

// jpxFileHasProp reports whether name is read as a prop somewhere in the
// file: `this.props.name`, `const { name } = this.props`, or a `{ name }`
// destructure in a function-component parameter.
func jpxFileHasProp(root *sitter.Node, src []byte, name string) bool {
	if sulCollectPropsBindings(root, src)[name] {
		return true
	}
	return jpxDestructuresName(root, src, name)
}

// jpxDestructuresName reports whether name appears as a key in any object
// destructuring pattern in the tree (`({ name }) => …`, `const { name } = x`).
func jpxDestructuresName(root *sitter.Node, src []byte, name string) bool {
	found := false
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil || found {
			return
		}
		if n.Type() == "object_pattern" {
			for i := 0; i < int(n.ChildCount()); i++ {
				c := n.Child(i)
				switch c.Type() {
				case "shorthand_property_identifier_pattern":
					if c.Content(src) == name {
						found = true
						return
					}
				case "pair_pattern":
					if k := c.ChildByFieldName("key"); k != nil && k.Content(src) == name {
						found = true
						return
					}
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

// jpxPropURLCallVerb derives the HTTP verb from the transport call: the
// method name (`.post` → POST) or, for `.ajax`/`.request`, the
// `method`/`type` key of an options object argument. Defaults to GET.
func jpxPropURLCallVerb(call *sitter.Node, src []byte) string {
	if callee := call.ChildByFieldName("function"); callee != nil && callee.Type() == "member_expression" {
		if pr := callee.ChildByFieldName("property"); pr != nil {
			if v := sulPropClientVerb(pr.Content(src)); v != "" {
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
				if vn := jsast.ObjectKeyValue(a, src, k); vn != nil && vn.Type() == "string" {
					return strings.ToUpper(strings.Trim(vn.Content(src), `"'`))
				}
			}
		}
	}
	return "GET"
}

// jpxEnclosingFnName returns the name of the nearest enclosing function,
// method, or arrow bound to a declarator/field — for the `calls` edge lookup.
func jpxEnclosingFnName(n *sitter.Node, src []byte) string {
	fn := jsast.EnclosingFunction(n)
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

// jpxParamIndex returns the positional index of the parameter named `name`.
func jpxParamIndex(fn *sitter.Node, name string, src []byte) (int, bool) {
	if fn == nil {
		return 0, false
	}
	params := fn.ChildByFieldName("parameters")
	if params == nil {
		if p := fn.ChildByFieldName("parameter"); p != nil && sulParamName(p, src) == name {
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
		if sulParamName(c, src) == name {
			return idx, true
		}
		idx++
	}
	return 0, false
}

// ── value resolution post-processing ────────────────────────────────────────
// Ported from internal/linker/valuegraph_adapter.go's vgPropProducers/
// vgPropReadSite/vgSchemaProducerFallback family — the mint-site policy over
// a resolved valuegraph.Value (per-producer failure, schema-asset fallback,
// query-string stripping) stays hand-written; only the join that produced the
// Value is generic (RC.1's valuegraphfacts.Resolve).

type jpxPropProducer struct {
	file  string
	line  int
	paths []string
	fail  string // "" when paths resolved; otherwise the ledger's kind

	schemaMeta map[string]string
}

func jpxPropProducers(v valuegraph.Value, kind string, resolver *schemaurl.Resolver, svc string) []jpxPropProducer {
	type site struct {
		file string
		line int
		text string
		alts []valuegraph.Value
	}
	var order []*site
	byKey := map[string]*site{}
	for _, alt := range v.Alternatives() {
		if alt.Src.Reason != kind {
			continue
		}
		key := fmt.Sprintf("%s\x00%d", alt.Src.File, alt.Src.Line)
		s := byKey[key]
		if s == nil {
			s = &site{file: alt.Src.File, line: alt.Src.Line, text: alt.Src.Text}
			byKey[key] = s
			order = append(order, s)
		}
		s.alts = append(s.alts, alt)
	}

	out := make([]jpxPropProducer, 0, len(order))
	for _, s := range order {
		out = append(out, jpxPropReadSite(s.file, s.line, s.text, s.alts, resolver, svc))
	}
	return out
}

func jpxPropReadSite(file string, line int, text string, alts []valuegraph.Value, resolver *schemaurl.Resolver, svc string) jpxPropProducer {
	p := jpxPropProducer{file: file, line: line}
	if jpxIsQuotedLiteral(text) {
		if u, ok := jpxResolveJSPropURL(text); ok {
			p.paths = []string{u}
		} else {
			p.fail = "literal"
		}
		return p
	}
	seen := map[string]bool{}
	for _, alt := range alts {
		strs, ok := alt.Strings(jpxVGPropMaxStrings)
		if !ok {
			failKind := jpxPropFailKind(alt)
			if failKind == "member_expression" || failKind == "builder_call" {
				if hit, hok := jpxSchemaProducerFallback(resolver, svc, file, line, text); hok {
					return jpxPropProducer{
						file: file, line: line,
						paths:      []string{hit.Path},
						schemaMeta: schemaurl.MintMeta(hit),
					}
				}
			}
			return jpxPropProducer{file: file, line: line, fail: failKind}
		}
		for _, s := range strs {
			if !jpxIsLocalURLPath(s) {
				return jpxPropProducer{file: file, line: line, fail: "identifier"}
			}
			if !seen[s] {
				seen[s] = true
				p.paths = append(p.paths, s)
			}
		}
	}
	if len(p.paths) == 0 {
		p.fail = "identifier"
	}
	return p
}

func jpxIsQuotedLiteral(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	switch t[0] {
	case '"', '\'', '`':
		return true
	}
	return false
}

func jpxPropFailKind(v valuegraph.Value) string {
	origins := v.Origins()
	if len(origins) == 0 {
		return "identifier"
	}
	switch r := origins[0].Reason; r {
	case valuegraph.ReasonCall:
		return "builder_call"
	case valuegraph.ReasonMember, valuegraph.ReasonUnsupported:
		return "member_expression"
	case valuegraph.ReasonNoBinding, valuegraph.ReasonParam:
		return "identifier"
	default:
		return r
	}
}

// jpxSchemaProducerFallback is MS.3: a crossed prop's producer value the
// value engine correctly left opaque may still be a read of a discovered
// data asset. It re-locates the producer expression by the crossing's own
// (file, line, text) provenance, reusing sulParseHostFile/exprAtLine
// (hub_schema_url_link.go) — the same line-window text match, not a new copy.
func jpxSchemaProducerFallback(resolver *schemaurl.Resolver, svc, file string, line int, text string) (schemaurl.Hit, bool) {
	if resolver == nil || text == "" {
		return schemaurl.Hit{}, false
	}
	jf := sulParseHostFile(file)
	if jf == nil {
		return schemaurl.Hit{}, false
	}
	expr := jf.exprAtLine(line, text)
	if expr == nil {
		return schemaurl.Hit{}, false
	}
	fn := jsast.EnclosingFunction(expr)
	hit, hok, _ := resolver.ResolveURLExpr(expr, fn, jf.src, svc)
	return hit, hok
}

// jpxResolveJSPropURL turns a JS string / template-literal source into a
// root-relative wildcard path: `"/app/lro/5"` → `/app/lro/5`,
// “ `/app/lro/${id}?study_id=${s}` “ → `/app/lro/*`. Anything not starting
// with a literal `/` fails. Ported from
// internal/linker/js_prop_url_helpers.go's resolveJSPropURL.
func jpxResolveJSPropURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != raw[len(raw)-1] {
		return "", false
	}
	var body string
	switch raw[0] {
	case '"', '\'':
		body = raw[1 : len(raw)-1]
	case '`':
		body = jpxReTemplateSubst.ReplaceAllString(raw[1:len(raw)-1], "*")
	default:
		return "", false
	}
	if !strings.HasPrefix(body, "/") {
		return "", false
	}
	if i := strings.IndexAny(body, "?#"); i >= 0 {
		body = body[:i]
	}
	segs := strings.Split(body, "/")
	for i, s := range segs {
		if strings.Contains(s, "*") {
			segs[i] = "*"
		}
	}
	body = strings.TrimRight(strings.Join(segs, "/"), "/")
	if body == "" {
		return "", false
	}
	return body, true
}

var jpxReTemplateSubst = regexp.MustCompile(`\$\{[^{}]*\}`)

// jpxIsLocalURLPath reports whether p is a request path: "/"- or "*"-rooted,
// and carrying at least one literal (non-"*", non-"/") character.
func jpxIsLocalURLPath(p string) bool {
	if len(p) == 0 || (p[0] != '/' && p[0] != '*') {
		return false
	}
	return strings.ContainsFunc(p, func(r rune) bool { return r != '*' && r != '/' })
}
