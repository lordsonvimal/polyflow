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
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return SemanticResult{
			Warning: fmt.Sprintf("go/packages load failed for service %q: %v — falling back to tree-sitter call edges", service, err),
		}
	}
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
	for id := range knownNodes {
		// ID format: service:file:type:name:line
		parts := strings.SplitN(id, ":", 5)
		if len(parts) != 5 {
			continue
		}
		file, name := parts[1], parts[3]
		if parts[2] == string(graph.NodeTypeWorker) {
			workerByFileLine[canonicalPath(file)+"\x00"+parts[4]] = id
			continue
		}
		key := canonicalPath(file) + "\x00" + name
		// Prefer the first match; for Go, name is unique per file in practice.
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

		for _, b := range caller.Blocks {
			for _, instr := range b.Instrs {
				var callees []*ssa.Function

				switch c := instr.(type) {
				case ssa.CallInstruction:
					common := c.Common()
					if common.IsInvoke() {
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

	// Variable-tracking layer: package globals/consts, structs, mutations,
	// closure captures, by-ref/by-value flow (reuses this SSA build).
	varNodes, varEdges := extractVariables(ssaPkgs, dir, service, fset, inService, resolveFunc)
	edges = append(edges, varEdges...)

	referenced := collectReferenced(prog, ssaPkgs, allFns, resolveFunc)

	return SemanticResult{Nodes: varNodes, Edges: edges, Referenced: referenced}
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

// matchesInvoke returns true if fn satisfies the interface method described by call.
// We match by method name only — a lightweight approximation sufficient for v1.5.
func matchesInvoke(call *ssa.CallCommon, fn *ssa.Function) bool {
	if fn.Signature.Recv() == nil {
		return false
	}
	return fn.Name() == call.Method.Name()
}
