package jsast

import (
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/valuegraph"
)

// ResolveLocalURLBinding backtracks a URL expression to the set of concrete
// paths it can take, by resolving a bare identifier's assignments within fn's
// subtree through the symbolic value engine (internal/valuegraph).
//
// This is the same "Tier UL" resolver internal/linker/js_local_url.go's
// resolveLocalURLBinding delegates to (as resolveLocalURLBindingVG) — moved
// here so internal/factpipe's hub providers can call it too, without
// creating an import cycle through internal/linker -> internal/patterns ->
// internal/factpipe. internal/linker's copy now delegates to this one.
//
// Returns paths in source order, deduplicated. ok is false — and paths is
// nil — if ANY assignment fails to resolve, if the identifier is reassigned
// at module scope, or if the count exceeds MaxLocalURLBranches. reason names
// the ledger kind a caller should record when ok is false.
func ResolveLocalURLBinding(urlExpr *sitter.Node, fn *sitter.Node, src []byte) (paths []string, reason string, ok bool) {
	if urlExpr == nil || fn == nil {
		return nil, LedgerLocalURLUnresolved, false
	}
	expr := localURLUnwrapObject(urlExpr, src)
	if expr == nil {
		return nil, LedgerLocalURLUnresolved, false
	}

	eng := valuegraph.New(jsValuegraphSpec(), jsEngineFileSource{}, valuegraph.Options{
		// One distinct URL per branch; past this the site is a table or a
		// loop, not a branch.
		MaxUnionWidth: MaxLocalURLBranches,
	})

	name := LocalIdentName(expr, src)
	if name == "" {
		// Not a binding to backtrack: a literal, template or concatenation
		// written straight at the call site. Resolve it in place.
		return resolveOneLocalURLExpr(eng, expr, fn, src)
	}

	if ModuleScopeReassign(fn, src, name) {
		return nil, LedgerLocalURLUnresolved, false
	}

	rhs := LocalAssignments(fn, expr.StartByte(), src, name)
	if len(rhs) == 0 {
		return nil, LedgerLocalURLUnresolved, false
	}

	var (
		out  []string
		seen = make(map[string]bool, len(rhs))
	)
	for _, r := range rhs {
		got, _, rok := resolveOneLocalURLExpr(eng, r, fn, src)
		if !rok {
			return nil, LedgerLocalURLUnresolved, false
		}
		for _, p := range got {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		return nil, LedgerLocalURLUnresolved, false
	}
	if len(out) > MaxLocalURLBranches {
		return nil, LedgerLocalURLHighFanout, false
	}
	return out, "", true
}

// MaxLocalURLBranches caps how many distinct URLs one local binding may
// resolve to before the site is treated as a table or a loop rather than a
// branch.
const MaxLocalURLBranches = 8

// Ledger kinds a caller of ResolveLocalURLBinding should use when ok is
// false and reason is empty, or to recognise the two reasons this package
// itself returns.
const (
	LedgerLocalURLUnresolved = "local_url_unresolved"
	LedgerLocalURLHighFanout = "local_url_high_fanout"
)

func localURLUnwrapObject(n *sitter.Node, src []byte) *sitter.Node {
	if n == nil || n.Type() != "object" {
		return n
	}
	for _, k := range []string{"url", "path", "href"} {
		if v := ObjectKeyValue(n, src, k); v != nil {
			return v
		}
	}
	return nil
}

var (
	jsVGSpecOnce sync.Once
	jsVGSpec     *valuegraph.Spec
)

// jsValuegraphSpec returns the embedded JavaScript/TypeScript binding spec,
// loaded once. A load failure yields a nil spec, which the engine treats as
// "resolve everything to Opaque".
func jsValuegraphSpec() *valuegraph.Spec {
	jsVGSpecOnce.Do(func() {
		if s, err := valuegraph.EmbeddedSpec("javascript"); err == nil {
			jsVGSpec = s
		}
	})
	return jsVGSpec
}

// jsEngineFileSource is a valuegraph.FileSource over this package's JS/TS
// parse cache.
type jsEngineFileSource struct {
	files []string
}

func (s jsEngineFileSource) Files() []string { return s.files }

func (s jsEngineFileSource) Parse(file string) ([]byte, *sitter.Node, bool) {
	src, root, _, ok := Parse(file)
	if !ok || root == nil {
		return nil, nil, false
	}
	return src, root, true
}

// resolveOneLocalURLExpr resolves a single expression to request paths via
// the engine.
//
// A ternary right-hand side is several real request paths in a definite
// source order (`cond ? "/a" : "/b"` is /a then /b). valuegraph's Union
// canonicalises order, and a mint site typically indexes branches
// positionally, so the ternary is split here and each arm resolved on its
// own — consequence before alternative — rather than handed to the engine
// whole.
func resolveOneLocalURLExpr(eng *valuegraph.Engine, n *sitter.Node, fn *sitter.Node, src []byte) (paths []string, reason string, ok bool) {
	if n == nil {
		return nil, LedgerLocalURLUnresolved, false
	}
	var (
		out  []string
		seen = make(map[string]bool)
	)
	for _, alt := range localURLExprAlternatives(n) {
		v := eng.Resolve(valuegraph.Query{Src: src, Root: fn, Expr: alt, Scope: fn})
		got, vok := v.Strings(0)
		if !vok {
			return nil, vgLedgerReason(v), false
		}
		for _, p := range got {
			if !isLocalURLPath(p) {
				return nil, LedgerLocalURLUnresolved, false
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		return nil, LedgerLocalURLUnresolved, false
	}
	if len(out) > MaxLocalURLBranches {
		return nil, LedgerLocalURLHighFanout, false
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

// isLocalURLPath reports whether p is a request path: "/"- or "*"-rooted, and
// carrying at least one literal (non-"*", non-"/") character. "*" and "*/*"
// fail the second test — abstain on the whole expression instead of emitting
// a bare wildcard a route matcher would treat as matching everything.
func isLocalURLPath(p string) bool {
	if len(p) == 0 || (p[0] != '/' && p[0] != '*') {
		return false
	}
	return strings.ContainsFunc(p, func(r rune) bool { return r != '*' && r != '/' })
}

// vgLedgerReason maps an opaque resolution onto the ledger kind a caller
// expects. A union-width breach is a high-fanout site; everything else is the
// generic unresolved kind.
func vgLedgerReason(v valuegraph.Value) string {
	for _, o := range v.Origins() {
		if o.Reason == valuegraph.ReasonWidth {
			return LedgerLocalURLHighFanout
		}
	}
	return LedgerLocalURLUnresolved
}
