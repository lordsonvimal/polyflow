package parser

import (
	"fmt"
	"path/filepath"
	"sync"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Parser extracts nodes and edges from a single source file.
type Parser interface {
	// Language returns the language this parser handles (e.g. "go", "javascript").
	Language() string
	// Extensions returns the file extensions this parser handles.
	Extensions() []string
	// Parse parses a file and returns discovered nodes and edges.
	Parse(file string) ([]graph.Node, []graph.Edge, error)
}

// Registry maps file extensions to parsers.
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

// ParseFile dispatches to the correct parser for the given file.
func ParseFile(path string) ([]graph.Node, []graph.Edge, error) {
	p := ForFile(path)
	if p == nil {
		return nil, nil, fmt.Errorf("no parser for %s", path)
	}
	return p.Parse(path)
}

// WorkerPool fans out file parsing across multiple goroutines.
type WorkerPool struct {
	workers int
}

// NewWorkerPool creates a pool with the given concurrency.
func NewWorkerPool(workers int) *WorkerPool {
	if workers <= 0 {
		workers = 4
	}
	return &WorkerPool{workers: workers}
}

// Result holds the parsed output or error for a single file.
type Result struct {
	File  string
	Nodes []graph.Node
	Edges []graph.Edge
	Err   error
}

// Run parses all files concurrently and streams results on the returned channel.
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
			nodes, edges, err := ParseFile(path)
			out <- Result{File: path, Nodes: nodes, Edges: edges, Err: err}
		}(f)
	}

	go func() {
		wg.Wait()
		close(out)
	}()

	return out
}
