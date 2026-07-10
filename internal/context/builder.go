package context

import (
	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Result is the structured output of a context query.
type Result struct {
	Target       *graph.Node   `json:"target"`
	Task         string        `json:"task"`
	Upstream     []TraceNode   `json:"upstream"`
	Downstream   []TraceNode   `json:"downstream"`
	CrossService []CrossEdge   `json:"cross_service"`
	Depth        int           `json:"depth"`
	TotalNodes   int           `json:"total_nodes"`
	TotalEdges   int           `json:"total_edges"`

	// Unresolved lists references in the traversed files that the indexer
	// could not resolve — edges that may be missing from this answer. Always
	// present ([] when clean) so its absence is never mistaken for certainty.
	Unresolved     []graph.UnresolvedRef `json:"unresolved"`
	UnresolvedNote string                `json:"unresolved_note,omitempty"`

	// Budget records the token-budgeting decision when --max-tokens was set
	// and the detail shape was emitted.
	Budget *budget.Info `json:"budget,omitempty"`
}

// TraceNode is a node in a traversal result with its edge type and depth.
// Meta carries node metadata (including package + resolved_version for
// version-gated matches); EdgeMeta/Confidence describe the connecting edge.
type TraceNode struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Label      string            `json:"label"`
	Service    string            `json:"service"`
	File       string            `json:"file"`
	Line       int               `json:"line"`
	Language   string            `json:"language"`
	Meta       map[string]string `json:"meta,omitempty"`
	EdgeType   string            `json:"edge_type"`
	Confidence string            `json:"confidence,omitempty"`
	EdgeMeta   map[string]string `json:"edge_meta,omitempty"`
	Depth      int               `json:"depth"`
	Snippet    string            `json:"snippet,omitempty"`
}

// CrossEdge represents a connection that crosses service boundaries.
type CrossEdge struct {
	FromService string            `json:"from_service"`
	ToService   string            `json:"to_service"`
	Label       string            `json:"label"`
	EdgeType    string            `json:"edge_type"`
	Confidence  string            `json:"confidence,omitempty"`
	Method      string            `json:"method,omitempty"`
	Path        string            `json:"path,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
}

// Build produces a context result for the given target node and task.
// Depth <= 0 means unlimited traversal.
func Build(idx *graph.AdjacencyIndex, targetID, task string, depth int) *Result {
	upstream, downstream := traverse(idx, targetID, task, depth)

	crossService := extractCrossService(idx, upstream, downstream)

	nodeSet := make(map[string]bool, len(upstream)+len(downstream))
	edgeSet := make(map[string]bool, len(upstream)+len(downstream))
	for _, n := range upstream {
		nodeSet[n.ID] = true
		if n.EdgeType != "" {
			edgeSet[n.ID+n.EdgeType] = true
		}
	}
	for _, n := range downstream {
		nodeSet[n.ID] = true
		if n.EdgeType != "" {
			edgeSet[n.ID+n.EdgeType] = true
		}
	}

	return &Result{
		Target:       idx.Nodes[targetID],
		Task:         task,
		Upstream:     upstream,
		Downstream:   downstream,
		CrossService: crossService,
		Depth:        depth,
		TotalNodes:   len(nodeSet) + 1, // +1 for the target itself
		TotalEdges:   len(edgeSet),
		Unresolved:   []graph.UnresolvedRef{},
	}
}

// AttachUnresolved scopes the workspace's unresolved-reference ledger to the
// files touched by this traversal and records the matches on the result.
func (r *Result) AttachUnresolved(refs []graph.UnresolvedRef) {
	files := make(map[string]bool, len(r.Upstream)+len(r.Downstream)+1)
	if r.Target != nil {
		files[r.Target.File] = true
	}
	for _, n := range r.Upstream {
		files[n.File] = true
	}
	for _, n := range r.Downstream {
		files[n.File] = true
	}
	r.Unresolved = graph.UnresolvedInFiles(refs, files)
	r.UnresolvedNote = graph.UnresolvedNote(len(r.Unresolved))
}

// traverse runs BFS in the appropriate directions for the given task.
func traverse(idx *graph.AdjacencyIndex, targetID, task string, depth int) (upstream, downstream []TraceNode) {
	switch task {
	case "impact":
		upstream = toTraceNodes(graph.Ancestors(idx, targetID, depth))
	case "generate":
		downstream = toTraceNodes(graph.Descendants(idx, targetID, depth))
	case "debug", "refactor":
		upstream = toTraceNodes(graph.Ancestors(idx, targetID, depth))
		downstream = toTraceNodes(graph.Descendants(idx, targetID, depth))
	default:
		upstream = toTraceNodes(graph.Ancestors(idx, targetID, depth))
		downstream = toTraceNodes(graph.Descendants(idx, targetID, depth))
	}
	return
}

func toTraceNodes(results []graph.TraversalResult) []TraceNode {
	out := make([]TraceNode, 0, len(results))
	for _, r := range results {
		if r.Node == nil {
			continue
		}
		tn := TraceNode{
			ID:       r.Node.ID,
			Type:     string(r.Node.Type),
			Label:    r.Node.Label,
			Service:  r.Node.Service,
			File:     r.Node.File,
			Line:     r.Node.Line,
			Language: r.Node.Language,
			Meta:     r.Node.Meta,
			Depth:    r.Depth,
		}
		if r.Via != nil {
			tn.EdgeType = string(r.Via.Type)
			tn.Confidence = r.Via.Confidence
			tn.EdgeMeta = r.Via.Meta
		}
		out = append(out, tn)
	}
	return out
}

// extractCrossService finds edges in the traversal results that cross service
// boundaries and returns them as CrossEdge entries.
func extractCrossService(idx *graph.AdjacencyIndex, upstream, downstream []TraceNode) []CrossEdge {
	// Collect all node IDs from results (plus implied edges via OutEdges).
	allNodeIDs := make(map[string]bool, len(upstream)+len(downstream))
	for _, n := range upstream {
		allNodeIDs[n.ID] = true
	}
	for _, n := range downstream {
		allNodeIDs[n.ID] = true
	}

	seen := make(map[string]bool)
	var out []CrossEdge
	for nodeID := range allNodeIDs {
		node := idx.Nodes[nodeID]
		if node == nil {
			continue
		}
		for _, e := range idx.OutEdges[nodeID] {
			toNode := idx.Nodes[e.To]
			if toNode == nil {
				continue
			}
			if node.Service == toNode.Service {
				continue
			}
			key := e.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, CrossEdge{
				FromService: node.Service,
				ToService:   toNode.Service,
				Label:       e.Label,
				EdgeType:    string(e.Type),
				Confidence:  e.Confidence,
				Method:      e.Method,
				Path:        e.Path,
				Meta:        e.Meta,
			})
		}
	}
	return out
}
