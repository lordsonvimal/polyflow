package parser

import (
	"fmt"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// JavaScriptParser parses JavaScript and TypeScript source files.
type JavaScriptParser struct{}

func (p *JavaScriptParser) Language() string     { return "javascript" }
func (p *JavaScriptParser) Extensions() []string { return []string{".js", ".ts", ".jsx", ".tsx", ".mjs"} }

// Parse extracts fetch/axios calls, DOM interactions, etc. from a JS/TS file.
func (p *JavaScriptParser) Parse(file string) ([]graph.Node, []graph.Edge, error) {
	// TODO: use go-tree-sitter with JavaScript grammar
	_ = file
	return nil, nil, fmt.Errorf("javascript parser: not yet implemented")
}

func init() {
	Register(&JavaScriptParser{})
}
