package parser

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// JavaScriptParser parses JavaScript and TypeScript source files.
type JavaScriptParser struct{}

func (p *JavaScriptParser) Language() string { return "javascript" }
func (p *JavaScriptParser) Extensions() []string {
	return []string{".js", ".ts", ".jsx", ".tsx", ".mjs"}
}

func (p *JavaScriptParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}

	grammarLang := grammarLanguage(file)
	// Language tag for nodes: tsx/jsx files are still "typescript"/"javascript" at the language level.
	langTag := tsLanguage(file)

	// For TypeScript/TSX files, run both javascript patterns (fetch, axios, etc.)
	// and typescript-specific patterns (interfaces, type annotations).
	// JS patterns use the TypeScript/TSX grammar since those are supersets.
	patternLangs := []string{"javascript"}
	if grammarLang == "typescript" || grammarLang == "tsx" {
		patternLangs = append(patternLangs, "typescript")
	}
	if grammarLang == "tsx" {
		// Full JSX pattern set: component renders queries (jsx_opening_element etc.)
		// require the TSX grammar and only run for .tsx/.jsx files.
		patternLangs = append(patternLangs, "jsx")
	} else {
		// For .ts/.js files, run only the call-ref patterns (component_fn_call),
		// which use only call_expression + identifier and compile against any grammar.
		patternLangs = append(patternLangs, "jsx_calls")
	}

	// Collect all match results across pattern languages before building the graph.
	// This ensures MatchToGraph sees function nodes and JSX usage nodes together,
	// so proximity-based edge linking works across pattern sets.
	var allResults []patterns.MatchResult
	for _, patLang := range patternLangs {
		results, matchErr := matcher.MatchWithGrammar(patLang, grammarLang, file, src)
		if matchErr != nil && err == nil {
			err = matchErr
		}
		allResults = append(allResults, results...)
	}

	nodes, edges := patterns.MatchToGraph(service, allResults)
	setLanguage(nodes, langTag)
	return nodes, edges, err
}

// grammarLanguage returns the tree-sitter grammar name for a file extension.
// .tsx/.jsx use the "tsx" grammar (JSX-aware superset of TypeScript/JavaScript).
// .ts uses "typescript". Everything else uses "javascript".
func grammarLanguage(file string) string {
	ext := strings.ToLower(filepath.Ext(file))
	switch ext {
	case ".tsx", ".jsx":
		return "tsx"
	case ".ts":
		return "typescript"
	default:
		return "javascript"
	}
}

// tsLanguage returns "typescript" for .ts/.tsx files, "javascript" otherwise.
// Kept for backward compatibility with language tagging.
func tsLanguage(file string) string {
	ext := strings.ToLower(filepath.Ext(file))
	if ext == ".ts" || ext == ".tsx" {
		return "typescript"
	}
	return "javascript"
}

func init() {
	Register(&JavaScriptParser{})
}
