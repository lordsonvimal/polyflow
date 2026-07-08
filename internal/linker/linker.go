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

// baseURLFor returns the base_url prefix for a from->to service link, or "".
func (l *Linker) baseURLFor(fromSvc, toSvc string) string {
	for _, link := range l.config.Links {
		if link.From == fromSvc && link.To == toSvc && link.BaseURL != "" {
			return link.BaseURL
		}
	}
	return ""
}

// LinkRouteHandlers emits calls edges from HTTP route nodes to their handler
// function nodes. Route patterns capture the handler as Meta["handler"], but
// since parsing is per-file the reference can't be resolved there. This pass
// runs after all nodes are collected and matches by function label within the
// same service.
func LinkRouteHandlers(nodes []graph.Node) []graph.Edge {
	// Index function/method nodes: service + "\x00" + label → nodeID
	funcIndex := make(map[string]string)
	for i := range nodes {
		n := &nodes[i]
		if n.Type == graph.NodeTypeFunction || n.Type == graph.NodeTypeMethod {
			key := n.Service + "\x00" + n.Label
			if _, exists := funcIndex[key]; !exists {
				funcIndex[key] = n.ID
			}
		}
	}

	var edges []graph.Edge
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPHandler {
			continue
		}
		handlerName, ok := n.Meta["handler"]
		if !ok || handlerName == "" {
			continue
		}
		// Strip method receiver: "s.handleSearch" → "handleSearch"
		if dot := strings.LastIndex(handlerName, "."); dot >= 0 {
			handlerName = handlerName[dot+1:]
		}
		calleeID, ok := funcIndex[n.Service+"\x00"+handlerName]
		if !ok {
			continue
		}
		edges = append(edges, graph.Edge{
			ID:   fmt.Sprintf("calls:%s->%s", n.ID, calleeID),
			From: n.ID,
			To:   calleeID,
			Type: graph.EdgeTypeCalls,
		})
	}
	return edges
}

// Link attempts to resolve cross-service HTTP connections.
// It returns synthetic edges connecting client call nodes to handler nodes.
// Clients whose paths cannot be resolved still produce an edge with confidence "unknown".
func (l *Linker) Link(nodes []graph.Node, edges []graph.Edge) ([]graph.Edge, error) {
	if l.config == nil {
		return nil, fmt.Errorf("linker: no workspace config provided")
	}

	// Index handlers and clients.
	var handlers []*graph.Node
	nodeByID := make(map[string]*graph.Node)
	clients := make([]*graph.Node, 0)

	for i := range nodes {
		n := &nodes[i]
		nodeByID[n.ID] = n
		switch n.Type {
		case graph.NodeTypeHTTPHandler:
			handlers = append(handlers, n)
		case graph.NodeTypeHTTPClient:
			clients = append(clients, n)
		}
	}

	var crossEdges []graph.Edge
	for _, client := range clients {
		method := client.Meta["method"]
		path := client.Meta["path"]
		targetSvc := client.Meta["target_service"]

		// Build handler lookup maps, stripping the base_url prefix from handler paths
		// so they align with the already-stripped client paths from ApplyHints.
		// When target_service is known, only index handlers from that service.
		baseURL := l.baseURLFor(client.Service, targetSvc)
		exactHandlers := make(map[string]*graph.Node)
		normalHandlers := make(map[string]*graph.Node)
		for _, h := range handlers {
			if targetSvc != "" && h.Service != targetSvc {
				continue
			}
			hpath := h.Meta["path"]
			if baseURL != "" && strings.HasPrefix(hpath, baseURL) {
				hpath = hpath[len(baseURL):]
				if hpath == "" {
					hpath = "/"
				}
			}
			raw := routeKey(h.Meta["method"], hpath)
			exactHandlers[raw] = h
			norm := routeKey(h.Meta["method"], normalizePath(hpath))
			normalHandlers[norm] = h
		}

		// Find handler and determine confidence
		handler, confidence := resolveHandler(method, path, exactHandlers, normalHandlers)

		if handler != nil {
			// Skip same-service pairs — those are already captured by "calls" edges
			if client.Service == handler.Service {
				continue
			}
			edgeMeta := map[string]string{"confidence": confidence}
			if client.Meta["datastar"] == "true" {
				edgeMeta["via"] = "datastar_action"
			}
			crossEdges = append(crossEdges, graph.Edge{
				ID:         fmt.Sprintf("link:%s->%s", client.ID, handler.ID),
				From:       client.ID,
				To:         handler.ID,
				Type:       graph.EdgeTypeHTTPCall,
				Label:      fmt.Sprintf("%s %s", method, path),
				Confidence: confidence,
				Method:     method,
				Path:       path,
				Meta:       edgeMeta,
			})
		} else {
			// Unresolvable: emit edge with unknown confidence so the call is visible
			targetService := client.Meta["target_service"]
			targetID := "unresolved"
			if targetService != "" {
				targetID = "unresolved:" + targetService
			}
			crossEdges = append(crossEdges, graph.Edge{
				ID:         fmt.Sprintf("link:%s->%s", client.ID, targetID),
				From:       client.ID,
				To:         targetID,
				Type:       graph.EdgeTypeHTTPCall,
				Label:      fmt.Sprintf("%s %s", method, path),
				Confidence: graph.ConfidenceUnknown,
				Method:     method,
				Path:       path,
				Meta:       map[string]string{"confidence": graph.ConfidenceUnknown},
			})
		}
	}

	return crossEdges, nil
}

// candidateMethods returns the methods to try when matching. An empty method
// means the client didn't specify one (e.g. a bare fetch() call), so we
// fall back to the most common HTTP verbs in priority order.
func candidateMethods(method string) []string {
	if method != "" {
		return []string{strings.ToUpper(method)}
	}
	return []string{"GET", "POST", "PUT", "PATCH", "DELETE", ""}
}

func resolveHandler(method, path string, exact, normal map[string]*graph.Node) (*graph.Node, string) {
	for _, m := range candidateMethods(method) {
		rawKey := routeKey(m, path)
		if h, ok := exact[rawKey]; ok {
			return h, "static"
		}
	}
	// Try normalized matching across candidate methods.
	for _, m := range candidateMethods(method) {
		prefix := m + " "
		for normKey, h := range normal {
			if m != "" && !strings.HasPrefix(normKey, prefix) {
				continue
			}
			handlerNorm := normKey
			if m != "" {
				handlerNorm = normKey[len(prefix):]
			} else if i := strings.Index(normKey, " "); i >= 0 {
				handlerNorm = normKey[i+1:]
			}
			if pathMatchesPattern(path, handlerNorm) {
				return h, "inferred"
			}
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
