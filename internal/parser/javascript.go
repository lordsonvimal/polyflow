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
	lang := tsLanguage(file)
	results, err := matcher.Match(lang, file, src)
	if err != nil {
		nodes, edges := patterns.MatchToGraph(service, results)
		setLanguage(nodes, lang)
		return nodes, edges, err
	}
	nodes, edges := patterns.MatchToGraph(service, results)
	setLanguage(nodes, lang)
	return nodes, edges, nil
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
