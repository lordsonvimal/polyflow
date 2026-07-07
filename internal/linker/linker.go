package linker

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// Linker resolves cross-service edges by matching HTTP client calls
// to HTTP handler nodes across services.
type Linker struct {
	config *workspace.WorkspaceConfig
}

// New creates a Linker for the given workspace configuration.
func New(cfg *workspace.WorkspaceConfig) *Linker {
	return &Linker{config: cfg}
}

var reParamColon = regexp.MustCompile(`:[^/]+`)
var reParamBrace = regexp.MustCompile(`\{[^}]+\}`)
var reParamRegex = regexp.MustCompile(`\[[^\]]+\][+*?]?`)

// normalizePath replaces parameter segments with * and strips trailing slashes.
func normalizePath(path string) string {
	p := reParamColon.ReplaceAllString(path, "*")
	p = reParamBrace.ReplaceAllString(p, "*")
	p = reParamRegex.ReplaceAllString(p, "*")
	p = strings.TrimRight(p, "/")
	if p == "" {
		p = "/"
	}
	return p
}

// Link attempts to resolve cross-service HTTP connections.
// It returns synthetic edges connecting client call nodes to handler nodes.
// Clients whose paths cannot be resolved still produce an edge with confidence "unknown".
func (l *Linker) Link(nodes []graph.Node, edges []graph.Edge) ([]graph.Edge, error) {
	if l.config == nil {
		return nil, fmt.Errorf("linker: no workspace config provided")
	}

	// Build maps for fast lookup: normalized key -> handler node
	exactHandlers := make(map[string]*graph.Node)    // exact "METHOD /path" -> node
	normalHandlers := make(map[string]*graph.Node)   // normalized "METHOD /path*" -> node
	nodeByID := make(map[string]*graph.Node)
	clients := make([]*graph.Node, 0)

	for i := range nodes {
		n := &nodes[i]
		nodeByID[n.ID] = n
		switch n.Type {
		case graph.NodeTypeHTTPHandler:
			raw := routeKey(n.Meta["method"], n.Meta["path"])
			exactHandlers[raw] = n
			norm := routeKey(n.Meta["method"], normalizePath(n.Meta["path"]))
			normalHandlers[norm] = n
		case graph.NodeTypeHTTPClient:
			clients = append(clients, n)
		}
	}

	var crossEdges []graph.Edge
	for _, client := range clients {
		method := client.Meta["method"]
		path := client.Meta["path"]

		// Find handler and determine confidence
		handler, confidence := resolveHandler(method, path, exactHandlers, normalHandlers)

		if handler != nil {
			// Skip same-service pairs — those are already captured by "calls" edges
			if client.Service == handler.Service {
				continue
			}
			meta := map[string]string{"confidence": confidence}
			crossEdges = append(crossEdges, graph.Edge{
				ID:    fmt.Sprintf("link:%s->%s", client.ID, handler.ID),
				From:  client.ID,
				To:    handler.ID,
				Type:  graph.EdgeTypeHTTPCall,
				Label: fmt.Sprintf("%s %s", method, path),
				Meta:  meta,
			})
		} else {
			// Unresolvable: emit edge with unknown confidence so the call is visible
			targetService := client.Meta["target_service"]
			targetID := "unresolved"
			if targetService != "" {
				targetID = "unresolved:" + targetService
			}
			crossEdges = append(crossEdges, graph.Edge{
				ID:    fmt.Sprintf("link:%s->%s", client.ID, targetID),
				From:  client.ID,
				To:    targetID,
				Type:  graph.EdgeTypeHTTPCall,
				Label: fmt.Sprintf("%s %s", method, path),
				Meta:  map[string]string{"confidence": "unknown"},
			})
		}
	}

	return crossEdges, nil
}

func resolveHandler(method, path string, exact, normal map[string]*graph.Node) (*graph.Node, string) {
	rawKey := routeKey(method, path)
	if h, ok := exact[rawKey]; ok {
		return h, "static"
	}
	// Try matching by normalizing both sides: if the client path has no params,
	// its normalized form equals itself, so we scan handler normalized paths for
	// a wildcard match against the client path.
	m := strings.ToUpper(method)
	for normKey, h := range normal {
		if !strings.HasPrefix(normKey, m+" ") {
			continue
		}
		handlerNorm := normKey[len(m)+1:]
		if pathMatchesPattern(path, handlerNorm) {
			return h, "inferred"
		}
	}
	return nil, ""
}

// pathMatchesPattern returns true when path matches pattern where "*" in pattern
// matches any single non-empty path segment.
func pathMatchesPattern(path, pattern string) bool {
	ps := splitPath(path)
	pp := splitPath(pattern)
	if len(ps) != len(pp) {
		return false
	}
	for i := range pp {
		if pp[i] != "*" && pp[i] != ps[i] {
			return false
		}
	}
	return true
}

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return []string{}
	}
	return strings.Split(p, "/")
}

func routeKey(method, path string) string {
	return strings.ToUpper(method) + " " + path
}
