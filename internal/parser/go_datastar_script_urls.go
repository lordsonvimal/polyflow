package parser

import (
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier DS.1 — HTTP requests embedded in a client-side script string that a Go
// handler ships to the browser via Datastar's `sse.ExecuteScript(...)`.
//
// A handler that wants the browser to fire a follow-up request builds the call
// as JavaScript source text and hands it to ExecuteScript:
//
//	buildURL := fmt.Sprintf("/maple/exec-configs/%s/v/%d/do-build", cfg.ID, cfg.Version)
//	buildScript := fmt.Sprintf(`
//	    fetch('%s', { method: 'POST', headers: {...}, credentials: 'include' })
//	      .then(...)
//	`, buildURL, detailPageURL, detailPageURL)
//	sse.ExecuteScript(buildScript)
//
// The `fetch(...)` never appears in any .js/.templ file — it lives inside a Go
// string literal — so neither the JS parser nor the tree-sitter templ datastar
// pass sees it, and the handler reads as having no outbound call. This pass
// recovers it: it recognises the ExecuteScript sink, pulls the format string out
// of the `fmt.Sprintf` that built the script, scans it for `fetch(...)` (URL +
// method), and resolves a `%s`/`%d` URL placeholder back through the matching
// Sprintf vararg using the same resolveComposedURL machinery Tier X.11 uses for
// `http.NewRequest` URLs. The synthesized http_client producer is attributed to
// the enclosing handler and graded on its literal path evidence exactly like the
// other composed-URL producers.
//
// Scope: `*.ExecuteScript(script)` where the callee is datastar-go's method and
// `script` is either a plain string constant or a single `fmt.Sprintf(...)` whose
// format string is a literal. Anything more indirect is left opaque rather than
// guessed.

// reEmbeddedFetch matches a `fetch('<url>' ...` call in reconstructed JS source.
// The URL token is whatever sits between the opening quote and the next quote of
// the same kind — a literal path, a bare `%s`, or a literal/verb mix.
var reEmbeddedFetch = regexp.MustCompile("fetch\\(\\s*(['\"`])((?:[^'\"`\\\\]|\\\\.)*)['\"`]")

// reFetchMethod pulls `method: 'POST'` out of a fetch options object.
var reFetchMethod = regexp.MustCompile(`(?i)method\s*:\s*['"` + "`" + `]([A-Za-z]+)['"` + "`" + `]`)

func extractDatastarScriptURLs(
	service, dir string,
	fset *token.FileSet,
	inService map[*ssa.Function]bool,
	resolveFunc func(*ssa.Function) (string, bool),
) ([]graph.Node, []graph.Edge) {
	_ = dir
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

	// Deterministic function order (bug-class #2), same key as the sibling passes.
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
				if !isDatastarExecuteScript(common) {
					continue
				}
				scriptVal := datastarScriptArg(common)
				if scriptVal == nil {
					continue
				}
				format, argAt, ok := sprintfFormatAndArgs(scriptVal)
				if !ok {
					continue
				}
				pos := fset.Position(instr.Pos())
				if !pos.IsValid() {
					pos = fset.Position(fn.Pos())
				}
				file := relPath(pos.Filename)
				for _, req := range scanEmbeddedFetch(format, argAt) {
					path, evidence, ok := composedRequestPathFromString(req.url)
					if !ok {
						continue
					}
					node, edge, ok := emitResolvedClient(service, file, pos, callerID, fn.Name(), path, req.method, "datastar_script_fetch", evidence, seen)
					if !ok {
						continue
					}
					node.Meta["datastar"] = "true"
					nodes = append(nodes, node)
					edges = append(edges, edge)
				}
			}
		}
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return nodes, edges
}

// isDatastarExecuteScript reports whether common is a static call to datastar-go's
// ServerSentEventGenerator.ExecuteScript. The package-path guard keeps an
// unrelated same-named method from being swept in.
func isDatastarExecuteScript(common *ssa.CallCommon) bool {
	fn, ok := common.Value.(*ssa.Function)
	if !ok || fn.Name() != "ExecuteScript" {
		return false
	}
	if fn.Pkg == nil || fn.Pkg.Pkg == nil {
		return false
	}
	return strings.Contains(fn.Pkg.Pkg.Path(), "datastar")
}

// datastarScriptArg returns the `scriptContents string` argument of an
// ExecuteScript call. The method is `ExecuteScript(scriptContents string, opts
// ...ExecuteScriptOption)`, so with the receiver at Args[0] the script is Args[1];
// the trailing variadic slice is skipped. Falls back to the first string-typed
// argument for any non-method shape.
func datastarScriptArg(common *ssa.CallCommon) ssa.Value {
	args := common.Args
	if fn, ok := common.Value.(*ssa.Function); ok && fn.Signature != nil && fn.Signature.Recv() != nil {
		if len(args) >= 2 {
			return args[1]
		}
		return nil
	}
	for _, a := range args {
		if b, ok := a.Type().Underlying().(*types.Basic); ok && b.Kind() == types.String {
			return a
		}
	}
	return nil
}

// sprintfFormatAndArgs renders scriptVal into a format string plus a positional
// accessor over the fmt.Sprintf varargs. A plain string constant is returned with
// an accessor that always yields nil (no substitutions to resolve).
func sprintfFormatAndArgs(scriptVal ssa.Value) (string, func(int) ssa.Value, bool) {
	if s, ok := ssaConstString(scriptVal); ok {
		return s, func(int) ssa.Value { return nil }, true
	}
	call, ok := sprintfCallOf(scriptVal)
	if !ok || len(call.Call.Args) == 0 {
		return "", nil, false
	}
	format, ok := ssaConstString(call.Call.Args[0])
	if !ok {
		return "", nil, false
	}
	var elems []ssa.Value
	if len(call.Call.Args) > 1 {
		elems = sprintfVarargSlice(call.Call.Args[1])
	}
	return format, func(i int) ssa.Value {
		if i >= 0 && i < len(elems) {
			return elems[i]
		}
		return nil
	}, true
}

// sprintfVarargSlice decodes the `[]any{...}` slice SSA constructs for a variadic
// call into its per-element values, unwrapping the `any` boxing.
func sprintfVarargSlice(v ssa.Value) []ssa.Value {
	sl, ok := ssaUnwrap(v).(*ssa.Slice)
	if !ok {
		return nil
	}
	a, ok := sl.X.(*ssa.Alloc)
	if !ok || a.Referrers() == nil {
		return nil
	}
	arr, ok := derefType(a.Type()).(interface{ Len() int64 })
	if !ok {
		return nil
	}
	n := int(arr.Len())
	out := make([]ssa.Value, n)
	for _, ref := range *a.Referrers() {
		ia, ok := ref.(*ssa.IndexAddr)
		if !ok || ia.X != a || ia.Referrers() == nil {
			continue
		}
		idx, ok := constIntValue(ia.Index)
		if !ok || idx < 0 || idx >= n {
			continue
		}
		for _, r2 := range *ia.Referrers() {
			store, ok := r2.(*ssa.Store)
			if !ok || store.Addr != ia {
				continue
			}
			val := store.Val
			if mi, ok := val.(*ssa.MakeInterface); ok {
				val = mi.X
			}
			out[idx] = val
		}
	}
	return out
}

type embeddedRequest struct {
	method string
	url    string
}

// scanEmbeddedFetch finds every `fetch(...)` in a reconstructed JS format string
// and resolves its URL token: printf verbs in the token are replaced with the
// resolved value of the matching Sprintf vararg (or `*` when opaque), so a bare
// `%s` fed from `fmt.Sprintf("/x/%s/y", id)` renders `/x/*/y`.
func scanEmbeddedFetch(format string, argAt func(int) ssa.Value) []embeddedRequest {
	var out []embeddedRequest
	for _, m := range reEmbeddedFetch.FindAllStringSubmatchIndex(format, -1) {
		tokenStart, tokenEnd := m[4], m[5]
		token := format[tokenStart:tokenEnd]
		verbBase := countFormatVerbs(format[:tokenStart])
		url := resolveTokenURL(token, verbBase, argAt)
		if url == "" {
			continue
		}
		method := "GET"
		tail := format[m[1]:]
		if len(tail) > 400 {
			tail = tail[:400]
		}
		if mm := reFetchMethod.FindStringSubmatch(tail); mm != nil {
			method = strings.ToUpper(mm[1])
		}
		out = append(out, embeddedRequest{method: method, url: url})
	}
	return out
}

// resolveTokenURL expands the printf verbs in a fetch URL token, consulting the
// Sprintf varargs so a `%s` placeholder recovers the literal path segments its
// argument carries.
func resolveTokenURL(token string, verbBase int, argAt func(int) ssa.Value) string {
	var b strings.Builder
	k := verbBase
	for i := 0; i < len(token); i++ {
		if token[i] != '%' {
			b.WriteByte(token[i])
			continue
		}
		j := i + 1
		for j < len(token) && strings.IndexByte("+-# 0123456789.", token[j]) >= 0 {
			j++
		}
		if j >= len(token) {
			b.WriteByte('%')
			break
		}
		if token[j] == '%' {
			b.WriteByte('%')
			i = j
			continue
		}
		sub := "*"
		if av := argAt(k); av != nil {
			if r := resolveComposedURL(av, 0); r != "" {
				sub = r
			}
		}
		b.WriteString(sub)
		k++
		i = j
	}
	return collapseWildcards(b.String())
}

// countFormatVerbs counts the printf substitution verbs in s (`%%` excluded),
// matching expandFormatVerbs' scanning rules.
func countFormatVerbs(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		j := i + 1
		for j < len(s) && strings.IndexByte("+-# 0123456789.", s[j]) >= 0 {
			j++
		}
		if j >= len(s) {
			break
		}
		if s[j] != '%' {
			n++
		}
		i = j
	}
	return n
}

// composedRequestPathFromString applies the same query-strip / literal-authority
// / evidence-grading rules as composedRequestPath, but to an already-resolved URL
// string rather than an SSA value.
func composedRequestPathFromString(pattern string) (string, string, bool) {
	pattern = collapseWildcards(pattern)
	if i := strings.IndexByte(pattern, '?'); i >= 0 {
		pattern = pattern[:i]
	}
	if hasLiteralAuthority(pattern) {
		return "", "", false
	}
	evidence := pathEvidence(pattern)
	if evidence == pathEvidenceNone {
		return "", "", false
	}
	return pattern, evidence, true
}
