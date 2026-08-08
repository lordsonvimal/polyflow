package parser

import (
	"fmt"
	"go/constant"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier X.7 — interprocedural wrapper URL propagation.
//
// The dominant real cross-service client shape in typed Go API clients wraps the
// request construction behind a helper whose URL is a *parameter*:
//
//	func (c *Client) RegisterApp(app any) error {
//	    return c.doWithRetry(http.MethodPost, "/api/v1/service/apps/register", app)
//	}
//	func (c *Client) doWithRetry(method, path string, body any) error {
//	    req, _ := http.NewRequest(method, c.baseURL+path, nil)   // ← real http_client
//	    ...
//	}
//
// The tree-sitter matcher mints the http_client node at the `http.NewRequest`
// line, but its URL reconstructs to `*`+`*` (both `c.baseURL` and `path` are
// non-literal there), so the concrete `/api/v1/service/apps/register` at the
// call site never reaches a node and every client method is invisible
// cross-service. This pass closes that one indirection using the SSA program the
// semantic analyzer already builds: it finds request constructors whose URL is a
// wrapper parameter, then resolves each call site's literal argument and
// synthesizes one resolved http_client producer per caller (bug-class #1
// fan-out). The original param-dynamic matcher node is left in place as the
// honest ledger for callers that pass a non-literal path — nothing is replaced.
//
// Scope: `net/http.NewRequest` / `NewRequestWithContext` with a `path` (or
// `base+path`) parameter, resolved across exactly one call boundary. Sprintf-
// composed URLs and resty/other-client wrappers are follow-ups.

// wrapperInfo records a function whose request-URL derives from one of its own
// parameters.
type wrapperInfo struct {
	fn            *ssa.Function
	urlParamIndex int    // SSA param index (receiver-inclusive) carrying the path
	base          string // static prefix from the non-param side: "" or a literal, or "*" when dynamic
	methodIndex   int    // SSA param index carrying the method, or -1
	methodConst   string // wrapper-hardcoded method literal, when methodIndex < 0
}

// extractWrapperURLs synthesizes resolved http_client producers for wrapper call
// sites. It is deterministic: wrappers are gathered as a transitive closure over
// a name-sorted function list, and the emitted nodes/edges are sorted by ID
// before return so Go map iteration never reaches output (bug-class #2).
func extractWrapperURLs(
	service, dir string,
	fset *token.FileSet,
	inService map[*ssa.Function]bool,
	resolveFunc func(*ssa.Function) (string, bool),
) ([]graph.Node, []graph.Edge) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	relPath := func(abs string) string {
		if rel, err := filepath.Rel(canonicalPath(cwd), canonicalPath(abs)); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
		return abs
	}

	byFn := findURLParamWrappers(inService, fset)
	if len(byFn) == 0 {
		return nil, nil
	}

	// Pass 2: resolve each call site's literal argument into a producer.
	var nodes []graph.Node
	var edges []graph.Edge
	seen := map[string]bool{}
	for caller := range inService {
		callerID, ok := resolveFunc(caller)
		if !ok {
			continue
		}
		for _, b := range caller.Blocks {
			for _, instr := range b.Instrs {
				ci, ok := instr.(ssa.CallInstruction)
				if !ok {
					continue
				}
				common := ci.Common()
				if common.IsInvoke() {
					continue // interface dispatch: the concrete wrapper is unknown
				}
				callee, ok := common.Value.(*ssa.Function)
				if !ok {
					continue
				}
				w, ok := byFn[callee]
				if !ok {
					continue
				}
				if w.urlParamIndex >= len(common.Args) {
					continue
				}
				lit, ok := ssaConstString(common.Args[w.urlParamIndex])
				if !ok {
					// Non-literal path — the wrapper's own dynamic node already
					// ledgers this call (X.1 key_dynamic); skip silently.
					continue
				}
				path := composeWrapperURL(w.base, lit)
				method := w.methodConst
				if w.methodIndex >= 0 && w.methodIndex < len(common.Args) {
					if m, ok := ssaConstString(common.Args[w.methodIndex]); ok {
						method = strings.ToUpper(m)
					} else {
						method = "" // dynamic method → engine's method_fallback tries verbs
					}
				}

				pos := fset.Position(instr.Pos())
				if !pos.IsValid() {
					pos = fset.Position(caller.Pos())
				}
				file := relPath(pos.Filename)
				// Graded the same way as the sprintf/concat path: a wrapper
				// called with "/health" onto an opaque base is the same
				// convention-not-a-route problem, and grading it here is what
				// keeps the two synthesis paths from disagreeing.
				node, edge, ok := emitResolvedClient(service, file, pos, callerID, callee.Name(), path, method, "wrapper_url", pathEvidence(path), seen)
				if !ok {
					continue
				}
				nodes = append(nodes, node)
				edges = append(edges, edge)
			}
		}
	}

	// Determinism: sort both slices by ID (bug-class #2).
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return nodes, edges
}

// emitResolvedClient builds the node/edge pair shared by every "resolve a
// dynamic client call to a literal path" extractor (X.7 wrapper params, X.11
// Sprintf-composed URLs): a synthesized http_client producer plus a calls edge
// from the resolved caller. tag distinguishes provenance in Meta["synthesized"]
// (downstream consumers key off ConfidenceInferred / key_dynamic, not this exact
// string, so per-mechanism tags stay honest without breaking anything).
// Applies the X.9 test-file guard and X.7's dedup-by-ID rule (bug-class #1); ok
// is false when either causes the call site to be skipped.
func emitResolvedClient(
	service, file string, pos token.Position,
	callerID, name, path, method, tag, evidence string,
	seen map[string]bool,
) (graph.Node, graph.Edge, bool) {
	// X.9: a resolved call site inside a _test.go file is test scaffolding
	// (httptest request builders, fixture helpers), not a real service endpoint —
	// see extractWrapperURLs' original comment for the measured false-positive
	// rate this guards against.
	if graph.IsTestFilePath(file) {
		return graph.Node{}, graph.Edge{}, false
	}
	id := fmt.Sprintf("%s:%s:%s:%s:%d", service, file, graph.NodeTypeHTTPClient, name, pos.Line)
	if seen[id] {
		return graph.Node{}, graph.Edge{}, false
	}
	seen[id] = true

	label := path
	if method != "" {
		label = method + " " + path
	}
	meta := map[string]string{
		"path":           path,
		"via_wrapper":    name,
		"url_confidence": graph.ConfidenceInferred,
		"synthesized":    tag,
	}
	if method != "" {
		meta["method"] = method
	}
	if evidence == pathEvidenceWeak {
		// One literal segment behind an opaque host. Whether that names a route
		// or a convention is not decidable here — the contract engine settles it
		// by counting the services that answer to the path.
		meta["path_evidence"] = pathEvidenceWeak
		// Even when it resolves in exactly one service, one segment behind an
		// opaque host is thin: an outbound call to a third-party API
		// (`*/emails` → Resend, `*/payment_links`) collides with a workspace
		// route on the segment alone. Capping at `partial` keeps such an edge
		// visible without letting a spec-only match promote it to `verified`;
		// runtime or config evidence, which pins the real host, still can.
		meta["confidence_ceiling"] = graph.ConfidencePartial
	}
	node := graph.Node{
		ID:       id,
		Type:     graph.NodeTypeHTTPClient,
		Label:    label,
		Service:  service,
		File:     file,
		Line:     pos.Line,
		Language: "go",
		Meta:     meta,
	}
	edge := graph.Edge{
		ID:         fmt.Sprintf("wrapperurl:%s:%s->%s", graph.EdgeTypeCalls, callerID, id),
		From:       callerID,
		To:         id,
		Type:       graph.EdgeTypeCalls,
		Confidence: graph.ConfidenceStatic,
		Meta:       map[string]string{"via": tag},
	}
	return node, edge, true
}

// extractComposedURLs synthesizes resolved http_client producers for request
// URLs composed inside the calling function — the sibling shape to
// extractWrapperURLs' parameter-forwarding case (Tier X.11, docs/sprintf-url-
// resolution-plan.md; extended by Tier K.1, docs/rails-asset-erb-coverage-plan.md).
// No wrapper closure is needed: this is a single-hop, intra-function pattern —
//
//	reqURL := fmt.Sprintf("%s/client_api/v1/folders/details_by_path?path=%s", c.baseURL, ...)
//	http.NewRequest("GET", reqURL, nil)
//
// — where SSA substitutes reqURL's use with the *ssa.Call value for Sprintf
// directly (no Alloc/Load indirection for a non-address-taken local), the same
// substitution X.7's `base + param` BinOp case already relies on.
//
// Tier K.1 generalizes the URL argument from "is an fmt.Sprintf call" to "is any
// composition of literals, concatenations and Sprintf calls" (resolveComposedURL),
// because the other half of the measured DSW→nextGen client surface builds its URL
// by concatenating a constructor-injected host field with a literal:
//
//	http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/client_api/v1/users/", body)
//
// The host is opaque either way — it is a struct field assigned from a `New*`
// parameter — so it renders as `*` and `dynamic_host_strip` drops it at match
// time. The evidence that makes the edge honest is the *path*, which is fully
// static at the call site. A bare literal URL argument is deliberately left to the
// tree-sitter matcher that already owns it, and a composition that yields no
// literal path segment is skipped rather than guessed (bug-class #12).
func extractComposedURLs(
	service, dir string,
	fset *token.FileSet,
	inService map[*ssa.Function]bool,
	resolveFunc func(*ssa.Function) (string, bool),
) ([]graph.Node, []graph.Edge) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	relPath := func(abs string) string {
		if rel, err := filepath.Rel(canonicalPath(cwd), canonicalPath(abs)); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
		return abs
	}

	// Deterministic function order (bug-class #2), same key as findURLParamWrappers.
	fns := make([]*ssa.Function, 0, len(inService))
	for fn := range inService {
		fns = append(fns, fn)
	}
	sort.Slice(fns, func(i, j int) bool {
		pi, pj := fset.Position(fns[i].Pos()), fset.Position(fns[j].Pos())
		if pi.Filename != pj.Filename {
			return pi.Filename < pj.Filename
		}
		if pi.Line != pj.Line {
			return pi.Line < pj.Line
		}
		return fns[i].Name() < fns[j].Name()
	})

	var nodes []graph.Node
	var edges []graph.Edge
	seen := map[string]bool{}
	for _, fn := range fns {
		callerID, ok := resolveFunc(fn)
		if !ok {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				ci, ok := instr.(ssa.CallInstruction)
				if !ok {
					continue
				}
				common := ci.Common()
				if common.IsInvoke() {
					continue
				}
				methodArg, urlArg, ok := httpRequestArgs(common)
				if !ok {
					continue
				}
				tag, ok := composedURLTag(urlArg)
				if !ok {
					// Bare literal (the tree-sitter matcher owns it) or a shape
					// this pass cannot compose — nothing to add.
					continue
				}
				composed, evidence, ok := composedRequestPath(urlArg)
				if !ok {
					// No literal path segment survived: zero evidence, so the call
					// stays on the dynamic ledger rather than fanning out (#12).
					continue
				}
				method := ""
				if m, ok := ssaConstString(methodArg); ok {
					method = strings.ToUpper(m)
				}

				pos := fset.Position(instr.Pos())
				if !pos.IsValid() {
					pos = fset.Position(fn.Pos())
				}
				file := relPath(pos.Filename)
				node, edge, ok := emitResolvedClient(service, file, pos, callerID, fn.Name(), composed, method, tag, evidence, seen)
				if !ok {
					continue
				}
				nodes = append(nodes, node)
				edges = append(edges, edge)
			}
		}
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return nodes, edges
}

// sprintfCallOf reports whether v (after stripping ChangeType/Convert noise) is
// itself the SSA call value for fmt.Sprintf(...), returning that call.
func sprintfCallOf(v ssa.Value) (*ssa.Call, bool) {
	call, ok := ssaUnwrap(v).(*ssa.Call)
	if !ok {
		return nil, false
	}
	common := call.Common()
	if common.IsInvoke() {
		return nil, false
	}
	fn, ok := common.Value.(*ssa.Function)
	if !ok || fn.Pkg == nil || fn.Pkg.Pkg == nil {
		return nil, false
	}
	if fn.Pkg.Pkg.Path() != "fmt" || fn.Name() != "Sprintf" {
		return nil, false
	}
	return call, true
}

// urlComposeMaxDepth caps resolveComposedURL's recursion. Request URLs in the
// measured corpus nest two levels at most (`host + Sprintf(...)`); the cap only
// exists so a pathological expression tree cannot stall the parser.
const urlComposeMaxDepth = 8

// composedURLTag classifies the request-URL argument and reports whether this
// pass should own it. A bare string constant is excluded: the tree-sitter HTTP
// matcher already mints a fully-resolved node for literal URLs, and emitting a
// second one here would double-count the call site.
func composedURLTag(v ssa.Value) (string, bool) {
	v = ssaUnwrap(v)
	if _, isConst := ssaConstString(v); isConst {
		return "", false
	}
	if _, isSprintf := sprintfCallOf(v); isSprintf {
		return "sprintf_url", true
	}
	if bin, ok := v.(*ssa.BinOp); ok && bin.Op == token.ADD {
		return "concat_url", true
	}
	return "", false
}

// composedRequestPath renders a request-URL argument into the match-ready path
// stored on the synthesized producer, or ok=false when the composition carries no
// static evidence.
//
// The query string is dropped here rather than left to the contract engine's
// `query_strip` normalizer so that the node's own label and Meta["path"] read as
// the endpoint a human would recognize.
func composedRequestPath(v ssa.Value) (string, string, bool) {
	pattern := collapseWildcards(resolveComposedURL(v, 0))
	if i := strings.IndexByte(pattern, '?'); i >= 0 {
		pattern = pattern[:i]
	}
	if hasLiteralAuthority(pattern) {
		// The call names its host outright, so this pass — which exists to recover
		// paths hidden behind an *opaque* host — has nothing to add, and the
		// tree-sitter matcher already holds the literal prefix. Emitting anyway is
		// actively harmful: `fmt.Sprintf("https://api.github.com/users/%s", u)`
		// reduces to `/users/*` once url_to_path drops the authority, which then
		// matches unrelated `users` member routes in workspace services.
		return "", "", false
	}
	evidence := pathEvidence(pattern)
	if evidence == pathEvidenceNone {
		return "", "", false
	}
	return pattern, evidence, true
}

// hasLiteralAuthority reports whether the pattern names a concrete host, as
// opposed to composing onto one it could not resolve (`*/api/x`, `http://*/api/x`).
func hasLiteralAuthority(pattern string) bool {
	i := strings.Index(pattern, "://")
	if i < 0 {
		return false
	}
	authority := pattern[i+3:]
	if j := strings.IndexByte(authority, '/'); j >= 0 {
		authority = authority[:j]
	}
	return authority != "" && !strings.Contains(authority, "*")
}

// resolveComposedURL renders an SSA string value into a URL pattern, substituting
// `*` for every part it cannot prove. Concatenation and fmt.Sprintf are the two
// composition forms that appear in Go request construction; anything else (a field
// load, a helper call, a parameter) is opaque and becomes `*`, which
// `dynamic_host_strip` / `param_wildcard` reduce at match time.
func resolveComposedURL(v ssa.Value, depth int) string {
	if depth > urlComposeMaxDepth {
		return "*"
	}
	v = ssaUnwrap(v)
	if s, ok := ssaConstString(v); ok {
		return s
	}
	switch t := v.(type) {
	case *ssa.BinOp:
		if t.Op == token.ADD {
			return resolveComposedURL(t.X, depth+1) + resolveComposedURL(t.Y, depth+1)
		}
	case *ssa.Call:
		if call, ok := sprintfCallOf(t); ok && len(call.Call.Args) > 0 {
			if format, ok := ssaConstString(call.Call.Args[0]); ok {
				return expandFormatVerbs(format)
			}
			// Dynamic format string — not observed in the measured corpus; fall
			// through to opaque rather than guess (#12).
		}
	}
	return "*"
}

// expandFormatVerbs rewrites every printf verb in a format string to `*`.
//
// The substituted arguments are deliberately *not* consulted. fmt.Sprintf is
// variadic, so SSA hands the arguments over as a constructed `[]any` slice whose
// element stores would have to be walked back; and across the measured corpus every
// substitution is a runtime value (a host field, an ID, a url.QueryEscape call)
// anyway. Wildcarding a substitution that happened to be constant only widens the
// pattern, which costs match precision on the wildcard tier but can never invent a
// path segment that is not there.
func expandFormatVerbs(format string) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			b.WriteByte(format[i])
			continue
		}
		j := i + 1
		// Skip flags, width and precision: `%-10.2f`, `%02d`.
		for j < len(format) && strings.IndexByte("+-# 0123456789.", format[j]) >= 0 {
			j++
		}
		if j >= len(format) {
			b.WriteByte('%')
			break
		}
		if format[j] == '%' { // `%%` is a literal percent, not a substitution.
			b.WriteByte('%')
		} else {
			b.WriteByte('*')
		}
		i = j
	}
	return b.String()
}

// collapseWildcards folds runs of `*` into one so `c.host + "/" + id` renders
// `*/*` rather than `*/**`, keeping patterns comparable segment by segment.
func collapseWildcards(s string) string {
	var b strings.Builder
	prevStar := false
	for i := 0; i < len(s); i++ {
		if s[i] == '*' {
			if prevStar {
				continue
			}
			prevStar = true
		} else {
			prevStar = false
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Path-evidence grading now lives in internal/graph (graph.PathEvidence), so
// that linker passes which rewrite Meta["path"] can re-grade what they wrote
// without importing the parser. These aliases keep the call sites here short.
const (
	pathEvidenceNone   = graph.PathEvidenceNone
	pathEvidenceWeak   = graph.PathEvidenceWeak
	pathEvidenceStrong = graph.PathEvidenceStrong
)

func pathEvidence(pattern string) string { return graph.PathEvidence(pattern) }

// findURLParamWrappers computes the transitive set of functions whose request
// URL derives from one of their own parameters. The seed is functions with a
// direct `net/http.NewRequest(...)` whose URL is `param` or `base+param`; the
// closure adds pass-through wrappers — a function that forwards its own
// parameter into a known wrapper's URL-parameter position (the real svc-c-mgr
// chain is RegisterApp → doWithRetry(method, path) → doRequest(method, path) →
// http.NewRequest, two hops). Iteration is over a name-sorted function slice to
// a fixpoint so the resulting set (and each entry's base/method) is
// order-independent; a round cap guards mutual recursion.
func findURLParamWrappers(inService map[*ssa.Function]bool, fset *token.FileSet) map[*ssa.Function]wrapperInfo {
	// Deterministic function order (bug-class #2): pointers can't be sorted, so
	// key on position + name.
	fns := make([]*ssa.Function, 0, len(inService))
	for fn := range inService {
		fns = append(fns, fn)
	}
	sort.Slice(fns, func(i, j int) bool {
		pi, pj := fset.Position(fns[i].Pos()), fset.Position(fns[j].Pos())
		if pi.Filename != pj.Filename {
			return pi.Filename < pj.Filename
		}
		if pi.Line != pj.Line {
			return pi.Line < pj.Line
		}
		return fns[i].Name() < fns[j].Name()
	})

	byFn := make(map[*ssa.Function]wrapperInfo)
	// Seed: direct request constructors.
	for _, fn := range fns {
		if info, ok := directURLParamWrapper(fn); ok {
			byFn[fn] = info
		}
	}
	// Closure: forward through pass-through wrappers.
	const maxRounds = 8
	for round := 0; round < maxRounds; round++ {
		changed := false
		for _, fn := range fns {
			if _, done := byFn[fn]; done {
				continue
			}
			if info, ok := forwardingWrapper(fn, byFn); ok {
				byFn[fn] = info
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return byFn
}

// forwardingWrapper reports whether fn forwards one of its own parameters into a
// known wrapper's URL-parameter slot, making fn itself a URL-param wrapper.
func forwardingWrapper(fn *ssa.Function, byFn map[*ssa.Function]wrapperInfo) (wrapperInfo, bool) {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			ci, ok := instr.(ssa.CallInstruction)
			if !ok {
				continue
			}
			common := ci.Common()
			if common.IsInvoke() {
				continue
			}
			callee, ok := common.Value.(*ssa.Function)
			if !ok {
				continue
			}
			g, ok := byFn[callee]
			if !ok || g.urlParamIndex >= len(common.Args) {
				continue
			}
			// fn must pass its own parameter (bare, base "") into g's URL slot.
			idx, base, ok := paramURL(common.Args[g.urlParamIndex], fn)
			if !ok || base != "" {
				continue // literal or composed forwards don't chain cleanly; skip
			}
			info := wrapperInfo{fn: fn, urlParamIndex: idx, base: g.base, methodIndex: -1, methodConst: g.methodConst}
			// Track the method through the same call, when g carries one.
			if g.methodIndex >= 0 && g.methodIndex < len(common.Args) {
				if mi, ok := paramIndex(common.Args[g.methodIndex], fn); ok {
					info.methodIndex = mi
				} else if mc, ok := ssaConstString(common.Args[g.methodIndex]); ok {
					info.methodConst = strings.ToUpper(mc)
				}
			}
			return info, true
		}
	}
	return wrapperInfo{}, false
}

// directURLParamWrapper inspects fn's body for a request constructor whose URL
// argument is one of fn's own parameters (directly, or as `base + param`).
// Returns the parameter index and any static base prefix.
func directURLParamWrapper(fn *ssa.Function) (wrapperInfo, bool) {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			ci, ok := instr.(ssa.CallInstruction)
			if !ok {
				continue
			}
			common := ci.Common()
			if common.IsInvoke() {
				continue
			}
			methodArg, urlArg, ok := httpRequestArgs(common)
			if !ok {
				continue
			}
			idx, base, ok := paramURL(urlArg, fn)
			if !ok {
				continue
			}
			info := wrapperInfo{fn: fn, urlParamIndex: idx, base: base, methodIndex: -1}
			// Resolve the method argument too: a parameter (propagate from caller)
			// or a wrapper-hardcoded literal. A dynamic non-param method leaves
			// methodIndex=-1/methodConst="" so the engine's method_fallback runs.
			if mi, ok := paramIndex(methodArg, fn); ok {
				info.methodIndex = mi
			} else if mc, ok := ssaConstString(methodArg); ok {
				info.methodConst = strings.ToUpper(mc)
			}
			return info, true
		}
	}
	return wrapperInfo{}, false
}

// httpRequestArgs returns the (method, url) argument values of a
// net/http.NewRequest / NewRequestWithContext call, or ok=false.
func httpRequestArgs(common *ssa.CallCommon) (methodArg, urlArg ssa.Value, ok bool) {
	fn, ok := common.Value.(*ssa.Function)
	if !ok || fn.Pkg == nil || fn.Pkg.Pkg == nil || fn.Pkg.Pkg.Path() != "net/http" {
		return nil, nil, false
	}
	switch fn.Name() {
	case "NewRequest": // (method, url, body)
		if len(common.Args) >= 2 {
			return common.Args[0], common.Args[1], true
		}
	case "NewRequestWithContext": // (ctx, method, url, body)
		if len(common.Args) >= 3 {
			return common.Args[1], common.Args[2], true
		}
	}
	return nil, nil, false
}

// paramURL reports whether v is a parameter of fn (base "") or `base + param`
// where base is a string literal ("...") or dynamic ("*"). It unwraps the
// ChangeType/Convert noise SSA emits around string values.
func paramURL(v ssa.Value, fn *ssa.Function) (idx int, base string, ok bool) {
	v = ssaUnwrap(v)
	if i, ok := paramIndex(v, fn); ok {
		return i, "", true
	}
	if bin, ok := v.(*ssa.BinOp); ok && bin.Op == token.ADD {
		x, y := ssaUnwrap(bin.X), ssaUnwrap(bin.Y)
		// `base + param`: param on the right, base on the left.
		if i, ok := paramIndex(y, fn); ok {
			return i, staticBase(x), true
		}
		// `param + suffix`: uncommon, but keep it symmetric.
		if i, ok := paramIndex(x, fn); ok {
			return i, staticBase(y), true
		}
	}
	return 0, "", false
}

// paramIndex returns the receiver-inclusive index of v within fn.Params, or ok=false.
func paramIndex(v ssa.Value, fn *ssa.Function) (int, bool) {
	p, ok := ssaUnwrap(v).(*ssa.Parameter)
	if !ok {
		return 0, false
	}
	for i, fp := range fn.Params {
		if fp == p {
			return i, true
		}
	}
	return 0, false
}

// staticBase renders the non-param side of a URL concat: a string literal
// contributes itself; anything else (a field load like c.baseURL) is dynamic,
// rendered "*" so the dynamic_host_strip normalizer drops it at match time.
func staticBase(v ssa.Value) string {
	if s, ok := ssaConstString(v); ok {
		return s
	}
	return "*"
}

// composeWrapperURL joins the wrapper's static base with the call site's literal
// path. A dynamic base ("*") is kept as a leading segment (dynamic_host_strip
// removes it during contract matching, aligning "*/api/x" with the handler's
// bare "/api/x").
func composeWrapperURL(base, lit string) string {
	switch base {
	case "":
		return lit
	default:
		return base + lit
	}
}

// ssaConstString returns the string value of a constant SSA value, unwrapping
// conversions. Named constants (e.g. http.MethodPost) are already inlined to
// *ssa.Const by SSA construction.
func ssaConstString(v ssa.Value) (string, bool) {
	c, ok := ssaUnwrap(v).(*ssa.Const)
	if !ok || c.Value == nil || c.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(c.Value), true
}

// ssaUnwrap strips ChangeType/Convert wrappers that surround string values,
// returning the underlying value.
func ssaUnwrap(v ssa.Value) ssa.Value {
	for {
		switch t := v.(type) {
		case *ssa.ChangeType:
			v = t.X
		case *ssa.Convert:
			v = t.X
		default:
			return v
		}
	}
}
