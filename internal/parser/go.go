package parser

import (
	"fmt"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// GoParser parses Go source files.
type GoParser struct{}

func (p *GoParser) Language() string     { return "go" }
func (p *GoParser) Extensions() []string { return []string{".go"} }

// Parse extracts HTTP handlers, clients, function calls, etc. from a Go file.
func (p *GoParser) Parse(file string) ([]graph.Node, []graph.Edge, error) {
	// TODO: use go-tree-sitter to parse the file and match patterns
	_ = file
	return nil, nil, fmt.Errorf("go parser: not yet implemented")
}

func init() {
	Register(&GoParser{})
}
