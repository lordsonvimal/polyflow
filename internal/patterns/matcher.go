package patterns

import (
	"context"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"

	sitter "github.com/smacker/go-tree-sitter"
	gositter "github.com/smacker/go-tree-sitter/golang"
	jssitter "github.com/smacker/go-tree-sitter/javascript"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"
	tssitter "github.com/smacker/go-tree-sitter/typescript/typescript"

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

// compiledQuery holds a compiled tree-sitter query and the original pattern.
type compiledQuery struct {
	query   *sitter.Query
	pattern *Pattern
}

// TreeSitterMatcher implements Matcher using go-tree-sitter.
type TreeSitterMatcher struct {
	registry *Registry
	mu       sync.Mutex
	// compiled queries cached per language: language -> patternName -> compiledQuery
	compiled map[string][]compiledQuery
}

// NewTreeSitterMatcher creates a matcher backed by the given registry.
func NewTreeSitterMatcher(reg *Registry) *TreeSitterMatcher {
	return &TreeSitterMatcher{
		registry: reg,
		compiled: make(map[string][]compiledQuery),
	}
}

// languageFor returns the tree-sitter Language for the given language string.
func languageFor(lang string) *sitter.Language {
	switch lang {
	case "go":
		return gositter.GetLanguage()
	case "javascript":
		return jssitter.GetLanguage()
	case "typescript":
		return tssitter.GetLanguage()
	case "ruby":
		return rubysitter.GetLanguage()
	default:
		return nil
	}
}

// getCompiledQueries returns cached compiled queries for a language, compiling them if needed.
func (m *TreeSitterMatcher) getCompiledQueries(language string, lang *sitter.Language) []compiledQuery {
	m.mu.Lock()
	defer m.mu.Unlock()

	if cqs, ok := m.compiled[language]; ok {
		return cqs
	}

	patterns := m.registry.List(language)
	cqs := make([]compiledQuery, 0, len(patterns))
	for _, p := range patterns {
		q, err := sitter.NewQuery([]byte(p.Query), lang)
		if err != nil {
			log.Printf("patterns: failed to compile query %q for language %q: %v", p.Name, language, err)
			continue
		}
		cqs = append(cqs, compiledQuery{query: q, pattern: p})
	}
	m.compiled[language] = cqs
	return cqs
}

// Match runs registered patterns for the language against the source bytes.
func (m *TreeSitterMatcher) Match(language, file string, src []byte) ([]MatchResult, error) {
	lang := languageFor(language)
	if lang == nil {
		// unknown language: return empty results, not an error
		return nil, nil
	}

	cqs := m.getCompiledQueries(language, lang)
	if len(cqs) == 0 {
		return nil, nil
	}

	// Parse the source
	root, err := sitter.ParseCtx(context.Background(), src, lang)
	if err != nil {
		return nil, fmt.Errorf("tree-sitter parse %s: %w", file, err)
	}

	var results []MatchResult

	for _, cq := range cqs {
		cursor := sitter.NewQueryCursor()
		cursor.Exec(cq.query, root)

		for {
			m2, ok := cursor.NextMatch()
			if !ok {
				break
			}
			// Apply predicate filtering (handles #eq? and #match? predicates)
			m2 = cursor.FilterPredicates(m2, src)
			if m2 == nil || len(m2.Captures) == 0 {
				continue
			}

			// Build capture map: capture name -> text
			captures := make(map[string]string, len(m2.Captures))
			var minLine int = -1
			for _, cap := range m2.Captures {
				name := cq.query.CaptureNameForId(cap.Index)
				text := cap.Node.Content(src)
				captures[name] = text
				row := int(cap.Node.StartPoint().Row) + 1 // 1-indexed
				if minLine < 0 || row < minLine {
					minLine = row
				}
			}

			// Apply Match filters if defined
			if len(cq.pattern.Match) > 0 {
				skip := false
				for capName, allowed := range cq.pattern.Match {
					val, ok := captures[capName]
					if !ok {
						skip = true
						break
					}
					if !slices.Contains(allowed, val) {
						skip = true
						break
					}
				}
				if skip {
					continue
				}
			}

			if minLine < 0 {
				minLine = 0
			}

			results = append(results, MatchResult{
				PatternName: cq.pattern.Name,
				Captures:    captures,
				Line:        minLine,
				File:        file,
			})
		}
	}

	return results, nil
}

// MatchToNodes converts raw match results into typed graph nodes and edges.
func (m *TreeSitterMatcher) MatchToNodes(service string, results []MatchResult) ([]graph.Node, []graph.Edge) {
	return MatchToGraph(service, results)
}

// MatchToGraph maps match results to graph nodes and edges.
func MatchToGraph(service string, results []MatchResult) ([]graph.Node, []graph.Edge) {
	nodes := make([]graph.Node, 0, len(results))
	edges := make([]graph.Edge, 0, len(results))

	for _, r := range results {
		nodeType, edgeType := classifyPattern(r.PatternName)

		nodeID := fmt.Sprintf("%s:%s:%d:%s", service, r.File, r.Line, r.PatternName)

		// Build label from captures
		label := r.PatternName
		if method, ok := r.Captures["method"]; ok {
			if url, ok2 := r.Captures["url"]; ok2 {
				label = fmt.Sprintf("%s %s", method, url)
			} else if path, ok2 := r.Captures["path"]; ok2 {
				label = fmt.Sprintf("%s %s", method, path)
			}
		} else if path, ok := r.Captures["path"]; ok {
			label = path
		}

		// Build meta from all captures
		meta := make(map[string]string, len(r.Captures))
		maps.Copy(meta, r.Captures)

		node := graph.Node{
			ID:      nodeID,
			Type:    nodeType,
			Label:   label,
			Service: service,
			File:    r.File,
			Line:    r.Line,
			Meta:    meta,
		}
		nodes = append(nodes, node)

		// Create a self-referencing edge to indicate the pattern was matched
		edgeID := fmt.Sprintf("%s:%s:%d:%s:edge", service, r.File, r.Line, r.PatternName)
		edge := graph.Edge{
			ID:    edgeID,
			From:  nodeID,
			To:    nodeID,
			Type:  edgeType,
			Label: string(edgeType),
			Meta:  meta,
		}
		edges = append(edges, edge)
	}

	return nodes, edges
}

// classifyPattern maps a pattern name to appropriate node and edge types.
func classifyPattern(patternName string) (graph.NodeType, graph.EdgeType) {
	lower := strings.ToLower(patternName)

	switch {
	case strings.Contains(lower, "handler") || strings.Contains(lower, "handle") || strings.Contains(lower, "route"):
		return graph.NodeTypeHTTPHandler, graph.EdgeTypeHTTPCall
	case strings.Contains(lower, "client") ||
		strings.Contains(lower, "request") ||
		strings.Contains(lower, "get") ||
		strings.Contains(lower, "post") ||
		strings.Contains(lower, "put") ||
		strings.Contains(lower, "delete") ||
		strings.Contains(lower, "fetch") ||
		strings.Contains(lower, "axios"):
		return graph.NodeTypeHTTPClient, graph.EdgeTypeHTTPCall
	case strings.Contains(lower, "publish"):
		return graph.NodeTypePublisher, graph.EdgeTypePublishes
	case strings.Contains(lower, "subscribe") || strings.Contains(lower, "consume"):
		return graph.NodeTypeSubscriber, graph.EdgeTypeSubscribes
	case strings.Contains(lower, "goroutine") || strings.Contains(lower, "spawn"):
		return graph.NodeTypeWorker, graph.EdgeTypeSpawns
	default:
		return graph.NodeTypeFunction, graph.EdgeTypeCalls
	}
}
