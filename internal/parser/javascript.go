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

	grammarLang := tsLanguage(file)

	// For TypeScript files, run both javascript patterns (fetch, axios, etc.)
	// and typescript-specific patterns (interfaces, type annotations).
	// JS patterns use the TypeScript grammar since TS is a superset.
	patternLangs := []string{"javascript"}
	if grammarLang == "typescript" {
		patternLangs = append(patternLangs, "typescript")
	}

	var allNodes []graph.Node
	var allEdges []graph.Edge
	for _, patLang := range patternLangs {
		results, matchErr := matcher.MatchWithGrammar(patLang, grammarLang, file, src)
		if matchErr != nil && err == nil {
			err = matchErr
		}
		nodes, edges := patterns.MatchToGraph(service, results)
		setLanguage(nodes, grammarLang)
		allNodes = append(allNodes, nodes...)
		allEdges = append(allEdges, edges...)
	}
	return allNodes, allEdges, err
}

// tsLanguage returns "typescript" for .ts/.tsx files, "javascript" otherwise.
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
