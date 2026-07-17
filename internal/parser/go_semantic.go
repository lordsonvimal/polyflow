package parser

import (
	"fmt"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func init() {
	RegisterServiceAnalyzer(&GoSemanticAnalyzer{})
}

// GoSemanticAnalyzer builds a type-resolved call graph for a Go service directory
// using golang.org/x/tools (go/packages + SSA). It walks SSA instructions directly
// rather than using RTA, which avoids panics when there is no single main entry point
// (e.g. library packages, tool packages with multiple mains).
type GoSemanticAnalyzer struct{}

func (a *GoSemanticAnalyzer) Language() string { return "go" }

// collapseTestVariants reduces the package set returned by packages.Load with
// Tests:true to one variant per import path:
//
//   - the synthetic test binary ("pkg.test", generated main only) is dropped;
//   - when both the plain package and its test-augmented variant
//     ("pkg [pkg.test]", a strict superset that adds in-package _test.go
//     files) are present, the test variant wins — unless the test variant has
//     build errors and the plain one is clean, in which case broken tests
//     must not take down the production call graph (fall back to plain);
//   - external test packages ("pkg_test") have their own import path and pass
//     through.
//
// Duplicate nodes/edges across variants are additionally deduped downstream
// by deterministic ID, so this filter is about error isolation and not
// double-walking, but it keeps the SSA build small.
func collapseTestVariants(pkgs []*packages.Package) []*packages.Package {
	type slot struct {
		plain *packages.Package
		test  *packages.Package
	}
	byPath := make(map[string]*slot, len(pkgs))
	order := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		if strings.HasSuffix(p.PkgPath, ".test") {
			continue // synthetic test binary
		}
		s, ok := byPath[p.PkgPath]
		if !ok {
			s = &slot{}
			byPath[p.PkgPath] = s
			order = append(order, p.PkgPath)
		}
		if strings.Contains(p.ID, " [") {
			s.test = p
		} else {
			s.plain = p
		}
	}
	out := make([]*packages.Package, 0, len(order))
	for _, path := range order {
		s := byPath[path]
		switch {
		case s.test != nil && len(s.test.Errors) == 0:
			out = append(out, s.test)
		case s.plain != nil:
			out = append(out, s.plain)
			// errored test-only variants (in-package or external _test) are
			// dropped: broken tests degrade to the pre-Tests:true graph
			// instead of aborting the whole semantic pass.
		}
	}
	return out
}

// AnalyzeService loads all packages under dir, builds SSA, then walks every Call
// instruction in every function to emit caller→callee edges. Only functions whose
// source file is inside dir are included — stdlib and vendor dependencies are skipped.
//
// knownNodes is the set of node IDs already written by tree-sitter. The semantic
// pass resolves SSA functions against this set by file+name lookup (ignoring line
// number, which differs between tree-sitter and SSA due to how each counts the
// `func` keyword position). Edges where either endpoint is not in knownNodes are
// dropped.
func (a *GoSemanticAnalyzer) AnalyzeService(dir, service string, fset *token.FileSet, knownNodes map[string]bool) SemanticResult {
	cfg := &packages.Config{
		Mode: packages.LoadAllSyntax,
		Dir:  dir,
		Fset: fset,
		// Tests: load *_test.go too — tests are real callers, and blast radius
		// without them silently omits "which tests break if I change this"
		// (recall over precision). Edges still resolve against knownNodes, so
		// workspaces that exclude test files from the walk are unaffected.
		Tests: true,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return SemanticResult{
			Warning: fmt.Sprintf("go/packages load failed for service %q: %v — falling back to tree-sitter call edges", service, err),
		}
	}
	pkgs = collapseTestVariants(pkgs)
	if packages.PrintErrors(pkgs) > 0 {
		return SemanticResult{
			Warning: fmt.Sprintf("service %q has build errors — semantic call graph unavailable, falling back to tree-sitter", service),
		}
	}

	prog, ssaPkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	// Build file+name → nodeID index from known tree-sitter nodes.
	// Key: "file\x00name" (both function and method are stored; we try both types).
	// Node IDs carry workspace-relative file paths while SSA positions are
	// absolute, so both sides are canonicalized before comparison.
	nodeByFileAndName := make(map[string]string, len(knownNodes))
	// Worker (goroutine) nodes indexed by file+line: anonymous SSA functions
	// spawned by `go func(){…}` resolve here so the goroutine body's calls
	// flow out of the worker node instead of the enclosing named function.
	workerByFileLine := make(map[string]string)
	// Templ component nodes indexed by generated-file path + label so a Go
	// `Component(args).Render(ctx, w)` call site can be linked back to the
	// `.templ` component it draws (T.4 renders pass). Keyed on the derived
	// generated path (`x.templ` → `x_templ.go`) + label, mirroring T.1's
	// LinkTemplComponents, so the semantic pass — which only sees the generated
	// Go function's position — can find the component twin.
	templComponentByGenKey := make(map[string]string)
	// Collect node IDs in sorted order so that "first wins" maps are
	// deterministic across runs regardless of Go map iteration order.
	// (Bug-class rule 2: Go map iteration must never reach output.)
	sortedIDs := make([]string, 0, len(knownNodes))
	for id := range knownNodes {
		sortedIDs = append(sortedIDs, id)
	}
	sort.Strings(sortedIDs)
	for _, id := range sortedIDs {
		// ID format: service:file:type:name:line
		parts := strings.SplitN(id, ":", 5)
		if len(parts) != 5 {
			continue
		}
		file, name := parts[1], parts[3]
		if parts[2] == string(graph.NodeTypeWorker) {
			if _, exists := workerByFileLine[canonicalPath(file)+"\x00"+parts[4]]; !exists {
				workerByFileLine[canonicalPath(file)+"\x00"+parts[4]] = id
			}
			continue
		}
		if parts[2] == string(graph.NodeTypeComponent) && strings.HasSuffix(file, ".templ") {
			genPath := file[:len(file)-len(".templ")] + "_templ.go"
			if _, exists := templComponentByGenKey[canonicalPath(genPath)+"\x00"+name]; !exists {
				templComponentByGenKey[canonicalPath(genPath)+"\x00"+name] = id
			}
			continue
		}
		key := canonicalPath(file) + "\x00" + name
		if _, exists := nodeByFileAndName[key]; !exists {
			nodeByFileAndName[key] = id
		}
	}

	// resolveFunc maps an SSA function to its tree-sitter node ID via file+name lookup.
	resolveFunc := func(fn *ssa.Function) (string, bool) {
		if fn.Synthetic != "" || fn.Package() == nil {
			return "", false
		}
		pos := fset.Position(fn.Pos())
		if !pos.IsValid() || pos.Filename == "" {
			return "", false
		}
		// Anonymous functions: when a worker node exists at the func literal's
		// position (a goroutine body), resolve to it; otherwise fall through to
		// name-stripping, which attributes plain closures to their parent.
		if fn.Parent() != nil {
			key := canonicalPath(pos.Filename) + "\x00" + strconv.Itoa(pos.Line)
			if id, ok := workerByFileLine[key]; ok {
				return id, true
			}
		}
		// Strip anonymous suffixes like "$1" and numbered init suffixes like "#1".
		name := fn.Name()
		if idx := strings.Index(name, "$"); idx >= 0 {
			name = name[:idx]
		}
		if idx := strings.Index(name, "#"); idx >= 0 {
			name = name[:idx]
		}
		if name == "" {
			return "", false
		}
		key := canonicalPath(pos.Filename) + "\x00" + name
		id, ok := nodeByFileAndName[key]
		return id, ok
	}

	// templComponentFor maps the receiver value of a `X.Render(ctx, w)` invoke
	// to the templ component node X draws, when X is a generated templ function
	// call (`views.PuzzleRows(vm)`). Returns "" for any receiver that is not a
	// direct call to a `_templ.go` function with a known component twin.
	templComponentFor := func(recv ssa.Value) string {
		if mi, ok := recv.(*ssa.MakeInterface); ok {
			recv = mi.X
		}
		call, ok := recv.(*ssa.Call)
		if !ok {
			return ""
		}
		fn, ok := call.Call.Value.(*ssa.Function)
		if !ok || fn.Pkg == nil {
			return ""
		}
		pos := fset.Position(fn.Pos())
		if !pos.IsValid() || !strings.HasSuffix(pos.Filename, "_templ.go") {
			return ""
		}
		genKey := canonicalPath(pos.Filename) + "\x00" + fn.Name()
		return templComponentByGenKey[genKey]
	}

	// Collect in-service functions.
	allFns := ssautil.AllFunctions(prog)
	inService := make(map[*ssa.Function]bool)
	resolved := 0
	for fn := range allFns {
		if isServiceFunc(fn, dir, fset) {
			inService[fn] = true
			if _, ok := resolveFunc(fn); ok {
				resolved++
			}
		}
	}
	// Every SSA function failing to resolve against the tree-sitter node set
	// means the two sides disagree on file paths (or the node set is stale) —
	// silently returning zero edges would leave the whole call graph missing.
	if len(inService) > 0 && resolved == 0 {
		return SemanticResult{
			Warning: fmt.Sprintf("service %q: none of %d analyzed functions matched indexed nodes — call edges unavailable (path mismatch between analyzer and index?)", service, len(inService)),
		}
	}

	seen := make(map[string]bool)
	var edges []graph.Edge

	for caller := range inService {
		callerID, ok := resolveFunc(caller)
		if !ok {
			continue
		}

		// Per-caller templ-render tracking (T.4): the components this function
		// draws via `Component(args).Render(ctx, w)`, and whether the function
		// streams them over a Datastar SSE response (`datastar.NewSSE`).
		var renderTargets []string
		callerIsSSE := false

		for _, b := range caller.Blocks {
			for _, instr := range b.Instrs {
				var callees []*ssa.Function

				switch c := instr.(type) {
				case ssa.CallInstruction:
					common := c.Common()
					if common.IsInvoke() {
						// `X.Render(ctx, w)` on a templ.Component: record the
						// component X draws so the enclosing func gets a renders
						// edge to the .templ node (not just the calls edge to the
						// generated Go twin).
						if common.Method != nil && common.Method.Name() == "Render" && isTemplRenderSig(common.Method) {
							if compID := templComponentFor(common.Value); compID != "" {
								renderTargets = append(renderTargets, compID)
							}
						}
						for fn := range allFns {
							if fn.Synthetic != "" {
								continue
							}
							if matchesInvoke(common, fn) {
								callees = append(callees, fn)
							}
						}
					} else if fn, ok2 := common.Value.(*ssa.Function); ok2 {
						callees = append(callees, fn)
						if isDatastarNewSSE(fn) {
							callerIsSSE = true
						}
					}
				}

				_, isGo := instr.(*ssa.Go)
				for _, callee := range callees {
					if !inService[callee] {
						continue
					}
					calleeID, ok := resolveFunc(callee)
					if !ok {
						continue
					}
					if callerID == calleeID {
						continue
					}
					key := callerID + "->" + calleeID
					if seen[key] {
						continue
					}
					seen[key] = true
					if isGo {
						// `go f()` / `go func(){…}()`: a spawn, not a call. The
						// ID matches the tree-sitter pattern edge so the store
						// upsert dedupes instead of drawing a second edge.
						edges = append(edges, graph.Edge{
							ID:   fmt.Sprintf("%s:%s->%s", graph.EdgeTypeSpawns, callerID, calleeID),
							From: callerID,
							To:   calleeID,
							Type: graph.EdgeTypeSpawns,
						})
						continue
					}
					edges = append(edges, graph.Edge{
						ID:   "semantic:calls:" + key,
						From: callerID,
						To:   calleeID,
						Type: graph.EdgeTypeCalls,
					})
				}
			}
		}

		// Emit renders (and, for SSE streamers, sse_endpoint) edges from this
		// function to each templ component it draws. Deduplicated per (caller,
		// component); a handler that renders the same component twice draws one
		// edge. SSE streaming is tagged on the renders edge and mirrored as an
		// sse_endpoint edge so the server-push path is queryable.
		renderSeen := make(map[string]bool, len(renderTargets))
		for _, compID := range renderTargets {
			if renderSeen[compID] {
				continue
			}
			renderSeen[compID] = true
			meta := map[string]string{"via": "templ_render"}
			if callerIsSSE {
				meta["sse"] = "true"
			}
			edges = append(edges, graph.Edge{
				ID:         "renders:" + callerID + "->" + compID,
				From:       callerID,
				To:         compID,
				Type:       graph.EdgeTypeRenders,
				Confidence: graph.ConfidenceStatic,
				Meta:       meta,
			})
			if callerIsSSE {
				edges = append(edges, graph.Edge{
					ID:         "sse_endpoint:" + callerID + "->" + compID,
					From:       callerID,
					To:         compID,
					Type:       graph.EdgeTypeSSEEndpoint,
					Confidence: graph.ConfidenceStatic,
					Meta:       map[string]string{"via": "datastar_sse"},
				})
			}
		}
	}

	// Synthetic main→init edges: Go's runtime calls all init() functions before
	// main(), but there's no explicit call site in main's body. Emit a synthetic
	// calls edge from main to each init in the same package so main is connected.
	for caller := range inService {
		if caller.Name() != "main" {
			continue
		}
		callerID, ok := resolveFunc(caller)
		if !ok {
			continue
		}
		callerPkg := caller.Package()
		for callee := range inService {
			name := callee.Name()
			// SSA names user-written init functions as init#1, init#2, etc.
			// After # stripping in resolveFunc they all map to "init".
			if name != "init" && !strings.HasPrefix(name, "init#") {
				continue
			}
			if callee.Package() != callerPkg {
				continue
			}
			calleeID, ok := resolveFunc(callee)
			if !ok {
				continue
			}
			key := callerID + "->" + calleeID
			if seen[key] {
				continue
			}
			seen[key] = true
			edges = append(edges, graph.Edge{
				ID:   "semantic:calls:" + key,
				From: callerID,
				To:   calleeID,
				Type: graph.EdgeTypeCalls,
			})
		}
	}

	// Variable-tracking layer: package globals/consts, structs, interface nodes,
	// mutations, closure captures, inherits (embedding), instantiates.
	varResult := extractVariables(ssaPkgs, dir, service, fset, inService, resolveFunc)
	edges = append(edges, varResult.Edges...)

	// Implements-edge sweep: in-service structs → in-service and external
	// interfaces they satisfy (type-checker-proven, confidence static).
	implNodes, implEdges := extractImplements(ssaPkgs, service, varResult.StructIDs, varResult.InterfaceIDs)
	edges = append(edges, implEdges...)

	allNodes := append(varResult.Nodes, implNodes...)

	referenced := collectReferenced(prog, ssaPkgs, allFns, resolveFunc)

	return SemanticResult{Nodes: allNodes, Edges: edges, Referenced: referenced}
}

// collectReferenced finds functions that are referenced without being called
// in-service — the "framework callback" signal for root classification:
//  1. Function values appearing as instruction operands outside the callee
//     position (stored in composite literals like cobra's RunE, passed to
//     http.HandlerFunc, assigned to fields). Synthetic package initializers
//     are scanned too: package-level `var cmd = &cobra.Command{RunE: runX}`
//     lives there.
//  2. Methods that satisfy an interface belonging to a package outside the
//     service (templ's Visitor, io.Reader): external code invokes them, so
//     an absent in-service caller does not mean dead.
func collectReferenced(prog *ssa.Program, ssaPkgs []*ssa.Package, allFns map[*ssa.Function]bool, resolveFunc func(*ssa.Function) (string, bool)) []string {
	svcPkgs := make(map[*ssa.Package]bool, len(ssaPkgs))
	svcTypesPkgs := make(map[*types.Package]bool, len(ssaPkgs))
	for _, p := range ssaPkgs {
		if p != nil {
			svcPkgs[p] = true
			svcTypesPkgs[p.Pkg] = true
		}
	}

	referenced := make(map[string]bool)

	// 1. Operand scan (includes synthetic init functions of service packages).
	for fn := range allFns {
		if fn.Package() == nil || !svcPkgs[fn.Package()] {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				var callee ssa.Value
				if c, ok := instr.(ssa.CallInstruction); ok && !c.Common().IsInvoke() {
					callee = c.Common().Value
				}
				var rands [8]*ssa.Value
				for _, op := range instr.Operands(rands[:0]) {
					if op == nil || *op == nil || *op == callee {
						continue
					}
					target, ok := (*op).(*ssa.Function)
					if !ok {
						continue
					}
					if id, ok := resolveFunc(target); ok {
						referenced[id] = true
					}
				}
			}
		}
	}

	// 2. External-interface method sets.
	for _, p := range ssaPkgs {
		if p == nil {
			continue
		}
		// Candidate interfaces: exported interfaces of directly imported
		// packages that are not part of this service.
		var ifaces []*types.Interface
		for _, imp := range p.Pkg.Imports() {
			if svcTypesPkgs[imp] {
				continue
			}
			scope := imp.Scope()
			for _, name := range scope.Names() {
				tn, ok := scope.Lookup(name).(*types.TypeName)
				if !ok {
					continue
				}
				if iface, ok := tn.Type().Underlying().(*types.Interface); ok && iface.NumMethods() > 0 {
					ifaces = append(ifaces, iface)
				}
			}
		}
		if len(ifaces) == 0 {
			continue
		}
		scope := p.Pkg.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			T := tn.Type()
			ptrT := types.NewPointer(T)
			for _, iface := range ifaces {
				var impl types.Type
				if types.Implements(T, iface) {
					impl = T
				} else if types.Implements(ptrT, iface) {
					impl = ptrT
				} else {
					continue
				}
				for i := 0; i < iface.NumMethods(); i++ {
					m := iface.Method(i)
					sel := prog.MethodSets.MethodSet(impl).Lookup(m.Pkg(), m.Name())
					if sel == nil {
						continue
					}
					fn := prog.MethodValue(sel)
					if fn == nil {
						continue
					}
					if id, ok := resolveFunc(fn); ok {
						referenced[id] = true
					}
				}
			}
		}
	}

	out := make([]string, 0, len(referenced))
	for id := range referenced {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// canonicalPath resolves a path to its absolute, symlink-evaluated form so
// workspace-relative node paths and absolute go/packages positions compare
// equal (filepath.Abs resolves relative paths against the indexer's cwd,
// which is the workspace root).
func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// isServiceFunc returns true if fn is a non-synthetic function whose source file
// is under serviceDir.
func isServiceFunc(fn *ssa.Function, serviceDir string, fset *token.FileSet) bool {
	if fn.Synthetic != "" || fn.Package() == nil {
		return false
	}
	pos := fset.Position(fn.Pos())
	if !pos.IsValid() || pos.Filename == "" {
		return false
	}
	return strings.HasPrefix(canonicalPath(pos.Filename), canonicalPath(serviceDir))
}

// isTemplRenderSig reports whether m has the templ.Component.Render shape —
// `Render(context.Context, io.Writer) error`. Matched structurally (not by the
// templ import path) so the check holds regardless of the templ module version
// or a vendored fork, and so it excludes unrelated `Render` methods with a
// different signature.
func isTemplRenderSig(m *types.Func) bool {
	sig, ok := m.Type().(*types.Signature)
	if !ok {
		return false
	}
	if sig.Params().Len() != 2 || sig.Results().Len() != 1 {
		return false
	}
	if sig.Results().At(0).Type().String() != "error" {
		return false
	}
	return sig.Params().At(1).Type().String() == "io.Writer"
}

// isDatastarNewSSE reports whether fn is the Datastar SSE constructor
// (`datastar.NewSSE`), the signal that its caller streams fragments over an SSE
// response. Keyed on the datastar package path + name rather than the writer
// type so it holds across datastar-go versions.
func isDatastarNewSSE(fn *ssa.Function) bool {
	if fn.Name() != "NewSSE" || fn.Pkg == nil || fn.Pkg.Pkg == nil {
		return false
	}
	return strings.Contains(fn.Pkg.Pkg.Path(), "datastar")
}

// matchesInvoke returns true if fn satisfies the interface method described by call.
// We match by method name only — a lightweight approximation sufficient for v1.5.
func matchesInvoke(call *ssa.CallCommon, fn *ssa.Function) bool {
	if fn.Signature.Recv() == nil {
		return false
	}
	return fn.Name() == call.Method.Name()
}

// extractImplements emits implements edges from in-service struct types to
// every interface they satisfy — both in-service interfaces (in interfaceIDs)
// and external interfaces (imported by service packages). All edges carry
// static confidence (type-checker-proven) and meta.nominal=false (Go
// satisfaction is structural). External interface targets become synthetic
// NodeTypeInterface nodes with no file/line and meta.external=true.
func extractImplements(
	ssaPkgs []*ssa.Package,
	service string,
	structIDs map[*types.Named]string,
	interfaceIDs map[*types.Named]string,
) ([]graph.Node, []graph.Edge) {
	if len(structIDs) == 0 {
		return nil, nil
	}

	// svcTypesPkgs: the type packages belonging to this service (not external).
	svcTypesPkgs := make(map[*types.Package]bool, len(ssaPkgs))
	for _, p := range ssaPkgs {
		if p != nil {
			svcTypesPkgs[p.Pkg] = true
		}
	}

	var nodes []graph.Node
	var edges []graph.Edge
	nodeSeen := map[string]bool{}
	edgeSeen := map[string]bool{}

	addEdge := func(from, to string, meta map[string]string) {
		id := fmt.Sprintf("semantic:implements:%s->%s", from, to)
		if edgeSeen[id] {
			return
		}
		edgeSeen[id] = true
		edges = append(edges, graph.Edge{
			ID: id, From: from, To: to, Type: graph.EdgeTypeImplements,
			Confidence: graph.ConfidenceStatic, Meta: meta,
		})
	}

	// syntheticIfaceID returns the node ID for a synthetic external interface
	// node. The node is created the first time a particular (pkgPath, name)
	// pair is seen.
	syntheticIfaceID := func(pkgPath, name string) string {
		id := fmt.Sprintf("%s::interface:%s.%s:0", service, pkgPath, name)
		if !nodeSeen[id] {
			nodeSeen[id] = true
			nodes = append(nodes, graph.Node{
				ID:       id,
				Type:     graph.NodeTypeInterface,
				Label:    pkgPath + "." + name,
				Service:  service,
				Language: "go",
				Meta:     map[string]string{"external": "true"},
			})
		}
		return id
	}

	// seenExtIface deduplicates the external interface collection across
	// service packages that import the same external package.
	type extIfaceEntry struct {
		iface    *types.Interface
		nodeID   string
	}
	seenExtIface := map[string]extIfaceEntry{} // pkgPath.Name → entry

	for _, p := range ssaPkgs {
		if p == nil {
			continue
		}
		// Collect external candidate interfaces from this package's imports.
		for _, imp := range p.Pkg.Imports() {
			if svcTypesPkgs[imp] {
				continue
			}
			scope := imp.Scope()
			for _, name := range scope.Names() {
				tn, ok := scope.Lookup(name).(*types.TypeName)
				if !ok {
					continue
				}
				iface, ok := tn.Type().Underlying().(*types.Interface)
				if !ok || iface.NumMethods() == 0 {
					continue
				}
				key := imp.Path() + "." + name
				if _, already := seenExtIface[key]; !already {
					nodeID := syntheticIfaceID(imp.Path(), name)
					seenExtIface[key] = extIfaceEntry{iface: iface, nodeID: nodeID}
				}
			}
		}

		// For each in-service named type that is a tracked struct, check
		// interface satisfaction.
		scope := p.Pkg.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || tn.IsAlias() {
				continue
			}
			T := tn.Type()
			named, ok := T.(*types.Named)
			if !ok {
				continue
			}
			structID, isStruct := structIDs[named]
			if !isStruct {
				continue
			}
			ptrT := types.NewPointer(T)

			// In-service interfaces.
			for ifaceNamed, ifaceID := range interfaceIDs {
				iface, ok := ifaceNamed.Underlying().(*types.Interface)
				if !ok || iface.NumMethods() == 0 {
					continue
				}
				if types.Implements(T, iface) || types.Implements(ptrT, iface) {
					addEdge(structID, ifaceID, map[string]string{"nominal": "false"})
				}
			}

			// External interfaces (collected above across all packages).
			for _, entry := range seenExtIface {
				if types.Implements(T, entry.iface) || types.Implements(ptrT, entry.iface) {
					addEdge(structID, entry.nodeID, map[string]string{
						"nominal": "false", "external": "true",
					})
				}
			}
		}
	}

	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return nodes, edges
}
