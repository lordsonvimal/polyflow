package linker

import (
	"fmt"
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

// Link attempts to resolve cross-service HTTP connections.
// It returns synthetic edges connecting client call nodes to handler nodes.
func (l *Linker) Link(nodes []graph.Node, edges []graph.Edge) ([]graph.Edge, error) {
	if l.config == nil {
		return nil, fmt.Errorf("linker: no workspace config provided")
	}

	// Build maps for fast lookup
	handlers := make(map[string]*graph.Node) // "METHOD /path" -> node
	clients := make([]*graph.Node, 0)

	for i := range nodes {
		n := &nodes[i]
		switch n.Type {
		case graph.NodeTypeHTTPHandler:
			key := routeKey(n.Meta["method"], n.Meta["path"])
			handlers[key] = n
		case graph.NodeTypeHTTPClient:
			clients = append(clients, n)
		}
	}

	var crossEdges []graph.Edge
	for _, client := range clients {
		method := client.Meta["method"]
		path := client.Meta["path"]
		key := routeKey(method, path)
		if handler, ok := handlers[key]; ok {
			crossEdges = append(crossEdges, graph.Edge{
				ID:    fmt.Sprintf("link:%s->%s", client.ID, handler.ID),
				From:  client.ID,
				To:    handler.ID,
				Type:  graph.EdgeTypeHTTPCall,
				Label: fmt.Sprintf("%s %s", method, path),
			})
		}
	}

	return crossEdges, nil
}

func routeKey(method, path string) string {
	return strings.ToUpper(method) + " " + path
}
