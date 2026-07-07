package server

import "github.com/lordsonvimal/polyflow/internal/graph"

// CytoscapeNode is the Cytoscape.js node format.
type CytoscapeNode struct {
	Data CytoscapeNodeData `json:"data"`
}

// CytoscapeNodeData holds the node payload for Cytoscape.js.
type CytoscapeNodeData struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Service  string `json:"service"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Language string `json:"language"`
}

// CytoscapeEdge is the Cytoscape.js edge format.
type CytoscapeEdge struct {
	Data CytoscapeEdgeData `json:"data"`
}

// CytoscapeEdgeData holds the edge payload for Cytoscape.js.
type CytoscapeEdgeData struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Type   string `json:"type"`
	Label  string `json:"label,omitempty"`
}

// CytoscapeGraph is the top-level Cytoscape.js elements object.
type CytoscapeGraph struct {
	Nodes []CytoscapeNode `json:"nodes"`
	Edges []CytoscapeEdge `json:"edges"`
}

// ToCytoscapeJSON converts polyflow nodes and edges to Cytoscape.js format.
func ToCytoscapeJSON(nodes []*graph.Node, edges []*graph.Edge) CytoscapeGraph {
	result := CytoscapeGraph{
		Nodes: make([]CytoscapeNode, 0, len(nodes)),
		Edges: make([]CytoscapeEdge, 0, len(edges)),
	}

	for _, n := range nodes {
		result.Nodes = append(result.Nodes, CytoscapeNode{
			Data: CytoscapeNodeData{
				ID:       n.ID,
				Label:    n.Label,
				Type:     string(n.Type),
				Service:  n.Service,
				File:     n.File,
				Line:     n.Line,
				Language: n.Language,
			},
		})
	}

	for _, e := range edges {
		result.Edges = append(result.Edges, CytoscapeEdge{
			Data: CytoscapeEdgeData{
				ID:     e.ID,
				Source: e.From,
				Target: e.To,
				Type:   string(e.Type),
				Label:  e.Label,
			},
		})
	}

	return result
}
