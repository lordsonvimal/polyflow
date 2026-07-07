package parser

import (
	"fmt"
	"go/token"
	"strings"

	"golang.org/x/tools/go/callgraph"
	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

func init() {
	RegisterServiceAnalyzer(&GoSemanticAnalyzer{})
}

// GoSemanticAnalyzer builds a type-resolved call graph for a Go service directory
// using golang.org/x/tools (go/packages + RTA). It operates at the service level,
// not per-file, because the type checker needs the full package graph.
type GoSemanticAnalyzer struct{}

func (a *GoSemanticAnalyzer) Language() string { return "go" }

// AnalyzeService loads all packages under dir, builds an SSA program, runs RTA,
// and returns call edges between functions. Node IDs match the format produced by
// MatchToGraph after the label-based ID fix: service:file:type:name:line.
func (a *GoSemanticAnalyzer) AnalyzeService(dir, service string, fset *token.FileSet) SemanticResult {
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

	// Collect all non-synthetic functions as RTA roots.
	var roots []*ssa.Function
	for fn := range ssautil.AllFunctions(prog) {
		if fn.Synthetic != "" || fn.Package() == nil {
			continue
		}
		roots = append(roots, fn)
	}
	if len(roots) == 0 {
		return SemanticResult{}
	}

	result := rta.Analyze(roots, true)

	var edges []graph.Edge
	result.CallGraph.DeleteSyntheticNodes()

	callgraph.GraphVisitEdges(result.CallGraph, func(e *callgraph.Edge) error {
		caller := e.Caller.Func
		callee := e.Callee.Func

		// Skip synthetic wrappers, closures in init, and external (no source) functions.
		if caller.Synthetic != "" || callee.Synthetic != "" {
			return nil
		}
		callerPos := caller.Pos()
		calleePos := callee.Pos()
		if !callerPos.IsValid() || !calleePos.IsValid() {
			return nil
		}

		callerID, ok := funcNodeID(service, caller, fset)
		if !ok {
			return nil
		}
		calleeID, ok := funcNodeID(service, callee, fset)
		if !ok {
			return nil
		}
		if callerID == calleeID {
			return nil
		}

		edgeID := fmt.Sprintf("semantic:calls:%s->%s", callerID, calleeID)
		edges = append(edges, graph.Edge{
			ID:   edgeID,
			From: callerID,
			To:   calleeID,
			Type: graph.EdgeTypeCalls,
		})
		return nil
	})

	return SemanticResult{Edges: edges}
}

// funcNodeID returns the graph node ID for an SSA function, matching the format
// produced by MatchToGraph: service:absoluteFilePath:type:funcName:line.
// Returns false if the position is not resolvable to a source file.
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
