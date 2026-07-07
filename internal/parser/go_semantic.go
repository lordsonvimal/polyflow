package parser

import (
	"fmt"
	"go/token"
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

	prog, _ := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	// Build file+name → nodeID index from known tree-sitter nodes.
	// Key: "file\x00name" (both function and method are stored; we try both types).
	nodeByFileAndName := make(map[string]string, len(knownNodes))
	for id := range knownNodes {
		// ID format: service:file:type:name:line
		parts := strings.SplitN(id, ":", 5)
		if len(parts) != 5 {
			continue
		}
		file, name := parts[1], parts[3]
		key := file + "\x00" + name
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
		// Strip anonymous suffixes like "$1".
		name := fn.Name()
		if idx := strings.Index(name, "$"); idx >= 0 {
			name = name[:idx]
		}
		if name == "" {
			return "", false
		}
		key := pos.Filename + "\x00" + name
		id, ok := nodeByFileAndName[key]
		return id, ok
	}

	// Collect in-service functions.
	allFns := ssautil.AllFunctions(prog)
	inService := make(map[*ssa.Function]bool)
	for fn := range allFns {
		if isServiceFunc(fn, dir, fset) {
			inService[fn] = true
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

	return SemanticResult{Edges: edges}
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
	return strings.HasPrefix(pos.Filename, serviceDir)
}

// matchesInvoke returns true if fn satisfies the interface method described by call.
// We match by method name only — a lightweight approximation sufficient for v1.5.
func matchesInvoke(call *ssa.CallCommon, fn *ssa.Function) bool {
	if fn.Signature.Recv() == nil {
		return false
	}
	return fn.Name() == call.Method.Name()
}

// funcNodeID returns the graph node ID for an SSA function, matching the format
// produced by MatchToGraph: service:absoluteFilePath:type:funcName:line.
// Returns false if the position is not resolvable to a source file under the service.
func funcNodeID(service string, fn *ssa.Function, fset *token.FileSet) (string, bool) {
	pos := fset.Position(fn.Pos())
	if !pos.IsValid() || pos.Filename == "" {
		return "", false
	}

	name := fn.Name()
	// Strip anonymous-function suffixes like "$1", "$2".
	if idx := strings.Index(name, "$"); idx >= 0 {
		name = name[:idx]
	}
	if name == "" {
		return "", false
	}

	nodeType := graph.NodeTypeFunction
	if fn.Signature.Recv() != nil {
		nodeType = graph.NodeTypeMethod
	}

	id := fmt.Sprintf("%s:%s:%s:%s:%d", service, pos.Filename, string(nodeType), name, pos.Line)
	return id, true
}
