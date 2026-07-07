package patterns

import (
	"fmt"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// MatchResult holds a single pattern match against source code.
type MatchResult struct {
	PatternName string
	NodeID      string
	Captures    map[string]string // capture name -> matched text
	Line        int
	File        string
}

// Matcher runs tree-sitter queries against source files.
type Matcher interface {
	// Match runs all patterns for the given language against src and returns matches.
	Match(language, file string, src []byte) ([]MatchResult, error)
	// MatchToNodes converts match results into graph nodes and edges.
	MatchToNodes(service string, results []MatchResult) ([]graph.Node, []graph.Edge)
}

// TreeSitterMatcher implements Matcher using go-tree-sitter.
type TreeSitterMatcher struct {
	registry *Registry
}

// NewTreeSitterMatcher creates a matcher backed by the given registry.
func NewTreeSitterMatcher(reg *Registry) *TreeSitterMatcher {
	return &TreeSitterMatcher{registry: reg}
}

// Match runs registered patterns for the language against the source bytes.
func (m *TreeSitterMatcher) Match(language, file string, src []byte) ([]MatchResult, error) {
	patterns := m.registry.List(language)
	if len(patterns) == 0 {
		return nil, nil
	}
	// TODO: initialize tree-sitter parser for language, run each query
	_ = patterns
	return nil, fmt.Errorf("tree-sitter matcher: not yet implemented for %s", language)
}

// MatchToNodes converts raw match results into typed graph nodes and edges.
func (m *TreeSitterMatcher) MatchToNodes(service string, results []MatchResult) ([]graph.Node, []graph.Edge) {
	// TODO: map pattern names to NodeTypes, derive edges from captures
	return nil, nil
}
