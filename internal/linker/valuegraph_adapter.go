package linker

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/lordsonvimal/polyflow/internal/valuegraph"
)

// Tier VG.3 — the JS URL resolvers on the symbolic value engine.
//
// docs/js-value-graph-pilot-plan.md. `resolveLocalURLBinding` (and through it
// `js_prop_urls.go`, `js_prop_transport.go`, `js_prop_client.go`) keeps its
// exact signature and, when PF_VALUEGRAPH=1 is set, answers the same question
// through `internal/valuegraph` instead of the hand-written backtracker. The
// signatures are unchanged on purpose: that is what makes VG.3 a differential
// change the vgdiff tool can verify.
//
// This file is the linker-side adapter: the FileSource over the phase parse
// cache, the embedded spec, and the flag.

// vgLayer / vgLocalBindingRule are the SA.1 provenance stamped onto every
// http_client node the engine path mints or mutates
// (docs/static-architecture-target-plan.md). L2 is the value-resolution layer;
// the rule names the valuegraph spec concern that resolved the URL.
const (
	vgLayer            = "L2"
	vgLocalBindingRule = "valuegraph/javascript#local_binding"

	// The two crossing kinds declared in valuegraph/javascript.yaml, and the
	// rule names they are stamped with. The rule names the *crossing*, not the
	// language: which of the two mirror-image rules produced an edge is what
	// tells a reviewer where to look when one is suspect, and it is the reason
	// UB.2 and UB.3 were separate passes worth distinguishing.
	vgCrossPropURL       = "jsx_attribute"
	vgCrossPropTransport = "jsx_attribute_callback"
	vgPropURLRule        = "valuegraph/javascript#" + vgCrossPropURL
	vgPropTransportRule  = "valuegraph/javascript#" + vgCrossPropTransport

	// vgPropMaxStrings bounds the alternatives a crossed prop may enumerate
	// before the engine stops. It is deliberately wider than maxPropURLFanout:
	// the *cap* is the mint site's decision, and an engine that collapsed at 24
	// would make a 25-site component indistinguishable from an unreadable one.
	vgPropMaxStrings = 2 * maxPropURLFanout
)

// ValuegraphEnabled reports whether the engine path is selected. Read from the
// environment on every call rather than cached: it is consulted once per
// candidate site, never in a hot loop, and a cached value would make the
// differential tests (which flip the flag) unable to see both paths in one
// process. Exported so the indexer can gate the edge-provenance rebuild in
// writeEdges on the same flag.
func ValuegraphEnabled() bool { return os.Getenv("PF_VALUEGRAPH") == "1" }

var (
	jsVGSpecOnce sync.Once
	jsVGSpec     *valuegraph.Spec
)

// jsValuegraphSpec returns the embedded JavaScript/TypeScript binding spec,
// loaded once. A load failure yields a nil spec, which the engine treats as
// "resolve everything to Opaque" — the correct behaviour for a caller with no
// spec, and one the differential test would immediately flag as a wall of LOST
// rows.
func jsValuegraphSpec() *valuegraph.Spec {
	jsVGSpecOnce.Do(func() {
		if s, err := valuegraph.EmbeddedSpec("javascript"); err == nil {
			jsVGSpec = s
		}
	})
	return jsVGSpec
}

// jsEngineFileSource is a valuegraph.FileSource over the linker's per-phase
// JS/TS parse cache (js_tree_cache.go). VG.3 resolution is intraprocedural, so
// the engine is handed Src/Root directly and never reaches Parse; the type
// exists so the engine is constructed the same way it will be in VG.4, where
// the crossings index does walk files.
type jsEngineFileSource struct {
	files []string
}

func (s jsEngineFileSource) Files() []string { return s.files }

func (s jsEngineFileSource) Parse(file string) ([]byte, *sitter.Node, bool) {
	src, root, _, ok := jsParse(file)
	if !ok || root == nil {
		return nil, nil, false
	}
	return src, root, true
}

// ── VG.4: the crossing-capable engine ───────────────────────────────────────

// jsPropFileSource is jsEngineFileSource plus the two facts a crossing needs
// and no parse contains: which component a file defines, and which files define
// a component. Both come from the graph's own component index, which is where
// "which symbol is a component" is already decided — the engine never learns
// what a component is.
//
// Implementing valuegraph.CrossSource is what switches the spec's crossing
// rules on. The intraprocedural caller (VG.3) uses jsEngineFileSource, which
// does not implement it, so Tier UL cannot reach a crossing by accident.
type jsPropFileSource struct {
	files        []string
	ownersByFile map[string][]string
	filesByOwner map[string][]string
}

func (s *jsPropFileSource) Files() []string { return s.files }

func (s *jsPropFileSource) Parse(file string) ([]byte, *sitter.Node, bool) {
	src, root, _, ok := jsParse(file)
	if !ok || root == nil {
		return nil, nil, false
	}
	return src, root, true
}

func (s *jsPropFileSource) OwnersIn(file string) []string { return s.ownersByFile[file] }

func (s *jsPropFileSource) FilesForOwner(owner string) []string { return s.filesByOwner[owner] }

// jsPropScope is what both crossing passes need before they can ask anything:
// one engine per service, and the function-label index the `calls` edge is
// wired through.
type jsPropScope struct {
	fnBySvcLabel map[string]string
	engines      map[string]*valuegraph.Engine
}

// newJSPropScope builds the per-service engines. The file list, the test-file
// exclusion and the component index are exactly the ones the legacy producer
// scans used — recognition, which stays at the mint site; what moves is the
// resolution the scans existed to feed.
func newJSPropScope(nodes []graph.Node, serviceFiles map[string][]string) *jsPropScope {
	sc := &jsPropScope{
		fnBySvcLabel: map[string]string{},
		engines:      map[string]*valuegraph.Engine{},
	}

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
			if k := n.Service + "\x00" + n.Label; sc.fnBySvcLabel[k] == "" {
				sc.fnBySvcLabel[k] = n.ID
			}
		}
	}

	sources := map[string]*jsPropFileSource{}
	sourceFor := func(svc string) *jsPropFileSource {
		if s := sources[svc]; s != nil {
			return s
		}
		s := &jsPropFileSource{
			ownersByFile: map[string][]string{},
			filesByOwner: map[string][]string{},
		}
		sources[svc] = s
		return s
	}
	addOwner := func(svc, owner, file string) {
		if svc == "" || owner == "" || file == "" {
			return
		}
		s := sourceFor(svc)
		for _, have := range s.ownersByFile[file] {
			if have == owner {
				return
			}
		}
		s.ownersByFile[file] = append(s.ownersByFile[file], owner)
		s.filesByOwner[owner] = append(s.filesByOwner[owner], file)
	}
	for _, svc := range sortedBoolKeys(svcSet) {
		ci := newComponentIndex(nodes, svc)
		for sym, ids := range ci.bySymbol {
			for _, id := range ids {
				addOwner(svc, sym, nodeFile[id])
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
			addOwner(n.Service, l, n.File)
		}
	}

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
			svc := svcKey
			if svc == "" {
				svc = svcOfFile[rel]
			}
			s := sourceFor(svc)
			s.files = append(s.files, rel)
		}
	}

	spec := jsValuegraphSpec()
	for svc, s := range sources {
		sort.Strings(s.files)
		for _, files := range s.filesByOwner {
			sort.Strings(files)
		}
		sc.engines[svc] = valuegraph.New(spec, s, valuegraph.Options{
			// The union width is the engine's ceiling, not the pass's: the
			// fan-out cap is a mint decision and stays at the mint site.
			MaxUnionWidth: vgPropMaxStrings,
			// The crossing index is service-wide by construction, so the file
			// budget has to admit the whole service — a truncated index is a
			// silently missing producer, which is the one failure this tier
			// cannot afford.
			MaxFiles: len(s.files) + 8,
		})
	}
	return sc
}

// vgPropProducer is one producer site's contribution: the paths it resolved to,
// or the reason it could not be read. One site fails on its own — suppressing
// its siblings would hide a real render site behind the ones that resolved.
type vgPropProducer struct {
	file  string
	line  int
	paths []string
	fail  string // "" when paths resolved; otherwise the ledger's kind
}

// vgPropProducers splits a resolved value into what each producer site
// contributed, keeping only the sites the named crossing produced.
//
// Filtering by crossing kind is what keeps the two mirror passes from stealing
// each other's rows: a name that is both a prop and a callback parameter
// resolves through both rules, and each pass mints only what its own rule found.
//
// The alternatives are regrouped by site first. A producer whose value is a
// branch — a switch over four endpoints, a ternary — arrives as several
// alternatives that all came from one render site, and the shipped passes read
// such a site all-or-nothing: one unreadable arm poisons it, because three of
// four flows presented as complete is worse than a ledger row. That policy is
// the caller's and it is applied here, not in the engine.
func vgPropProducers(v valuegraph.Value, kind string) []vgPropProducer {
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

	out := make([]vgPropProducer, 0, len(order))
	for _, s := range order {
		out = append(out, vgPropReadSite(s.file, s.line, s.text, s.alts))
	}
	return out
}

// vgPropReadSite reads one producer site's alternatives as request paths.
//
// A producer *written* as a literal is read as a literal, by the same function
// that has always read one (resolveJSPropURL): it strips the query string a
// route never matches on, and it declines an interpolation-led template. A
// producer reached through a binding keeps the shape Tier UL gives it. The two
// have disagreed about query strings since UB.2 shipped; reproducing that
// disagreement is what makes this a differential change rather than a
// recall change wearing one's clothes. VG.5 reports it.
func vgPropReadSite(file string, line int, text string, alts []valuegraph.Value) vgPropProducer {
	p := vgPropProducer{file: file, line: line}
	if vgIsQuotedLiteral(text) {
		if u, ok := resolveJSPropURL(text); ok {
			p.paths = []string{u}
		} else {
			p.fail = "literal"
		}
		return p
	}
	seen := map[string]bool{}
	for _, alt := range alts {
		strs, ok := alt.Strings(vgPropMaxStrings)
		if !ok {
			return vgPropProducer{file: file, line: line, fail: vgPropFailKind(alt)}
		}
		for _, s := range strs {
			if !isLocalURLPath(s) {
				// A site whose value is not exclusively a request path — a
				// branch carrying null, a bare word — is not one this tier can
				// read, on any of its arms.
				return vgPropProducer{file: file, line: line, fail: "identifier"}
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

// vgIsQuotedLiteral reports whether a producer was written as a string or
// template literal, which is the one distinction resolveJSPropURL needs and the
// lattice deliberately does not carry.
func vgIsQuotedLiteral(text string) bool {
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

// vgPropFailKind names an unreadable producer in the ledger's existing
// vocabulary, so a reader of `prop_url_unresolved` still sees builder_call /
// member_expression / identifier. A reason with no legacy name is passed
// through verbatim: "depth" or "cycle" in the ledger is more useful than
// flattening it into "expression".
func vgPropFailKind(v valuegraph.Value) string {
	origins := v.Origins()
	if len(origins) == 0 {
		// Nothing opaque in it: the value resolved, to something that is not a
		// request path. That is the identifier case in the ledger's vocabulary.
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


// resolveLocalURLBindingVG is the engine-backed implementation of
// resolveLocalURLBinding. It keeps the legacy control flow exactly — unwrap the
// options object, abstain on a module-scope reassignment, walk the enclosing
// function's assignments in source order, poison the whole site if any one
// arm is unreadable — and swaps only the per-expression resolver:
// `localURLStaticPaths` (the JS KeyWalker) becomes `valuegraph.Engine.Resolve`.
//
// Keeping localURLAssignments (the which-nodes-bind-the-name walk) in Go is
// deliberate for VG.3: source order is load-bearing at the mint site
// (localURLBranchID indexes branches positionally), and the engine canonicalises
// union order. VG.4 revisits this once the crossing index lands.
func resolveLocalURLBindingVG(urlExpr *sitter.Node, fn *sitter.Node, src []byte) (paths []string, reason string, ok bool) {
	if urlExpr == nil || fn == nil {
		return nil, ledgerLocalURLUnresolved, false
	}
	expr := localURLUnwrapObject(urlExpr, src)
	if expr == nil {
		return nil, ledgerLocalURLUnresolved, false
	}

	eng := valuegraph.New(jsValuegraphSpec(), jsEngineFileSource{}, valuegraph.Options{
		// One distinct URL per branch; past this the site is a table or a
		// loop, not a branch — the same ceiling maxLocalURLBranches enforces
		// on the legacy path, expressed to the engine as its union width.
		MaxUnionWidth: maxLocalURLBranches,
	})

	name := localURLIdentName(expr, src)
	if name == "" {
		// Not a binding to backtrack: a literal, template or concatenation
		// written straight at the call site. Resolve it in place.
		return resolveOneLocalURLExprVG(eng, expr, fn, src)
	}

	// The module-scope guard is applied before any read of the name: Tier UL
	// is intraprocedural and abstains when the enclosing function does not own
	// the name. The engine, given Root == fn, will not walk out to module
	// scope anyway; the explicit guard also covers the "name reassigned at
	// module scope but written twice in the function" case.
	if localURLModuleReassign(fn, src, name) {
		return nil, ledgerLocalURLUnresolved, false
	}

	rhs := localURLAssignments(fn, expr.StartByte(), src, name)
	if len(rhs) == 0 {
		return nil, ledgerLocalURLUnresolved, false
	}

	var (
		out  []string
		seen = make(map[string]bool, len(rhs))
	)
	for _, r := range rhs {
		got, _, rok := resolveOneLocalURLExprVG(eng, r, fn, src)
		if !rok {
			// One unreadable arm poisons the whole site — three of four flows
			// presented as complete is worse than a ledger row.
			return nil, ledgerLocalURLUnresolved, false
		}
		for _, p := range got {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		return nil, ledgerLocalURLUnresolved, false
	}
	if len(out) > maxLocalURLBranches {
		return nil, ledgerLocalURLHighFanout, false
	}
	return out, "", true
}

// resolveOneLocalURLExprVG resolves a single expression to request paths via
// the engine.
//
// A ternary right-hand side is several real request paths in a definite source
// order (`cond ? "/a" : "/b"` is /a then /b). valuegraph's Union canonicalises
// order, and the mint site indexes branches positionally, so the ternary is
// split here and each arm resolved on its own — consequence before alternative
// — rather than handed to the engine whole.
//
// Two gates match the legacy JS KeyWalker's behaviour: a value that is not a
// "/"- or "*"-rooted path (a bare word like "done" carried on some branch), and
// a value with no literal segment at all ("*", "*/*"), both make the whole site
// unreadable. The KeyWalker abstains wholesale on a dynamic segment it cannot
// read; the engine resolves arm by arm and would otherwise leak a bare-wildcard
// "path" the route matcher treats as matching everything.
func resolveOneLocalURLExprVG(eng *valuegraph.Engine, n *sitter.Node, fn *sitter.Node, src []byte) (paths []string, reason string, ok bool) {
	if n == nil {
		return nil, ledgerLocalURLUnresolved, false
	}
	var (
		out  []string
		seen = make(map[string]bool)
	)
	for _, alt := range localURLExprAlternatives(n) {
		// Root == fn caps valuegraph's outward scope walk at the enclosing
		// function: outward() stops as soon as it reaches c.root, so a sibling
		// or module-scope binding of the same name is never read.
		v := eng.Resolve(valuegraph.Query{Src: src, Root: fn, Expr: alt, Scope: fn})
		got, vok := v.Strings(0)
		if !vok {
			return nil, vgLedgerReason(v), false
		}
		for _, p := range got {
			if !isLocalURLPath(p) {
				return nil, ledgerLocalURLUnresolved, false
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		return nil, ledgerLocalURLUnresolved, false
	}
	if len(out) > maxLocalURLBranches {
		return nil, ledgerLocalURLHighFanout, false
	}
	return out, "", true
}

// localURLExprAlternatives flattens a ternary (through parenthesised and
// TypeScript cast wrappers) into its alternative sub-expressions in source
// order. Anything that is not a ternary is returned unchanged as a
// single-element slice.
func localURLExprAlternatives(n *sitter.Node) []*sitter.Node {
	switch n.Type() {
	case "ternary_expression", "conditional_expression":
		var out []*sitter.Node
		for _, f := range []string{"consequence", "alternative"} {
			if c := n.ChildByFieldName(f); c != nil {
				out = append(out, localURLExprAlternatives(c)...)
			}
		}
		if len(out) > 0 {
			return out
		}
	case "parenthesized_expression":
		if n.NamedChildCount() == 1 {
			return localURLExprAlternatives(n.NamedChild(0))
		}
	case "as_expression", "satisfies_expression", "non_null_expression":
		if n.NamedChildCount() >= 1 {
			return localURLExprAlternatives(n.NamedChild(0))
		}
	}
	return []*sitter.Node{n}
}

// isLocalURLPath reports whether p is a request path the legacy KeyWalker would
// also have produced: "/"- or "*"-rooted, and carrying at least one literal
// (non-"*", non-"/") character. "*" and "*/*" fail the second test — the
// KeyWalker never emits them, it abstains on the whole expression instead.
func isLocalURLPath(p string) bool {
	if len(p) == 0 || (p[0] != '/' && p[0] != '*') {
		return false
	}
	return strings.ContainsFunc(p, func(r rune) bool { return r != '*' && r != '/' })
}

// vgLedgerReason maps an opaque resolution onto the ledger kind the caller
// expects. A union-width breach is a high-fanout site (its own kind, so the
// ledger can say "table or loop" rather than "unreadable"); everything else is
// the generic unresolved kind.
func vgLedgerReason(v valuegraph.Value) string {
	for _, o := range v.Origins() {
		if o.Reason == valuegraph.ReasonWidth {
			return ledgerLocalURLHighFanout
		}
	}
	return ledgerLocalURLUnresolved
}
