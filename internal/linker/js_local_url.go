package linker

import (
	"strconv"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/jsast"
	"github.com/lordsonvimal/polyflow/internal/schemaurl"
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
// This tier reads those sites. It resolves every assignment rather than one,
// and mints one http_client per distinct resolved URL rather than one client
// with several outbound edges — see the mint sites in ResolveJSLocalURLs and
// LinkJSPropClients. The fan-out invariant (no *node* reaches more than one
// handler) is preserved exactly; what changes is how many nodes a site owes.

// maxLocalURLBranches caps how many distinct URLs one local binding may resolve
// to before the site is treated as a table or a loop rather than a branch.
// Cedar's widest genuine case is 4 (a four-arm switch selecting a search
// endpoint by entity kind); 8 leaves headroom without letting a generated
// lookup table mint eight clients.
const maxLocalURLBranches = 8

// localURLLineSlack bounds how far past an http_client node's own line the URL
// expression that produced it may start. The node's line is the call site's
// start (`ajaxStatus\n  .ajax(...)`, `$.ajax({`), and a wrapped call regularly
// puts its URL argument two or three lines down. Mirrors jsHostLineSlack, which
// exists for the same reason on the host path.
const localURLLineSlack = 6

// localURLFnTypes are the nodes that introduce a function scope. The backtrack
// is bounded to one of these: "the enclosing function, nothing wider" is what
// makes the read intraprocedural, and a wider search would resolve a sibling
// handler's `url` into this one.
var localURLFnTypes = map[string]bool{
	"function_declaration":           true,
	"function_expression":            true,
	"function":                       true,
	"generator_function":             true,
	"generator_function_declaration": true,
	"arrow_function":                 true,
	"method_definition":              true,
	"class_static_block":             true,
}

// Ledger kinds emitted when a recognised site's URL cannot be read. Every
// unresolvable site gets one: a site with neither a node nor a ledger row is
// the failure mode this tier exists to remove, not an acceptable outcome.
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

// ── the mutating pass over matcher-minted clients ───────────────────────────

// ResolveJSLocalURLs reads the URL of every JS/TS http_client the pattern
// matcher left dynamic, by backtracking its URL expression through
// ResolveLocalURLBinding.
//
// A site that resolves to one path has that path written onto its existing
// node, in place. A site that resolves to several gets its existing node
// rewritten to the first path and one *additional* node per remaining path,
// each carrying its own branch_index — never one node with several outbound
// edges. Rewriting in place rather than appending a replacement is required by
// the mutating-pass rule: two nodes at one site would give the contract engine
// two producers to match and manufacture the fan-out this tier is careful not
// to create.
//
// Returns the mutated nodes (also mutated in place in the passed slice), the
// additional branch nodes, and one ledger row per site that could not be read.
// Must run before the contract engine, and before Tier CB, so a recovered path
// still gets its host and base-URL treatment.
func ResolveJSLocalURLs(nodes []graph.Node, sr *schemaurl.Resolver) (changed, added []graph.Node, ledger []graph.UnresolvedRef) {
	fileCache := make(map[string]*jsHostFile)
	for i := range nodes {
		n := &nodes[i]
		raw, wanted := localURLCandidate(n)
		if !wanted {
			continue
		}
		jf, cached := fileCache[n.File]
		if !cached {
			jf = parseJSHostFile(n.File)
			fileCache[n.File] = jf
		}
		if jf == nil {
			continue
		}
		expr := jf.exprAtLine(n.Line, raw)
		if expr == nil {
			ledger = append(ledger, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: localURLLedgerName(raw), Kind: ledgerLocalURLUnresolved,
			})
			continue
		}
		fn := enclosingJSFunction(expr)
		paths, reason, ok := resolveLocalURLBinding(expr, fn, jf.src)
		if !ok || len(paths) == 0 {
			// Tier MS.1/MS.2: the binding is not a literal path but a read of a
			// discovered data asset (`schema.create_url`, `getCreateURL(schema)`).
			hit, hok, hkind := sr.ResolveURLExpr(expr, fn, jf.src, n.Service)
			verb := strings.ToUpper(n.Meta["method"])
			if hok && verb != "" {
				schemaurl.ApplyURL(n, hit, verb)
				changed = append(changed, *n)
				continue
			}
			if hok || hkind != "" {
				k := hkind
				if k == "" {
					k = schemaurl.LedgerSchemaEntityUnresolved // resolved but no verb
				}
				ledger = append(ledger, graph.UnresolvedRef{
					Service: n.Service, File: n.File, Line: n.Line,
					Name: localURLLedgerName(raw), Kind: k,
				})
				continue
			}
			if reason == "" {
				reason = ledgerLocalURLUnresolved
			}
			ledger = append(ledger, graph.UnresolvedRef{
				Service: n.Service, File: n.File, Line: n.Line,
				Name: localURLLedgerName(raw), Kind: reason,
			})
			continue
		}

		applyLocalURL(n, paths[0], 0)
		n.Label = localURLLabel(n, paths[0])
		changed = append(changed, *n)
		for bi := 1; bi < len(paths); bi++ {
			branch := *n
			branch.ID = localURLBranchID(n.ID, bi)
			branch.Meta = cloneMeta(n.Meta)
			applyLocalURL(&branch, paths[bi], bi)
			branch.Label = localURLLabel(n, paths[bi])
			added = append(added, branch)
		}
	}
	return changed, added, ledger
}

// localURLCandidate reports whether n is a JS/TS http_client whose URL the
// matcher could not read, and returns the raw source text of the expression it
// gave up on. Nav links are excluded: they are a separate concern with their
// own tier, and their "unreadable" URLs are overwhelmingly `href="#"` rather
// than a binding to resolve.
func localURLCandidate(n *graph.Node) (raw string, ok bool) {
	if n.Type != graph.NodeTypeHTTPClient || n.File == "" {
		return "", false
	}
	if n.Language != "javascript" && n.Language != "typescript" {
		return "", false
	}
	if n.Meta["nav_link"] != "" || n.Meta["key_dynamic"] != "true" {
		return "", false
	}
	if n.Meta["url"] != "" || n.Meta["path"] != "" {
		return "", false // already readable — nothing owed
	}
	if n.Meta["url_origin"] == localURLOriginLocalBinding {
		return "", false // idempotent re-run
	}
	raw = n.Meta["key_dynamic_raw"]
	if raw == "" || raw == "(attached)" {
		return "", false
	}
	return raw, true
}

// applyLocalURL writes a resolved path onto a node and retires the dynamic
// markers, so the contract engine sees an ordinary readable client and the
// ledger check does not double-count a site that has since been read.
func applyLocalURL(n *graph.Node, path string, branch int) {
	n.Meta = ensureMeta(n.Meta)
	n.Meta["url"] = path
	n.Meta["url_origin"] = localURLOriginLocalBinding
	n.Meta["branch_index"] = strconv.Itoa(branch)
	// SA.1 provenance: which layer and rule read this URL. Carried on the node
	// so a reviewer — and writeEdges, which propagates it onto the http_call
	// edge — can see what minted the flow.
	n.Meta["vg_layer"] = vgLayer
	n.Meta["vg_rule"] = vgLocalBindingRule
	delete(n.Meta, "key_dynamic")
	delete(n.Meta, "key_dynamic_raw")
	delete(n.Meta, "key_candidates")
}

// localURLLabel keeps the node's existing verb prefix if it has one, so a
// relabelled client still reads "GET /api/clients".
func localURLLabel(n *graph.Node, path string) string {
	if v := n.Meta["method"]; v != "" {
		return strings.ToUpper(v) + " " + path
	}
	return path
}

// localURLBranchID suffixes the branch index onto the origin node's ID. The
// suffix is required, not cosmetic: without it every branch collides on one ID
// and the writer's dedup silently keeps one flow of the several.
func localURLBranchID(id string, branch int) string {
	return id + ":ul" + strconv.Itoa(branch)
}

// localURLLedgerName names the site by the raw URL expression the matcher gave
// up on, verbatim.
//
// Verbatim matters more than readable here. The config-resolve provider
// re-derives its own ledger from the persisted nodes and emits a
// `config_not_found` row for each of these same sites, keyed on
// (service, file, name, line) with the raw text as the name; the evidence
// reconciler then replaces the static row with the provider's. Truncating the
// name breaks that key, and the site ends up listed twice — once as
// local_url_unresolved and once as config_not_found — saying the same thing in
// two registers. Every site this pass declines is covered either way, which is
// what the no-silent-site rule actually requires.
func localURLLedgerName(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "(dynamic)"
	}
	return raw
}

// exprAtLine finds the expression whose source text is exactly raw, starting
// within a small window at or after line. Matching on the recorded text rather
// than re-deriving which argument was the URL keeps this pass agreed with the
// matcher about which node it is talking about, whatever pattern produced it.
func (jf *jsHostFile) exprAtLine(line int, raw string) *sitter.Node {
	var found *sitter.Node
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if found != nil || n == nil {
			return
		}
		row := int(n.StartPoint().Row) + 1
		if row > line+localURLLineSlack {
			return
		}
		if row >= line && n.Content(jf.src) == raw {
			found = n
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(jf.root)
	return found
}

func cloneMeta(m map[string]string) map[string]string {
	out := make(map[string]string, len(m)+2)
	for k, v := range m {
		out[k] = v
	}
	return out
}
