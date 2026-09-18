package linker

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/jsast"
)

// Tier UL — resolve URLs held in function-local bindings.
//
// The JS KeyWalker already backtracks a bare identifier to its binding
// (contract/keywalk_javascript_local.go, Tier C.1), but only when that binding
// is written exactly once in the scope that owns it. Two writes and it returns
// ambiguous, because a single value cannot be guessed from them. That guard is
// right about the value and wrong about the flow: a URL local assigned a
// different literal on each arm of an if/else or a switch is not ambiguity, it
// is several real requests written at one call site.
//
// The driver+emit loop that used to live here (ResolveJSLocalURLs) migrated
// to Tier FX (docs/js-declarative-composition-cluster-plan.md, RC.2) —
// patterns/javascript/js_local_url.yaml + rules/javascript/js_local_url.dl,
// driven by the "js_local_url" hub (internal/factpipe/hub_js_local_url.go).
// What's left here are the delegating wrappers and shared constants
// internal/linker/valuegraph_adapter.go (Tier UB.2/UB.3, MS.3 kind 1 — not
// yet migrated) still calls directly.

// maxLocalURLBranches caps how many distinct URLs one local binding may resolve
// to before the site is treated as a table or a loop rather than a branch.
// Cedar's widest genuine case is 4 (a four-arm switch selecting a search
// endpoint by entity kind); 8 leaves headroom without letting a generated
// lookup table mint eight clients.
const maxLocalURLBranches = 8

// Ledger kinds a caller of resolveLocalURLBinding should use when ok is false.
const (
	ledgerLocalURLUnresolved   = "local_url_unresolved"
	ledgerLocalURLHighFanout   = "local_url_high_fanout"
	localURLOriginLocalBinding = "local_binding"
)

// ResolveLocalURLBinding resolves the URL expression at a call site to the set
// of concrete paths it can take, by backtracking a bare identifier to its
// assignments within fn's subtree.
//
// Returns paths in source order, deduplicated. ok is false — and paths is nil —
// if ANY assignment fails to resolve, if the identifier is reassigned at module
// scope, or if the count exceeds maxLocalURLBranches. Partial resolution is
// deliberately not offered: a site that yields three of its four flows reads as
// complete and is worse than a ledger entry, which is what the caller must
// write when ok is false.
//
// urlExpr is the argument node as found at the call site; fn is the nearest
// enclosing function/method/arrow node. Both must come from the same parse.
func ResolveLocalURLBinding(urlExpr *sitter.Node, fn *sitter.Node, src []byte) (paths []string, ok bool) {
	paths, _, ok = resolveLocalURLBinding(urlExpr, fn, src)
	return paths, ok
}

// resolveLocalURLBinding is ResolveLocalURLBinding with the reason a site
// declined, so the caller can pick the right ledger kind. A cap breach and an
// unreadable right-hand side are different facts about the code and collapsing
// them would make the ledger unable to say which.
//
// The walkers that used to answer this were retired in VG.5; internal/jsast's
// ResolveLocalURLBinding (backed by the symbolic value engine,
// internal/valuegraph) is now the only implementation — moved there (Tier FX,
// schema_url_link + js_prop_clients migration) so internal/factpipe's hub
// providers can call it too. The signature is kept because several passes
// call it.
func resolveLocalURLBinding(urlExpr *sitter.Node, fn *sitter.Node, src []byte) (paths []string, reason string, ok bool) {
	return jsast.ResolveLocalURLBinding(urlExpr, fn, src)
}

// localURLUnwrapObject returns the expression that actually holds the URL: the
// url/path/href member of an options object, or the node itself. `{ url }`
// shorthand resolves to the shorthand identifier, which is the binding the
// backtrack then reads — the single commonest unread shape in the corpus.
func localURLUnwrapObject(n *sitter.Node, src []byte) *sitter.Node {
	if n == nil || n.Type() != "object" {
		return n
	}
	for _, k := range []string{"url", "path", "href"} {
		if v := jsObjectKeyValue(n, src, k); v != nil {
			return v
		}
	}
	return nil
}

// localURLIdentName delegates to internal/jsast.LocalIdentName (moved there,
// Tier FX schema_url_link + js_prop_clients migration, so factpipe's hub
// providers can call it too).
func localURLIdentName(n *sitter.Node, src []byte) string {
	return jsast.LocalIdentName(n, src)
}

// localURLAssignments delegates to internal/jsast.LocalAssignments.
func localURLAssignments(fn *sitter.Node, useStart uint32, src []byte, name string) []*sitter.Node {
	return jsast.LocalAssignments(fn, useStart, src, name)
}

// jsObjectKeyValue delegates to internal/jsast.ObjectKeyValue.
func jsObjectKeyValue(n *sitter.Node, src []byte, key string) *sitter.Node {
	return jsast.ObjectKeyValue(n, src, key)
}

// localURLModuleReassign delegates to internal/jsast.ModuleScopeReassign.
func localURLModuleReassign(fn *sitter.Node, src []byte, name string) bool {
	return jsast.ModuleScopeReassign(fn, src, name)
}

// enclosingJSFunction delegates to internal/jsast.EnclosingFunction.
func enclosingJSFunction(n *sitter.Node) *sitter.Node {
	return jsast.EnclosingFunction(n)
}
