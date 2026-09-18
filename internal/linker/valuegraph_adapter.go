package linker

import (
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/valuegraph"
)

// Tier VG — the JS URL resolvers on the symbolic value engine.
//
// docs/js-value-graph-pilot-plan.md and docs/js-value-graph-pilot-report.md.
// `resolveLocalURLBinding` keeps its exact signature and answers through
// `internal/valuegraph` rather than a hand-written backtracker. The signature
// was unchanged on purpose: that is what made VG.3's differential changes the
// vgdiff tool could verify against the walker, which VG.5 then retired.
//
// This file is the linker-side adapter for the remaining intraprocedural
// (VG.3) callers: the FileSource over the phase parse cache and the embedded
// spec. The VG.4 crossing-capable engine section this file used to also hold
// (jsPropFileSource/newJSPropScope/vgPropProducers and friends) migrated to
// internal/factpipe/hub_js_prop_crossings.go + internal/valuegraphfacts in
// Tier RC.3 (docs/js-declarative-composition-cluster-plan.md) — js_prop_urls.go
// and js_prop_transport.go, its only callers, are retired.

// vgLayer / vgLocalBindingRule are the SA.1 provenance stamped onto every
// http_client node the engine path mints or mutates
// (docs/static-architecture-target-plan.md). L2 is the value-resolution layer;
// the rule names the valuegraph spec concern that resolved the URL.
const (
	vgLayer            = "L2"
	vgLocalBindingRule = "valuegraph/javascript#local_binding"
	vgCallSiteRule     = "valuegraph/javascript#call_sites"
)

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

// resolveWrapperArgURL reads the URL argument of a call to a detected API
// wrapper (JP.2, js_wrapper_calls.go) when the KeyWalker could not.
//
// The relation this needs is the one `jsResolveForwardedParamURL` hand-wrote:
// `function load(url) { apiGet(url) }` is answered by `load("/api/x")` further
// down the file. It is the reverse crossing minus the crossing, and VG.6
// expresses it as the spec's `call_sites` rule — so what is left here is only
// the pass's policy, unchanged from the walker it replaces: one value or none.
//
// Root is the whole file, not the enclosing function: the call that fills the
// parameter is by definition outside the function that declares it.
func resolveWrapperArgURL(file string, src []byte, root, arg *sitter.Node) (string, bool) {
	if arg == nil || root == nil {
		return "", false
	}
	eng := valuegraph.New(jsValuegraphSpec(), jsEngineFileSource{}, valuegraph.Options{})
	v := eng.Resolve(valuegraph.Query{File: file, Src: src, Root: root, Expr: arg})
	if v.IsDynamic() {
		// A hole anywhere poisons the site. The walker abstained on any
		// non-literal argument at any caller; a wildcard segment minted from a
		// wrapper call would be a path this pass has never claimed to produce.
		return "", false
	}
	got, ok := v.Strings(2)
	if !ok || len(got) != 1 || !isWrapperURLPath(got[0]) {
		// Two callers with two URLs is a real fan-out, but this pass mints one
		// node per call site with one `url`. Widening that is a mint decision
		// nobody has taken; until then the site stays dynamic, exactly as it did.
		return "", false
	}
	return got[0], true
}

// isWrapperURLPath is the shape test the walker applied to a forwarded literal:
// a request path or an absolute URL, and nothing else. A bare word passed to a
// wrapper is a key, an id or a flag — not an endpoint.
func isWrapperURLPath(s string) bool {
	return strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
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
