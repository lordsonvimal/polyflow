package parser

import (
	"path/filepath"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// JavaScriptParser parses JavaScript and TypeScript source files.
type JavaScriptParser struct{}

func (p *JavaScriptParser) Language() string { return "javascript" }
func (p *JavaScriptParser) Extensions() []string {
	// .es6 is plain ES2015 handled by the JS grammar unchanged (Tier K.5);
	// nextGen ships its per-page Sprockets assets under that extension.
	return []string{".js", ".ts", ".jsx", ".tsx", ".mjs", ".es6"}
}

func (p *JavaScriptParser) Parse(file, service string, matcher *patterns.TreeSitterMatcher, cache SourceCache) ([]graph.Node, []graph.Edge, []graph.UnresolvedRef, error) {
	src, err := readSource(file, cache)
	if err != nil {
		return nil, nil, nil, err
	}
	// `file` arrives absolute (needed for readSource/os.ReadFile); every node
	// this parser mints must carry the cwd-relative form instead, matching
	// the Go semantic pass's convention — extractJSVariables builds nodes
	// directly (bypassing the matcher's own relativization).
	file = patterns.RelativizeToCwd(file)

	grammarLang := patterns.DetectJSGrammar(file, src, grammarLanguage(file))
	// Language tag for nodes: tsx/jsx files are still "typescript"/"javascript" at the language level.
	// Deliberately NOT re-derived from grammarLang: a Flow-typed .js file
	// upgraded to the typescript grammar (DetectJSGrammar) is still
	// JavaScript source, not TypeScript — the tag describes what the file
	// IS, the grammar describes what parsed it.
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

	allResults = dropNonHTTPJSMatches(allResults)

	nodes, edges, unresolved := patterns.MatchToGraph(service, allResults)
	setLanguage(nodes, langTag)

	// Structural variable tracking: module vars, classes, reads/writes,
	// closure captures, flows. Lower confidence than the Go semantic pass.
	varNodes, varEdges, varUnresolved, jqListeners := extractJSVariables(file, service, langTag, grammarLang, src)

	// Tier K.4: hand the matcher's jQuery event nodes the handler the structural
	// pass just minted, so LinkDOMDefinitions can close element→handler once it
	// has resolved the selector. Runs before the append so it only rewrites
	// matcher output, mirroring resolveRubyQueueKeys in ruby.go.
	stampJQueryHandlers(nodes, jqListeners)
	edges = reattributeJQueryHandlers(nodes, edges, jqListeners)

	nodes = append(nodes, varNodes...)
	edges = append(edges, varEdges...)
	unresolved = append(unresolved, varUnresolved...)
	return nodes, edges, unresolved, err
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
