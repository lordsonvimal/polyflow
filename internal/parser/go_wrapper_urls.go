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
				node, edge, ok := emitResolvedClient(service, file, pos, callerID, callee.Name(), path, method, "wrapper_url", seen)
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
	callerID, name, path, method, tag string,
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

// extractSprintfURLs synthesizes resolved http_client producers for request
// URLs built via fmt.Sprintf within the same function — the sibling shape to
// extractWrapperURLs' parameter-forwarding case (Tier X.11, docs/sprintf-url-
// resolution-plan.md). No wrapper closure is needed: this is a single-hop,
// intra-function pattern —
//
//	reqURL := fmt.Sprintf("%s/client_api/v1/folders/details_by_path?path=%s", c.baseURL, ...)
//	http.NewRequest("GET", reqURL, nil)
//
// — where SSA substitutes reqURL's use with the *ssa.Call value for Sprintf
// directly (no Alloc/Load indirection for a non-address-taken local), the same
// substitution X.7's `base + param` BinOp case already relies on.
func extractSprintfURLs(
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
				sprintfCall, ok := sprintfCallOf(urlArg)
				if !ok || len(sprintfCall.Call.Args) == 0 {
					continue
				}
				format, ok := ssaConstString(sprintfCall.Call.Args[0])
				if !ok {
					// Dynamic format string — not observed in the measured corpus;
					// leave the call on the dynamic ledger rather than guess (#12).
					continue
				}
				path, ok := extractSprintfPathPrefix(format)
				if !ok {
					continue
				}
				composed := composeWrapperURL("*", path)
				method := ""
				if m, ok := ssaConstString(methodArg); ok {
					method = strings.ToUpper(m)
				}

				pos := fset.Position(instr.Pos())
				if !pos.IsValid() {
					pos = fset.Position(fn.Pos())
				}
				file := relPath(pos.Filename)
				node, edge, ok := emitResolvedClient(service, file, pos, callerID, fn.Name(), composed, method, "sprintf_url", seen)
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

// extractSprintfPathPrefix extracts the literal path segment from a Sprintf
// format string shaped "%s<literal-path>" or "%s<literal-path>?<query>" — the
// dominant Go idiom for `fmt.Sprintf("%s/some/path?q=%s", c.baseURL, val)".
// Only a leading two-byte verb (%s/%d/%v) is recognized, matching the measured
// corpus (the dynamic base is always the first substitution).
//
// If a verb appears in the path itself before any '?' (a dynamic segment
// *inside* the path, e.g. "%s/v1/%s/detail"), this bails with ok=false rather
// than truncating to a misleading partial path — out of scope per the plan's
// non-goals; the call stays on the dynamic ledger (#12, don't guess).
func extractSprintfPathPrefix(format string) (string, bool) {
	if len(format) < 2 || format[0] != '%' {
		return "", false
	}
	switch format[1] {
	case 's', 'd', 'v':
	default:
		return "", false
	}
	rest := format[2:]
	if rest == "" {
		return "", false
	}
	qIdx := strings.IndexByte(rest, '?')
	vIdx := strings.IndexByte(rest, '%')
	if vIdx >= 0 && (qIdx < 0 || vIdx < qIdx) {
		return "", false
	}
	path := rest
	if qIdx >= 0 {
		path = rest[:qIdx]
	}
	if path == "" {
		return "", false
	}
	return path, true
}

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
