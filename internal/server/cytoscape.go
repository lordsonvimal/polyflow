package server

import "github.com/lordsonvimal/polyflow/internal/graph"

// CytoscapeNode is the Cytoscape.js node format.
type CytoscapeNode struct {
	Data CytoscapeNodeData `json:"data"`
}

// CytoscapeNodeData holds the node payload for Cytoscape.js. Meta carries
// node metadata — notably package + resolved_version for framework-boundary
// and cloud-SDK matches, which the UI surfaces and groups on.
type CytoscapeNodeData struct {
	ID       string            `json:"id"`
	Label    string            `json:"label"`
	Type     string            `json:"type"`
	Service  string            `json:"service"`
	File     string            `json:"file"`
	Line     int               `json:"line"`
	EndLine  int               `json:"end_line,omitempty"`
	Language string            `json:"language"`
	Meta     map[string]string `json:"meta,omitempty"`
}

// CytoscapeEdge is the Cytoscape.js edge format.
type CytoscapeEdge struct {
	Data CytoscapeEdgeData `json:"data"`
}

// CytoscapeEdgeData holds the edge payload for Cytoscape.js.
type CytoscapeEdgeData struct {
	ID         string `json:"id"`
	Source     string `json:"source"`
	Target     string `json:"target"`
	Type       string `json:"type"`
	Label      string `json:"label,omitempty"`
	Confidence string `json:"confidence,omitempty"`
	Method     string `json:"method,omitempty"`
	Path       string `json:"path,omitempty"`
	// VerificationState (UF.6): plan-10's edge-styling contract needs this on
	// every general-purpose graph endpoint (trace/graph/scope), not just the
	// flow-lane's own bespoke fetch — otherwise the coverage overlay could
	// only ever style one scope kind.
	VerificationState string            `json:"verification_state,omitempty"`
	Meta              map[string]string `json:"meta,omitempty"`
}

// CytoscapeGraph is the top-level Cytoscape.js elements object.
type CytoscapeGraph struct {
	Nodes []CytoscapeNode `json:"nodes"`
	Edges []CytoscapeEdge `json:"edges"`
}

// ToCytoscapeJSON converts polyflow nodes and edges to Cytoscape.js format.
// Every edge is labeled with Tier NV's noise classification (Meta
// ["noise_class"]) when applicable — a pure labeling pass over data the
// caller's traversal already produced raw/unfiltered; see
// graph.ClassifyEdgeNoise. This is label-don't-filter: FilterBar decides
// client-side what to hide, mirroring trace/context/impact's
// classify-then-partition but without dropping anything server-side.
func ToCytoscapeJSON(nodes []*graph.Node, edges []*graph.Edge) CytoscapeGraph {
	result := CytoscapeGraph{
		Nodes: make([]CytoscapeNode, 0, len(nodes)),
		Edges: make([]CytoscapeEdge, 0, len(edges)),
	}

	byID := make(map[string]*graph.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
		result.Nodes = append(result.Nodes, CytoscapeNode{
			Data: CytoscapeNodeData{
				ID:       n.ID,
				Label:    n.Label,
				Type:     string(n.Type),
				Service:  n.Service,
				File:     n.File,
				Line:     n.Line,
				EndLine:  n.EndLine,
				Language: n.Language,
				Meta:     n.Meta,
			},
		})
	}

	for _, e := range edges {
		meta := e.Meta
		if class := graph.ClassifyEdgeNoise(e, byID[e.To]); class != graph.NoiseNone {
			meta = make(map[string]string, len(e.Meta)+1)
			for k, v := range e.Meta {
				meta[k] = v
			}
			meta["noise_class"] = string(class)
		}
		result.Edges = append(result.Edges, CytoscapeEdge{
			Data: CytoscapeEdgeData{
				ID:                e.ID,
				Source:            e.From,
				Target:            e.To,
				Type:              string(e.Type),
				Label:             e.Label,
				Confidence:        e.Confidence,
				Method:            e.Method,
				Path:              e.Path,
				VerificationState: e.VerificationState,
				Meta:              meta,
			},
		})
	}

	return result
}
