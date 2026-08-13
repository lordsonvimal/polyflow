package graph

import (
	"fmt"
	"sort"
)

// SeamSide is one producer or consumer node participating in a seam, together
// with its chain to its entrypoint (producer) or terminus (consumer).
type SeamSide struct {
	Node  *Node     `json:"node"`
	Chain []FlowHop `json:"chain"`
}

// SeamResult is the response body for GET /api/seam/{edge-id}.
type SeamResult struct {
	Channel           string     `json:"channel"`
	VerificationState string     `json:"verification_state,omitempty"`
	Producers         []SeamSide `json:"producers"`
	Consumers         []SeamSide `json:"consumers"`
}

// FindEdgeByID scans the index for an edge with the given ID. There is no
// dedicated by-ID edge index (AllEdges already pays this linear cost
// elsewhere), so this mirrors that precedent rather than adding a new map to
// every AddEdge call for a lookup only the seam and UB.6 context-bundle
// endpoints need.
func FindEdgeByID(idx *AdjacencyIndex, id string) *Edge {
	for _, list := range idx.OutEdges {
		for _, e := range list {
			if e.ID == id {
				return e
			}
		}
	}
	return nil
}

// Seam isolates every producer and consumer sharing the channel touched by
// edgeID (rule 1: fan-out, never first-match). Channel identity resolves in
// priority order: Meta["channel_key"] on the edge (scanned across all edges
// sharing the key, From = producer, To = consumer), else the NodeTypeChannel
// node the edge touches (every edge into it is a producer edge, every edge
// out of it a consumer edge — the topology publishers/subscribers already
// use), else the edge's own two endpoints as a singleton channel.
func Seam(idx *AdjacencyIndex, edgeID string) (*SeamResult, error) {
	e := FindEdgeByID(idx, edgeID)
	if e == nil {
		return nil, fmt.Errorf("edge not found: %s", edgeID)
	}

	var producerEdges, consumerEdges []*Edge
	channel := ""

	switch {
	case e.Meta["channel_key"] != "":
		key := e.Meta["channel_key"]
		var matched []*Edge
		for _, edge := range idx.AllEdges() {
			if edge.Meta["channel_key"] != key {
				continue
			}
			edgeCopy := edge
			matched = append(matched, &edgeCopy)
		}
		// From = producer side, To = consumer side for every matching edge
		// (the same convention publishes/subscribes edges already use).
		producerEdges = matched
		consumerEdges = matched
		channel = key
	case idx.Nodes[e.From] != nil && idx.Nodes[e.From].Type == NodeTypeChannel:
		producerEdges = idx.InEdges[e.From]
		consumerEdges = idx.OutEdges[e.From]
		channel = channelLabel(idx.Nodes[e.From])
	case idx.Nodes[e.To] != nil && idx.Nodes[e.To].Type == NodeTypeChannel:
		producerEdges = idx.InEdges[e.To]
		consumerEdges = idx.OutEdges[e.To]
		channel = channelLabel(idx.Nodes[e.To])
	default:
		producerEdges = []*Edge{e}
		consumerEdges = []*Edge{e}
		channel = e.Label
		if channel == "" {
			channel = string(e.Type)
		}
	}

	producers := seamSidesFromEdges(idx, producerEdges, true)
	consumers := seamSidesFromEdges(idx, consumerEdges, false)

	return &SeamResult{
		Channel:           channel,
		VerificationState: e.VerificationState,
		Producers:         producers,
		Consumers:         consumers,
	}, nil
}

// seamSidesFromEdges converts producer/consumer edges into deduplicated,
// sorted SeamSide entries. fromSide selects e.From (producer) vs e.To
// (consumer) as the participant node.
func seamSidesFromEdges(idx *AdjacencyIndex, edges []*Edge, fromSide bool) []SeamSide {
	seen := map[string]bool{}
	var nodeIDs []string
	for _, e := range edges {
		id := e.To
		if fromSide {
			id = e.From
		}
		if seen[id] {
			continue
		}
		if idx.Nodes[id] == nil {
			continue
		}
		seen[id] = true
		nodeIDs = append(nodeIDs, id)
	}
	sort.Strings(nodeIDs)

	out := make([]SeamSide, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		n := idx.Nodes[id]
		var chain []FlowHop
		if fromSide {
			chain = longestChain(idx, id, "in")
		} else {
			chain = longestChain(idx, id, "out")
		}
		if len(chain) == 0 {
			chain = []FlowHop{flowHopFromNode(n)}
		}
		out = append(out, SeamSide{Node: n, Chain: chain})
	}
	return out
}

// longestChain runs a simple-path DFS from nodeID in the given direction
// (contains/declares excluded) and returns the longest maximal chain found,
// tie-broken lexically by node-id sequence for determinism. Empty when
// nodeID has no further edges in that direction.
func longestChain(idx *AdjacencyIndex, nodeID, direction string) []FlowHop {
	var best []string
	var bestEdges []*Edge

	var path []string
	var edges []*Edge
	onPath := map[string]bool{nodeID: true}

	var walk func(id string, depth int)
	walk = func(id string, depth int) {
		path = append(path, id)
		defer func() { path = path[:len(path)-1] }()

		if depth > 200 {
			return
		}

		var candidates []*Edge
		if direction == "in" {
			for _, e := range idx.InEdges[id] {
				if isFlowEdge(e.Type) {
					candidates = append(candidates, e)
				}
			}
			sort.Slice(candidates, func(i, j int) bool {
				if candidates[i].Type != candidates[j].Type {
					return candidates[i].Type < candidates[j].Type
				}
				return candidates[i].From < candidates[j].From
			})
		} else {
			candidates = sortedFlowEdges(idx, id)
		}

		extended := false
		for _, e := range candidates {
			next := e.To
			if direction == "in" {
				next = e.From
			}
			if onPath[next] || idx.Nodes[next] == nil {
				continue
			}
			extended = true
			onPath[next] = true
			edges = append(edges, e)
			walk(next, depth+1)
			edges = edges[:len(edges)-1]
			onPath[next] = false
		}
		if !extended && len(path) > len(best) {
			best = append([]string{}, path...)
			bestEdges = append([]*Edge{}, edges...)
		}
	}
	walk(nodeID, 0)

	if len(best) <= 1 {
		return nil
	}
	if direction == "in" {
		// path was walked root-outward (nodeID first); reverse so the chain
		// reads source -> ... -> nodeID in flow order.
		revNodes := make([]string, len(best))
		for i, id := range best {
			revNodes[len(best)-1-i] = id
		}
		revEdges := make([]*Edge, len(bestEdges))
		for i, e := range bestEdges {
			revEdges[len(bestEdges)-1-i] = e
		}
		return pathToFlowHops(idx, revNodes, revEdges)
	}
	return pathToFlowHops(idx, best, bestEdges)
}
