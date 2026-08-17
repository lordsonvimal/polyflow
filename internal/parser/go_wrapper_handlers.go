package parser

import (
	"fmt"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/ssa"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Tier X.14 — interprocedural handler-registration wrapper resolution.
//
// The tree-sitter net_http_handler patterns only match a literal
// `X.HandleFunc("/path", handler)` / `X.Handle("/path", handler)` call
// expression. A route table that goes through a local audit/logging wrapper
// instead —
//
//	func (s *Server) handle(pattern string, h http.HandlerFunc) {
//	    s.mux.HandleFunc(pattern, s.audit(pattern, h))
//	}
//	func (s *Server) registerRoutes() {
//	    s.handle("GET /api/graph/search", s.handleSearch)
//	}
//
// — never matches: inside handle, the path/handler are parameters, not
// literals; at the call site, the callee is named "handle", not
// "HandleFunc"/"Handle". Every route registered through the wrapper is
// missing from the graph entirely (no http_handler node at all), which is
// silent — nothing downstream (contract matching, cross-service linking)
// even has a target to fail against.
//
// This pass closes that one indirection using the SSA program the semantic
// analyzer already builds: it finds registration wrappers whose path and
// handler are forwarded parameters, then resolves each call site's literal
// path and concrete handler function, synthesizing one http_handler node
// (plus the "calls" edge straight to the resolved handler, since the callee
// is already known here — no need to route back through LinkRouteHandlers'
// label-based lookup) per call site. Mirrors go_wrapper_urls.go's X.7 pass
// for the http_client side of the same problem.
//
// Scope: a wrapper method/function taking a path parameter and a handler
// parameter, forwarding both (the handler through at most one further
// wrapping call, e.g. an audit/logging decorator) into a `.HandleFunc`/
// `.Handle` call. Multi-hop wrapper chains are a follow-up.

// handlerWrapperInfo records a function whose HTTP route registration derives
// from its own parameters.
type handlerWrapperInfo struct {
	pathParamIndex    int    // SSA param index (receiver-inclusive) carrying the route pattern
	handlerParamIndex int    // SSA param index carrying the handler
	field             string // "HandleFunc" or "Handle" — mirrors the tree-sitter pattern name
}

// extractWrapperHandlers synthesizes resolved http_handler nodes (with their
// calls edge to the concrete handler function) for routes registered through
// a wrapper. Deterministic: wrappers and call sites are visited in a
// name/position-sorted function order, and the returned slices are sorted by
// ID before return (bug-class #2).
func extractWrapperHandlers(
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

	byFn := make(map[*ssa.Function]handlerWrapperInfo)
	for _, fn := range fns {
		if info, ok := directHandlerParamWrapper(fn); ok {
			byFn[fn] = info
		}
	}
	if len(byFn) == 0 {
		return nil, nil
	}

	var nodes []graph.Node
	var edges []graph.Edge
	seen := map[string]bool{}
	for _, caller := range fns {
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
				if w.pathParamIndex >= len(common.Args) || w.handlerParamIndex >= len(common.Args) {
					continue
				}
				pathLit, ok := ssaConstString(common.Args[w.pathParamIndex])
				if !ok {
					// Non-literal path at this call site — nothing to synthesize.
					continue
				}
				handlerFn, ok := resolveHandlerArgFunction(common.Args[w.handlerParamIndex])
				if !ok {
					continue
				}
				calleeID, ok := resolveFunc(handlerFn)
				if !ok {
					continue
				}

				pos := fset.Position(instr.Pos())
				if !pos.IsValid() {
					pos = fset.Position(caller.Pos())
				}
				file := relPath(pos.Filename)
				node, edge, ok := emitResolvedHandler(service, file, pos, calleeID, w.field, pathLit, seen)
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

// emitResolvedHandler builds the node/edge pair for one resolved wrapper call
// site. ID/Meta shape matches what the net_http_handler tree-sitter patterns
// produce (patterns/go/net_http_handler.yaml + internal/patterns/matcher.go's
// Go-1.22-ServeMux "METHOD /path" split) so downstream consumers — contract
// route matching, LinkRouteHandlers, the UI — can't tell the two apart. The
// calls edge is emitted directly rather than left for LinkRouteHandlers,
// because the callee is already known here (LinkRouteHandlers only exists to
// recover it from a Meta["handler"] label match, which this pass never needs).
func emitResolvedHandler(
	service, file string, pos token.Position,
	calleeID, field, pathLit string,
	seen map[string]bool,
) (graph.Node, graph.Edge, bool) {
	if graph.IsTestFilePath(file) {
		return graph.Node{}, graph.Edge{}, false
	}
	patternName := "http_handle_func"
	if field == "Handle" {
		patternName = "http_handle"
	}
	id := fmt.Sprintf("%s:%s:%s:%s:%d", service, file, graph.NodeTypeHTTPHandler, patternName, pos.Line)
	if seen[id] {
		return graph.Node{}, graph.Edge{}, false
	}
	seen[id] = true

	method, path := "", pathLit
	if i := strings.IndexByte(pathLit, ' '); i > 0 {
		method, path = pathLit[:i], pathLit[i+1:]
	}
	label := path
	if method != "" {
		label = method + " " + path
	}
	meta := map[string]string{
		"fn":          field,
		"path":        path,
		"pattern":     patternName,
		"synthesized": "wrapper_handler",
	}
	if method != "" {
		meta["method"] = method
	}
	node := graph.Node{
		ID:       id,
		Type:     graph.NodeTypeHTTPHandler,
		Label:    label,
		Service:  service,
		File:     file,
		Line:     pos.Line,
		EndLine:  pos.Line,
		Language: "go",
		Meta:     meta,
	}
	edge := graph.Edge{
		ID:         fmt.Sprintf("wrapperhandler:%s:%s->%s", graph.EdgeTypeCalls, id, calleeID),
		From:       id,
		To:         calleeID,
		Type:       graph.EdgeTypeCalls,
		Confidence: graph.ConfidenceStatic,
		Meta:       map[string]string{"via": "wrapper_handler"},
	}
	return node, edge, true
}

// directHandlerParamWrapper inspects fn's body for a `.HandleFunc`/`.Handle`
// registration call whose path argument is one of fn's own parameters. The
// handler argument may be a parameter directly, or forwarded through one more
// wrapping call (an audit/logging decorator that also takes the path, like
// `s.audit(pattern, h)`) — resolveHandlerParamIndex covers both.
func directHandlerParamWrapper(fn *ssa.Function) (handlerWrapperInfo, bool) {
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
			field, ok := handleFuncFieldName(common)
			if !ok {
				continue
			}
			// A statically-resolved method call carries its receiver as
			// Args[0] ahead of the declared parameters (`s.mux.HandleFunc(pattern,
			// handler)` → Args = [mux, pattern, handler]); a package-level function
			// call (`http.HandleFunc(pattern, handler)`) does not.
			off := 0
			if sf, ok := common.Value.(*ssa.Function); ok && sf.Signature.Recv() != nil {
				off = 1
			}
			if len(common.Args) < off+2 {
				continue
			}
			pathIdx, ok := paramIndex(ssaUnwrap(common.Args[off]), fn)
			if !ok {
				continue
			}
			handlerIdx, ok := resolveHandlerParamIndex(common.Args[off+1], fn, pathIdx)
			if !ok {
				continue
			}
			return handlerWrapperInfo{pathParamIndex: pathIdx, handlerParamIndex: handlerIdx, field: field}, true
		}
	}
	return handlerWrapperInfo{}, false
}

// handleFuncFieldName reports the callee's method/function name when it is
// "HandleFunc" or "Handle" — the same receiver-agnostic match the
// net_http_handler tree-sitter query performs on `X.HandleFunc(...)` /
// `X.Handle(...)`, just evaluated against the SSA-resolved static callee
// instead of a bare selector field.
func handleFuncFieldName(common *ssa.CallCommon) (string, bool) {
	fn, ok := common.Value.(*ssa.Function)
	if !ok {
		return "", false
	}
	switch fn.Name() {
	case "HandleFunc", "Handle":
		return fn.Name(), true
	default:
		return "", false
	}
}

// resolveHandlerParamIndex reports whether v is fn's own handler parameter —
// directly, or forwarded through one wrapping call that itself passes that
// same parameter along (e.g. an audit/logging decorator called as
// `s.audit(pattern, h)`, where h is fn's handler param). exclude is the
// already-claimed path parameter index, so a decorator that also forwards the
// path (as an audit wrapper typically does) can't be mistaken for the handler.
func resolveHandlerParamIndex(v ssa.Value, fn *ssa.Function, exclude int) (int, bool) {
	// A decorator call passes fn's own receiver as its first argument
	// (`s.audit(pattern, h)` → Args = [s, pattern, h]); that receiver is
	// fn.Params[0] whenever fn itself is a method, and must never be picked as
	// the handler.
	minIdx := 0
	if fn.Signature.Recv() != nil {
		minIdx = 1
	}
	v = ssaUnwrap(v)
	if idx, ok := paramIndex(v, fn); ok && idx != exclude && idx >= minIdx {
		return idx, true
	}
	call, ok := v.(*ssa.Call)
	if !ok {
		return 0, false
	}
	common := call.Common()
	if common.IsInvoke() {
		return 0, false
	}
	for _, arg := range common.Args {
		if idx, ok := paramIndex(ssaUnwrap(arg), fn); ok && idx != exclude && idx >= minIdx {
			return idx, true
		}
	}
	return 0, false
}

// resolveHandlerArgFunction resolves a call-site handler argument to the
// concrete function it names: a bare function reference, or a bound-method
// value (`s.handleSearch`), which SSA represents as a MakeClosure over a
// synthetic thunk whose single instruction calls the real method with the
// receiver bound as a free variable.
func resolveHandlerArgFunction(v ssa.Value) (*ssa.Function, bool) {
	switch t := ssaUnwrap(v).(type) {
	case *ssa.Function:
		return t, true
	case *ssa.MakeClosure:
		thunk, ok := t.Fn.(*ssa.Function)
		if !ok {
			return nil, false
		}
		for _, b := range thunk.Blocks {
			for _, instr := range b.Instrs {
				ci, ok := instr.(ssa.CallInstruction)
				if !ok {
					continue
				}
				if callee := ci.Common().StaticCallee(); callee != nil {
					return callee, true
				}
			}
		}
	}
	return nil, false
}
