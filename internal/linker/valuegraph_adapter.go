package linker

import (
	"os"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"

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
)

// valuegraphEnabled reports whether the engine path is selected. Read from the
// environment on every call rather than cached: it is consulted once per
// candidate site, never in a hot loop, and a cached value would make the
// differential tests (which flip the flag with t.Setenv) unable to see both
// paths in one process.
func valuegraphEnabled() bool { return os.Getenv("PF_VALUEGRAPH") == "1" }

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
// the engine, applying the same path-shape gate localURLStaticPaths applies: a
// value that is not a "/"- or "*"-rooted path (a bare word like "done" or
// "html" carried on some branch) makes the site unreadable rather than
// contributing a non-URL string.
func resolveOneLocalURLExprVG(eng *valuegraph.Engine, n *sitter.Node, fn *sitter.Node, src []byte) (paths []string, reason string, ok bool) {
	if n == nil {
		return nil, ledgerLocalURLUnresolved, false
	}
	// Root == fn caps valuegraph's outward scope walk at the enclosing
	// function: outward() stops as soon as it reaches c.root, so a sibling or
	// module-scope binding of the same name is never read.
	v := eng.Resolve(valuegraph.Query{Src: src, Root: fn, Expr: n, Scope: fn})

	got, vok := v.Strings(0)
	if !vok {
		return nil, vgLedgerReason(v), false
	}
	for _, p := range got {
		if !isLocalURLPath(p) {
			return nil, ledgerLocalURLUnresolved, false
		}
	}
	if len(got) > maxLocalURLBranches {
		return nil, ledgerLocalURLHighFanout, false
	}
	return got, "", true
}

func isLocalURLPath(p string) bool {
	return len(p) > 0 && (p[0] == '/' || p[0] == '*')
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
