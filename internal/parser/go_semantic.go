package parser

import (
	"fmt"
	"go/build/constraint"
	"go/token"
	"go/types"
	"os"
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
	// Build-tag-gated test files (`//go:build integration`, `e2e`, etc.) are
	// otherwise invisible to go/packages under default build constraints —
	// a silent recall gap, since these are exactly the real test-file callers
	// the Tests:true pass above exists to capture (e.g. *_integration_test.go
	// suites that call production constructors directly). Force-enabling a
	// discovered tag is not automatically safe, though: the same tag can also
	// gate a *production* file (e.g. a `legacy_x` build-tag toggle between two
	// competing implementations), and turning it on can introduce brand-new
	// build errors having nothing to do with the test file that named it.
	// Each candidate is trialed in isolation and kept only if it introduces no
	// error beyond what the untagged baseline already has.
	if widened, ok := widenWithTestBuildTags(dir, fset, pkgs); ok {
		pkgs = widened
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
		// Unwrap interface boxing/re-typing: a component passed as an argument to
		// a method whose param is a *different* component interface (e.g.
		// datastar's TemplComponent vs templ.Component, WS.2) arrives as a
		// ChangeType over the templ call; MakeInterface boxes a concrete value.
	unwrap:
		for {
			switch v := recv.(type) {
			case *ssa.MakeInterface:
				recv = v.X
			case *ssa.ChangeType:
				recv = v.X
			default:
				break unwrap
			}
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

	// Two-pass edge collection: gather (caller, callee, isGo) triples from all
	// SSA functions, then emit edges — preferring "spawns" over "calls" when
	// both exist for the same (callerID, calleeID) pair.  Without this, the
	// non-deterministic map iteration of `inService` lets a closure (resolved
	// to its parent's callerID) race with the parent function itself, flipping
	// the edge type between runs when one uses `go f()` and the other uses
	// `f()` for the same callee.
	spawnPairs := make(map[string]bool)   // callerID+"->"+calleeID seen as ssa.Go
	callPairs := make(map[string]bool)    // callerID+"->"+calleeID seen as regular call
	funcArgCounts := make(map[string]int) // callerID+"->"+calleeID for func-arg references
	var edges []graph.Edge

	// WS.1: in-service forwarders that construct+return a datastar SSE generator,
	// so a handler calling the wrapper (not datastar.NewSSE directly) is still
	// flagged as an SSE streamer.
	sseCtors := sseConstructors(inService)

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
					// WS.2: datastar fragment-patch methods render a templ
					// component passed as an argument
					// (sse.PatchElementTempl(Comp(args))), not via
					// Comp.Render(ctx, w). templComponentFor returns "" for
					// signals/JS/[]byte args, so nothing is fabricated.
					if isDatastarPatchRender(callMethodName(common)) {
						for _, arg := range common.Args {
							if compID := templComponentFor(arg); compID != "" {
								renderTargets = append(renderTargets, compID)
							}
						}
					}
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
						if isDatastarNewSSE(fn) || sseCtors[fn] {
							callerIsSSE = true
						}
					}
					// B.1: detect function values passed as arguments. Any *ssa.Function
					// or *ssa.MakeClosure wrapping a named in-service function in argument
					// position becomes a calls edge (via=func_arg). Synthetic thunks
					// (bound-method wrappers whose Synthetic field is set) resolve to ""
					// in resolveFunc and are silently skipped — no ledger entry.
					// ChangeType unwrapping is required: named function types (e.g.
					// writefreely's `handlerFunc`) produce a *ssa.ChangeType wrapper
					// around the underlying *ssa.Function at the call site.
					for _, arg := range common.Args {
						targetFn := ssaArgFunc(arg)
						if targetFn == nil || !inService[targetFn] {
							continue
						}
						targetID, ok2 := resolveFunc(targetFn)
						if !ok2 || callerID == targetID {
							continue
						}
						funcArgCounts[callerID+"->"+targetID]++
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
					if isGo {
						spawnPairs[key] = true
					} else {
						callPairs[key] = true
					}
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

	// Emit call/spawns edges now that all (caller, callee, isGo) pairs have been
	// accumulated.  Spawns take priority: if the same (caller, callee) pair
	// appears as both a goroutine and a regular call (possible when a closure
	// inside the caller is attributed to the parent node), we emit only the
	// spawns edge so the result is deterministic regardless of iteration order.
	for key := range spawnPairs {
		parts := strings.SplitN(key, "->", 2)
		edges = append(edges, graph.Edge{
			ID:   fmt.Sprintf("%s:%s", graph.EdgeTypeSpawns, key),
			From: parts[0],
			To:   parts[1],
			Type: graph.EdgeTypeSpawns,
		})
	}
	for key := range callPairs {
		if spawnPairs[key] {
			continue
		}
		parts := strings.SplitN(key, "->", 2)
		edges = append(edges, graph.Edge{
			ID:   "semantic:calls:" + key,
			From: parts[0],
			To:   parts[1],
			Type: graph.EdgeTypeCalls,
		})
	}

	// Emit func-arg edges (B.1): function values passed as call arguments.
	// Sorted for determinism (bug-class rule 2); deduped via funcArgCounts.
	funcArgKeys := make([]string, 0, len(funcArgCounts))
	for key := range funcArgCounts {
		funcArgKeys = append(funcArgKeys, key)
	}
	sort.Strings(funcArgKeys)
	for _, key := range funcArgKeys {
		sep := strings.Index(key, "->")
		from, to := key[:sep], key[sep+2:]
		edges = append(edges, graph.Edge{
			ID:         "funcarg:calls:" + key,
			From:       from,
			To:         to,
			Type:       graph.EdgeTypeCalls,
			Confidence: graph.ConfidenceStatic,
			Meta:       map[string]string{"via": "func_arg", "count": strconv.Itoa(funcArgCounts[key])},
		})
	}

	// Synthetic main→init edges: Go's runtime calls all init() functions before
	// main(), but there's no explicit call site in main's body. Emit a synthetic
	// calls edge from main to each init in the same package so main is connected.
	syntheticSeen := make(map[string]bool)
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
			if syntheticSeen[key] {
				continue
			}
			syntheticSeen[key] = true
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
	varResult := extractVariables(pkgs, ssaPkgs, dir, service, fset, inService, resolveFunc)
	edges = append(edges, varResult.Edges...)

	// Implements-edge sweep: in-service structs → in-service and external
	// interfaces they satisfy (type-checker-proven, confidence static).
	implNodes, implEdges := extractImplements(ssaPkgs, service, varResult.StructIDs, varResult.InterfaceIDs)
	edges = append(edges, implEdges...)

	allNodes := append(varResult.Nodes, implNodes...)

	// Tier X.7: interprocedural wrapper URL propagation. Synthesize resolved
	// http_client producers for API-client wrappers whose URL is a parameter
	// (e.g. doWithRetry(method, path) → http.NewRequest(method, base+path)),
	// reading each call site's literal path from SSA. Runs on the same program,
	// so no extra load. The param-dynamic matcher node stays as the ledger for
	// non-literal callers.
	wrapNodes, wrapEdges := extractWrapperURLs(service, dir, fset, inService, resolveFunc)
	allNodes = append(allNodes, wrapNodes...)
	edges = append(edges, wrapEdges...)

	// W.2: resolve wrapper-mediated / const AMQP publish exchanges into producer
	// channel nodes so the amqp contract can join them to W.1's consumer binds.
	amqpNodes, amqpEdges := extractAMQPNames(service, dir, fset, inService, resolveFunc)
	allNodes = append(allNodes, amqpNodes...)
	edges = append(edges, amqpEdges...)

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

// reservedBuildTags are tags the Go toolchain manages implicitly (GOOS,
// maxTestBuildTagCandidates bounds how many discovered tags widenWithTestBuildTags
// will trial. Each candidate costs a full extra packages.Load; real repos name
// a small handful of test-gating tags (integration, e2e, ...), so this is a
// safety cap against a pathological number of distinct tags, not a tuning knob.
const maxTestBuildTagCandidates = 20

// widenWithTestBuildTags re-loads dir with build tags discovered in *_test.go
// files force-enabled, but only after confirming each candidate tag doesn't
// introduce a build error the untagged baseline didn't already have — a tag
// can just as easily gate a *production* file (a compile-time feature toggle
// between two competing implementations) as a test file, and enabling it
// blindly can silently take down the entire service's semantic pass instead
// of just adding the intended test edges. basePkgs is the already-successful
// untagged load, used both as the error baseline and as the safe fallback.
// Returns (nil, false) when no candidate widens the build cleanly, in which
// case the caller keeps using basePkgs.
func widenWithTestBuildTags(dir string, fset *token.FileSet, basePkgs []*packages.Package) ([]*packages.Package, bool) {
	candidates := discoverTestBuildTags(dir)
	if len(candidates) == 0 || len(candidates) > maxTestBuildTagCandidates {
		return nil, false
	}
	baseErrs := packageErrorSet(basePkgs)

	var safe []string
	for _, tag := range candidates {
		trialCfg := &packages.Config{
			Mode:       packages.LoadAllSyntax,
			Dir:        dir,
			Fset:       token.NewFileSet(), // throwaway: only error text is inspected
			Tests:      true,
			BuildFlags: []string{"-tags=" + tag},
		}
		trialPkgs, err := packages.Load(trialCfg, "./...")
		if err != nil {
			continue
		}
		if isErrorSubset(packageErrorSet(trialPkgs), baseErrs) {
			safe = append(safe, tag)
		}
	}
	if len(safe) == 0 {
		return nil, false
	}

	finalCfg := &packages.Config{
		Mode:       packages.LoadAllSyntax,
		Dir:        dir,
		Fset:       fset,
		Tests:      true,
		BuildFlags: []string{"-tags=" + strings.Join(safe, ",")},
	}
	finalPkgs, err := packages.Load(finalCfg, "./...")
	if err != nil || !isErrorSubset(packageErrorSet(finalPkgs), baseErrs) {
		// Individually-safe tags can still conflict in combination (e.g. two
		// mutually exclusive opt-in features); bail to the known-good baseline
		// rather than risk a worse build than not widening at all.
		return nil, false
	}
	return finalPkgs, true
}

// packageErrorSet collects every packages.Package error as its formatted
// string ("file:line:col: message"), which is stable across separate Load
// calls using different token.FileSets (unlike the underlying positions).
func packageErrorSet(pkgs []*packages.Package) map[string]bool {
	set := make(map[string]bool)
	for _, p := range pkgs {
		for _, e := range p.Errors {
			set[e.Error()] = true
		}
	}
	return set
}

// isErrorSubset reports whether every error in sub also appears in super.
func isErrorSubset(sub, super map[string]bool) bool {
	for e := range sub {
		if !super[e] {
			return false
		}
	}
	return true
}

// reservedBuildTags are tags the Go toolchain manages implicitly (GOOS,
// GOARCH, and standard toolchain flags). Force-enabling one of these via
// discoverTestBuildTags could pull in platform-specific files that don't
// match the host, so they're never added to BuildFlags even if a _test.go
// file's constraint happens to name one.
var reservedBuildTags = map[string]bool{
	// GOOS
	"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true,
	"hurd": true, "illumos": true, "ios": true, "js": true, "linux": true, "nacl": true,
	"netbsd": true, "openbsd": true, "plan9": true, "solaris": true, "wasip1": true,
	"windows": true, "zos": true,
	// GOARCH
	"386": true, "amd64": true, "amd64p32": true, "arm": true, "armbe": true, "arm64": true,
	"arm64be": true, "loong64": true, "mips": true, "mipsle": true, "mips64": true,
	"mips64le": true, "mips64p32": true, "mips64p32le": true, "ppc": true, "ppc64": true,
	"ppc64le": true, "riscv": true, "riscv64": true, "s390": true, "s390x": true,
	"sparc": true, "sparc64": true, "wasm": true,
	// Toolchain / implicit
	"cgo": true, "race": true, "msan": true, "asan": true, "purego": true,
	"boringcrypto": true, "netgo": true, "netcgo": true, "osusergo": true,
	"unix": true, "gc": true, "gccgo": true,
}

// discoverTestBuildTags scans every *_test.go file under dir for a leading
// `//go:build` or legacy `// +build` constraint and collects the positive
// (non-negated) tag identifiers it names, skipping reservedBuildTags and Go
// version tags ("go1.21"). Negated tags (`!foo`) are skipped: enabling `foo`
// would flip a file that's included by default (because foo is undefined)
// to excluded, which is a regression, not an improvement.
//
// Best-effort: only the leading comment block of each file is scanned, and a
// malformed or unparsable constraint line is silently skipped — this exists
// to widen recall, so any file it fails to help with is no worse off than
// before the scan existed.
func discoverTestBuildTags(dir string) []string {
	tagSet := map[string]bool{}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "package ") {
				break // constraints only ever precede the package clause
			}
			if !constraint.IsGoBuild(line) && !constraint.IsPlusBuild(line) {
				continue
			}
			expr, perr := constraint.Parse(line)
			if perr != nil {
				continue
			}
			collectPositiveTags(expr, tagSet)
		}
		return nil
	})
	tags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		if reservedBuildTags[t] || strings.HasPrefix(t, "go1.") {
			continue
		}
		tags = append(tags, t)
	}
	sort.Strings(tags)
	return tags
}

// collectPositiveTags walks a build constraint expression tree, adding every
// tag that appears outside a NotExpr to tagSet.
func collectPositiveTags(expr constraint.Expr, tagSet map[string]bool) {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		tagSet[e.Tag] = true
	case *constraint.AndExpr:
		collectPositiveTags(e.X, tagSet)
		collectPositiveTags(e.Y, tagSet)
	case *constraint.OrExpr:
		collectPositiveTags(e.X, tagSet)
		collectPositiveTags(e.Y, tagSet)
	case *constraint.NotExpr:
		// Skip: forcing a negated tag true would exclude a file that's
		// currently included by default.
	}
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

// sseConstructors returns the set of in-service functions that yield a Datastar
// SSE response — either they *are* datastar.NewSSE (external, never in the set)
// or they are an in-service one-hop forwarder that calls datastar.NewSSE and
// returns its *datastar.ServerSentEventGenerator result (e.g. views.NewSSE(c)).
// WS.1: handlers calling such a wrapper cannot be flagged on the direct
// isDatastarNewSSE check. The return-type guard keeps an unrelated helper that
// merely references datastar from being swept in.
func sseConstructors(inService map[*ssa.Function]bool) map[*ssa.Function]bool {
	out := make(map[*ssa.Function]bool)
	for fn := range inService {
		if !sseForwarderReturnType(fn) || !fnCallsDatastarNewSSE(fn) {
			continue
		}
		out[fn] = true
	}
	return out
}

// sseForwarderReturnType reports whether fn returns a
// *datastar.ServerSentEventGenerator (matched structurally on the type name so it
// holds across datastar-go versions / vendored forks).
func sseForwarderReturnType(fn *ssa.Function) bool {
	if fn.Signature == nil {
		return false
	}
	res := fn.Signature.Results()
	for i := 0; i < res.Len(); i++ {
		if strings.Contains(res.At(i).Type().String(), "ServerSentEventGenerator") {
			return true
		}
	}
	return false
}

// fnCallsDatastarNewSSE reports whether fn's body contains a direct call to
// datastar.NewSSE.
func fnCallsDatastarNewSSE(fn *ssa.Function) bool {
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			c, ok := instr.(ssa.CallInstruction)
			if !ok {
				continue
			}
			if callee, ok := c.Common().Value.(*ssa.Function); ok && isDatastarNewSSE(callee) {
				return true
			}
		}
	}
	return false
}

// isDatastarPatchRender reports whether a method name is a datastar-go
// fragment/element patch that renders a templ component passed as an argument
// (WS.2), rather than via Component.Render(ctx, w). Signal/JS patch methods
// (MarshalAndPatchSignals, PatchSignals, ExecuteScript) carry no templ fragment
// and are deliberately excluded.
func isDatastarPatchRender(name string) bool {
	switch name {
	case "PatchElementTempl", "PatchElements", "MergeFragmentTempl", "MergeFragments":
		return true
	}
	return false
}

// callMethodName returns the method/function name for an SSA call, whether it is
// an interface invoke (Method set) or a static call (Value is the *ssa.Function).
func callMethodName(c *ssa.CallCommon) string {
	if c.Method != nil {
		return c.Method.Name()
	}
	if fn, ok := c.Value.(*ssa.Function); ok {
		return fn.Name()
	}
	return ""
}

// ssaArgFunc extracts an *ssa.Function from an SSA argument value, resolving
// through ChangeType wrappers that Go emits when a plain function literal is
// passed where a named function type is expected (e.g. `handlerFunc` in
// writefreely). Only ChangeType is unwrapped — Convert is for numeric/pointer
// conversions and won't wrap function values; MakeInterface would lose type
// information and is not unwrapped here (interface-wrapped funcs stay silent).
func ssaArgFunc(v ssa.Value) *ssa.Function {
	for {
		switch a := v.(type) {
		case *ssa.Function:
			return a
		case *ssa.MakeClosure:
			if fn, ok := a.Fn.(*ssa.Function); ok {
				return fn
			}
			return nil
		case *ssa.ChangeType:
			v = a.X // unwrap and retry
		default:
			return nil
		}
	}
}

// matchesInvoke returns true if fn satisfies the interface method described by call.
// Matching by method name alone fans out to every same-named method across the
// whole service (e.g. two unrelated types both declaring `Do()`), which is a
// real false-positive source: name-only matching linked call sites to methods
// that could never actually be invoked there. The static interface type of the
// call is available from the type checker (`call.Value.Type()`), so candidates
// are additionally required to have a receiver type that actually implements
// that interface — a real disambiguation, not a heuristic, and strictly
// narrower than before (never introduces new matches, only removes false ones).
func matchesInvoke(call *ssa.CallCommon, fn *ssa.Function) bool {
	if fn.Signature.Recv() == nil {
		return false
	}
	if fn.Name() != call.Method.Name() {
		return false
	}
	iface, ok := call.Value.Type().Underlying().(*types.Interface)
	if !ok {
		// Can't narrow further without a static interface type; keep prior
		// name-only behavior rather than dropping the edge.
		return true
	}
	recvType := fn.Signature.Recv().Type()
	if types.Implements(recvType, iface) {
		return true
	}
	if _, isPtr := recvType.(*types.Pointer); !isPtr && types.Implements(types.NewPointer(recvType), iface) {
		return true
	}
	return false
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
	// service packages that import the same external package. The synthetic
	// interface node is NOT minted here — only when an in-service struct
	// actually satisfies the interface (see satisfaction branch below), so
	// unimplemented external interfaces leave no dangling stub node.
	type extIfaceEntry struct {
		iface   *types.Interface
		pkgPath string
		name    string
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
					seenExtIface[key] = extIfaceEntry{
						iface: iface, pkgPath: imp.Path(), name: name,
					}
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

			// External interfaces (collected above across all packages). The
			// synthetic node is minted lazily here — only on satisfaction — so
			// an external interface that nothing implements never becomes a
			// dangling stub node.
			for _, entry := range seenExtIface {
				if types.Implements(T, entry.iface) || types.Implements(ptrT, entry.iface) {
					nodeID := syntheticIfaceID(entry.pkgPath, entry.name)
					addEdge(structID, nodeID, map[string]string{
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
