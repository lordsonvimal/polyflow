package graph

import "sort"

// LinkRow is one row in a LinkExplorerResult — the far node reached by
// following one adjacency edge (or a short path when depth > 1) from a
// selected node, in a single direction (upstream/downstream).
type LinkRow struct {
	NodeID            string   `json:"node_id"`
	Label             string   `json:"label"`
	Type              string   `json:"type"`
	Service           string   `json:"service"`
	File              string   `json:"file"`
	Line              int      `json:"line"`
	EdgeID            string   `json:"edge_id"`
	EdgeType          string   `json:"edge_type"`
	EdgeLabel         string   `json:"edge_label,omitempty"`
	Channel           string   `json:"channel,omitempty"`
	CrossService      bool     `json:"cross_service,omitempty"`
	Confidence        string   `json:"confidence,omitempty"`
	VerificationState string   `json:"verification_state,omitempty"`
	Depth             int      `json:"depth"`
	// Via holds the intermediate node labels between the selected node and
	// this row, root-exclusive/row-exclusive, only set when Depth > 1 (UF.8's
	// "via X → Y" depth grouping).
	Via []string `json:"via,omitempty"`
}

// LinkExplorerResult is the response body for GET /api/node/{id}/links.
type LinkExplorerResult struct {
	Direction string    `json:"direction"`
	Depth     int       `json:"depth"`
	// Total is the exact count after the kind/service filter, independent of
	// offset/limit — Rule-1 honesty: nothing hidden by pagination.
	Total     int       `json:"total"`
	Offset    int       `json:"offset"`
	Rows      []LinkRow `json:"rows"`
	Truncated bool      `json:"truncated"`
}

// linkParent records the single (first-discovered, shortest) BFS predecessor
// of a reached node so its path back to the start can be replayed for
// depth>1 "via" grouping.
type linkParent struct {
	via   *Edge
	prev  string
	depth int
}

// LinkExplorerAdjacency walks direction ("upstream" follows InEdges,
// "downstream" follows OutEdges) up to depth hops from nodeID, keeping the
// first-discovered path to each reached node so every row has exactly one
// path. kindFilter/serviceFilter apply to the far node; empty means
// unfiltered. Results are paginated at offset/limit (capped 100) but Total
// always reflects the complete filtered adjacency.
func LinkExplorerAdjacency(idx *AdjacencyIndex, nodeID, direction string, depth, offset, limit int, kindFilter, serviceFilter string) *LinkExplorerResult {
	if depth <= 0 {
		depth = 1
	}
	if depth > 3 {
		depth = 3
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	start := idx.Nodes[nodeID]
	if start == nil {
		return &LinkExplorerResult{Direction: direction, Depth: depth, Rows: []LinkRow{}}
	}

	parents := map[string]linkParent{}
	visited := map[string]bool{nodeID: true}
	queue := []string{nodeID}
	var order []string // reached node ids, BFS discovery order

	for d := 0; d < depth && len(queue) > 0; d++ {
		var next []string
		for _, cur := range queue {
			for _, e := range sortedAdjacency(idx, cur, direction) {
				far := e.To
				if direction == "upstream" {
					far = e.From
				}
				if visited[far] {
					continue
				}
				visited[far] = true
				parents[far] = linkParent{via: e, prev: cur, depth: d + 1}
				order = append(order, far)
				next = append(next, far)
			}
		}
		queue = next
	}

	rows := make([]LinkRow, 0, len(order))
	for _, id := range order {
		n := idx.Nodes[id]
		if n == nil {
			continue
		}
		if kindFilter != "" && string(n.Type) != kindFilter {
			continue
		}
		if serviceFilter != "" && n.Service != serviceFilter {
			continue
		}
		p := parents[id]
		row := LinkRow{
			NodeID:            n.ID,
			Label:             n.Label,
			Type:              string(n.Type),
			Service:           n.Service,
			File:              n.File,
			Line:              n.Line,
			EdgeID:            p.via.ID,
			EdgeType:          string(p.via.Type),
			EdgeLabel:         p.via.Label,
			Channel:           edgeChannel(idx, p.via),
			CrossService:      start.Service != "" && n.Service != "" && start.Service != n.Service,
			Confidence:        p.via.Confidence,
			VerificationState: p.via.VerificationState,
			Depth:             p.depth,
		}
		if p.depth > 1 {
			row.Via = linkViaPath(idx, parents, id)
		}
		rows = append(rows, row)
	}

	total := len(rows)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := append([]LinkRow{}, rows[offset:end]...)

	return &LinkExplorerResult{
		Direction: direction,
		Depth:     depth,
		Total:     total,
		Offset:    offset,
		Rows:      page,
		Truncated: end < total,
	}
}

// sortedAdjacency returns id's edges in the given direction ordered by
// (type, far-node-id) for deterministic traversal.
func sortedAdjacency(idx *AdjacencyIndex, id, direction string) []*Edge {
	var edges []*Edge
	if direction == "upstream" {
		edges = idx.InEdges[id]
	} else {
		edges = idx.OutEdges[id]
	}
	out := append([]*Edge{}, edges...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		if direction == "upstream" {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// edgeChannel derives an edge's channel summary when either endpoint is a
// NodeTypeChannel node — the same convention Seam and Entrypoints use.
// Returns "" when neither endpoint is a channel node (never guessed).
func edgeChannel(idx *AdjacencyIndex, e *Edge) string {
	if n := idx.Nodes[e.From]; n != nil && n.Type == NodeTypeChannel {
		return channelLabel(n)
	}
	if n := idx.Nodes[e.To]; n != nil && n.Type == NodeTypeChannel {
		return channelLabel(n)
	}
	return ""
}

// linkViaPath renders the intermediate node labels on the path from the
// LinkExplorerAdjacency start node to id, root-first, id-exclusive — the
// depth>1 "via X → Y" grouping the UF.8 spec calls for.
func linkViaPath(idx *AdjacencyIndex, parents map[string]linkParent, id string) []string {
	var labels []string
	cur := parents[id].prev
	for {
		p, ok := parents[cur]
		if !ok {
			break // cur is the start node, not itself a hop
		}
		if n := idx.Nodes[cur]; n != nil {
			labels = append(labels, n.Label)
		}
		cur = p.prev
	}
	for i, j := 0, len(labels)-1; i < j; i, j = i+1, j-1 {
		labels[i], labels[j] = labels[j], labels[i]
	}
	return labels
}
