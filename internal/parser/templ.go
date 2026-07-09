package parser

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
)

// TemplParser parses .templ files (a-h/templ HTML templating).
// It uses regex-based scanning because a-h/templ/parser/v2 is not yet a dependency.
type TemplParser struct{}

func (p *TemplParser) Language() string     { return "templ" }
func (p *TemplParser) Extensions() []string { return []string{".templ"} }

// Patterns for Datastar HTML attributes and component/template declarations.
var (
	reDataOnAction = regexp.MustCompile(`data-on-\w+\s*=\s*["']@(get|post|put|delete|patch)\s*\(\s*["']([^"']+)["']`)
	reDataBind     = regexp.MustCompile(`data-(?:bind|signals|model)\s*=\s*["']([^"']+)["']`)
	reDataText     = regexp.MustCompile(`data-(?:text|indicator)\s*=\s*["'](\$[A-Za-z_]\w*)["']`)
	// [^"']* (zero-or-more) so href="/" (root path) is captured correctly.
	reHrefAction = regexp.MustCompile(`(?:href|action)\s*=\s*["'](\/[^"']*)["']`)
	// Only `templ` keyword — not `func` — to avoid matching regular exported Go
	// helper functions that are also legal inside .templ files.
	reTemplComponent = regexp.MustCompile(`^templ\s+([A-Z][A-Za-z0-9_]*)\s*\(`)
)

func (p *TemplParser) Parse(file, service string, _ *patterns.TreeSitterMatcher) ([]graph.Node, []graph.Edge, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, err
	}

	var nodes []graph.Node
	var edges []graph.Edge

	// currentComponentID tracks the most recently declared templ component so
	// attribute nodes can be attached to it with a real edge instead of a self-loop.
	currentComponentID := ""

	scanner := bufio.NewScanner(bytes.NewReader(src))
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// templ component declaration — `templ ComponentName(` only.
		// Explicitly excludes regular Go `func` declarations that are also
		// legal inside .templ files and would otherwise be false positives.
		if m := reTemplComponent.FindStringSubmatch(trimmed); m != nil {
			nodeID := templNodeID(service, file, lineNum, graph.NodeTypeComponent, m[1])
			nodes = append(nodes, graph.Node{
				ID: nodeID, Type: graph.NodeTypeComponent,
				Label: m[1], Service: service, File: file, Line: lineNum,
				Language: "templ",
				Meta:     map[string]string{"name": m[1]},
			})
			currentComponentID = nodeID
			// A component declaration line cannot also contain data-on-* or href
			// attributes, so skip the remaining attribute checks for this line.
			continue
		}

		// Attribute patterns are NOT mutually exclusive on a single line:
		// e.g. `<a href="/path" data-on-click="@get('/api')">` must emit
		// both a datastar_action node and an href_link node.
		// Each pattern is therefore checked independently (no continue between them).

		// data-on-<event>="@method('/path')"
		if m := reDataOnAction.FindStringSubmatch(trimmed); m != nil {
			method := strings.ToUpper(m[1])
			path := m[2]
			nodeID := templNodeID(service, file, lineNum, graph.NodeTypeHTTPClient, method+":"+path)
			nodes = append(nodes, graph.Node{
				ID: nodeID, Type: graph.NodeTypeHTTPClient,
				Label: fmt.Sprintf("%s %s", method, path), Service: service, File: file, Line: lineNum,
				Language: "templ",
				Meta:     map[string]string{"method": method, "path": path, "datastar": "true"},
			})
			edges = append(edges, componentEdge(currentComponentID, nodeID, graph.EdgeTypeDatastarAction))
		}

		// data-bind / data-signals / data-model
		if m := reDataBind.FindStringSubmatch(trimmed); m != nil {
			nodeID := templNodeID(service, file, lineNum, graph.NodeTypeComponent, "bind:"+m[1])
			nodes = append(nodes, graph.Node{
				ID: nodeID, Type: graph.NodeTypeComponent,
				Label: m[1], Service: service, File: file, Line: lineNum,
				Language: "templ",
				Meta:     map[string]string{"signal": m[1]},
			})
			edges = append(edges, componentEdge(currentComponentID, nodeID, graph.EdgeTypeDatastarBind))
		}

		// data-text / data-indicator referencing a signal variable
		if m := reDataText.FindStringSubmatch(trimmed); m != nil {
			nodeID := templNodeID(service, file, lineNum, graph.NodeTypeComponent, "read:"+m[1])
			nodes = append(nodes, graph.Node{
				ID: nodeID, Type: graph.NodeTypeComponent,
				Label: m[1], Service: service, File: file, Line: lineNum,
				Language: "templ",
				Meta:     map[string]string{"signal": m[1]},
			})
			edges = append(edges, componentEdge(currentComponentID, nodeID, graph.EdgeTypeDatastarBind))
		}

		// href / action pointing to a server path (including root "/")
		// Mark as navigation links so the cross-service linker skips them.
		for _, m := range reHrefAction.FindAllStringSubmatch(trimmed, -1) {
			path := m[1]
			nodeID := templNodeID(service, file, lineNum, graph.NodeTypeHTTPClient, "href:"+path)
			nodes = append(nodes, graph.Node{
				ID: nodeID, Type: graph.NodeTypeHTTPClient,
				Label: path, Service: service, File: file, Line: lineNum,
				Language: "templ",
				Meta:     map[string]string{"path": path, "nav_link": "true"},
			})
			edges = append(edges, componentEdge(currentComponentID, nodeID, graph.EdgeTypeHTTPCall))
		}
	}

	return nodes, edges, scanner.Err()
}

// templNodeID builds a deterministic node ID aligned with the design doc format:
// service:file:type:name:line
func templNodeID(service, file string, line int, nodeType graph.NodeType, name string) string {
	return fmt.Sprintf("%s:%s:%s:%s:%d", service, file, string(nodeType), name, line)
}

func selfEdge(nodeID string, edgeType graph.EdgeType) graph.Edge {
	return graph.Edge{
		ID:    nodeID + ":edge",
		From:  nodeID,
		To:    nodeID,
		Type:  edgeType,
		Label: string(edgeType),
	}
}

// componentEdge returns an edge from a component to an attribute node.
// Falls back to a self-loop when no enclosing component has been seen yet.
func componentEdge(fromID, toID string, edgeType graph.EdgeType) graph.Edge {
	if fromID == "" {
		return selfEdge(toID, edgeType)
	}
	return graph.Edge{
		ID:    fmt.Sprintf("%s:%s->%s", string(edgeType), fromID, toID),
		From:  fromID,
		To:    toID,
		Type:  edgeType,
		Label: string(edgeType),
	}
}

func init() {
	Register(&TemplParser{})
}
