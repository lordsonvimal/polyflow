package parser

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// Parser extracts nodes and edges from a single source file.
type Parser interface {
	Language() string
	Extensions() []string
	Parse(file, service string, matcher *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, error)
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

// Result holds the parsed output or error for a single file.
type Result struct {
	File  string
	Nodes []graph.Node
	Edges []graph.Edge
	Err   error
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
			nodes, edges, err := p.Parse(path, wp.service, wp.matcher)
			out <- Result{File: path, Nodes: nodes, Edges: edges, Err: err}
		}(f)
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	return out
}
