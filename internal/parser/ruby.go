package parser

import (
	"fmt"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// RubyParser parses Ruby source files.
type RubyParser struct{}

func (p *RubyParser) Language() string     { return "ruby" }
func (p *RubyParser) Extensions() []string { return []string{".rb", ".rake"} }

// Parse extracts Rails routes, HTTP clients, Sidekiq workers, etc. from a Ruby file.
func (p *RubyParser) Parse(file string) ([]graph.Node, []graph.Edge, error) {
	// TODO: use go-tree-sitter with Ruby grammar
	_ = file
	return nil, nil, fmt.Errorf("ruby parser: not yet implemented")
}

func init() {
	Register(&RubyParser{})
}
