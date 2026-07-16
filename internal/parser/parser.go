package parser

import (
	"fmt"
	"go/token"
	"path/filepath"
	"sync"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// ServiceAnalyzer runs whole-service semantic analysis (not per-file).
// This is used for languages like Go where type-resolved analysis requires
// loading the full package graph (go/packages).
// knownNodes is the set of node IDs already written by tree-sitter; the
// analyzer uses it to resolve SSA functions to existing nodes.
type ServiceAnalyzer interface {
	Language() string
	AnalyzeService(dir, service string, fset *token.FileSet, knownNodes map[string]bool) SemanticResult
}

// SemanticResult holds the output of a whole-service semantic analysis pass.
// Nodes are entities only semantics can see (tracked variables, structs);
// Edges reference either tree-sitter node IDs or these semantic nodes.
type SemanticResult struct {
	Nodes   []graph.Node
	Edges   []graph.Edge
	Warning string // non-empty when falling back to tree-sitter accuracy
	// Referenced lists node IDs of functions/methods that are referenced
	// without being called in-service: function values passed to other code
	// (cobra RunE, http.HandlerFunc) and methods satisfying an interface of
	// an external package (framework callbacks like templ's Visitor). Roots
	// in this set classify as "callback" rather than "unreachable".
	Referenced []string
}

var serviceAnalyzerRegistry = map[string]ServiceAnalyzer{}

// RegisterServiceAnalyzer adds a ServiceAnalyzer to the global registry.
func RegisterServiceAnalyzer(a ServiceAnalyzer) {
	serviceAnalyzerRegistry[a.Language()] = a
}

// ServiceAnalyzerFor returns the ServiceAnalyzer for the given language, or nil.
func ServiceAnalyzerFor(lang string) ServiceAnalyzer {
	return serviceAnalyzerRegistry[lang]
}

// Parser extracts nodes and edges from a single source file.
type Parser interface {
	Language() string
	Extensions() []string
	// Parse returns the file's nodes and edges plus any references it could
	// not resolve (the recall gauge's per-file input).
	Parse(file, service string, matcher *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error)
}

// registry maps file extensions to parsers.
var registry = map[string]Parser{}

// Register adds a parser to the global registry.
func Register(p Parser) {
	for _, ext := range p.Extensions() {
		registry[ext] = p
	}
}

// ForFile returns the parser for the given file path, or nil if unsupported.
func ForFile(path string) Parser {
	ext := filepath.Ext(path)
	return registry[ext]
}

// RegisteredLanguages returns the deduplicated list of Language() values for
// all registered parsers. Used by the doctor walker-coverage row.
func RegisteredLanguages() []string {
	seen := map[string]bool{}
	var langs []string
	for _, p := range registry {
		lang := p.Language()
		if !seen[lang] {
			seen[lang] = true
			langs = append(langs, lang)
		}
	}
	return langs
}

// Result holds the parsed output or error for a single file.
type Result struct {
	File       string
	Nodes      []graph.Node
	Edges      []graph.Edge
	Unresolved []graph.UnresolvedRef
	Err        error
}

// WorkerPool fans out file parsing across multiple goroutines.
type WorkerPool struct {
	workers int
	matcher *patterns.TreeSitterMatcher
	service string
}

// NewWorkerPool creates a pool with the given concurrency, matcher, and service name.
// service is used as a namespace prefix in generated node IDs.
func NewWorkerPool(workers int, matcher *patterns.TreeSitterMatcher, service string) *WorkerPool {
	if workers <= 0 {
		workers = 4
	}
	return &WorkerPool{workers: workers, matcher: matcher, service: service}
}

// setLanguage stamps the Language field on a slice of nodes.
func setLanguage(nodes []graph.Node, lang string) {
	for i := range nodes {
		nodes[i].Language = lang
	}
}

// Run parses all files concurrently and streams results on the returned channel.
// Files with no registered parser produce a Result with Err set and no nodes/edges.
func (wp *WorkerPool) Run(files []string) <-chan Result {
	out := make(chan Result, len(files))
	sem := make(chan struct{}, wp.workers)
	var wg sync.WaitGroup

	for _, f := range files {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			p := ForFile(path)
			if p == nil {
				out <- Result{File: path, Err: fmt.Errorf("no parser for %s", path)}
				return
			}
			nodes, edges, unresolved, err := p.Parse(path, wp.service, wp.matcher)
			out <- Result{File: path, Nodes: nodes, Edges: edges, Unresolved: unresolved, Err: err}
		}(f)
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	return out
}
