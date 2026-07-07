package parser

import (
	"fmt"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// TemplParser parses .templ files (a-h/templ Go HTML templating).
type TemplParser struct{}

func (p *TemplParser) Language() string     { return "templ" }
func (p *TemplParser) Extensions() []string { return []string{".templ"} }

// Parse extracts templ component definitions and render calls.
func (p *TemplParser) Parse(file string) ([]graph.Node, []graph.Edge, error) {
	// TODO: parse templ components using tree-sitter or regex
	_ = file
	return nil, nil, fmt.Errorf("templ parser: not yet implemented")
}

func init() {
	Register(&TemplParser{})
}
