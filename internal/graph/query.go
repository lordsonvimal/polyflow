package graph

// TraversalMode selects BFS or DFS.
type TraversalMode int

const (
	BFS TraversalMode = iota
	DFS
)

// TraversalResult holds a discovered node and the edge that led to it.
type TraversalResult struct {
	Node  *Node
	Via   *Edge
	Depth int
}

// Traverse walks the graph from startID in the given direction using BFS or DFS.
// direction: "out" follows OutEdges, "in" follows InEdges.
// maxDepth <= 0 means unlimited.
func Traverse(idx *AdjacencyIndex, startID string, direction string, mode TraversalMode, maxDepth int) []TraversalResult {
	if _, ok := idx.Nodes[startID]; !ok {
		return nil
	}

	var results []TraversalResult
	visited := make(map[string]bool)
	visited[startID] = true

	type item struct {
		nodeID string
		via    *Edge
		depth  int
	}

	queue := []item{{nodeID: startID, depth: 0}}

	for len(queue) > 0 {
		var cur item
		if mode == BFS {
			cur, queue = queue[0], queue[1:]
		} else {
			cur, queue = queue[len(queue)-1], queue[:len(queue)-1]
		}

		if cur.depth > 0 {
			results = append(results, TraversalResult{
				Node:  idx.Nodes[cur.nodeID],
				Via:   cur.via,
				Depth: cur.depth,
			})
		}

		if maxDepth > 0 && cur.depth >= maxDepth {
			continue
		}

		var edges []*Edge
		if direction == "in" {
			edges = idx.InEdges[cur.nodeID]
		} else {
			edges = idx.OutEdges[cur.nodeID]
		}

		for _, e := range edges {
			next := e.To
			if direction == "in" {
				next = e.From
			}
			if !visited[next] {
				visited[next] = true
				queue = append(queue, item{nodeID: next, via: e, depth: cur.depth + 1})
			}
		}
	}

	return results
}

// Ancestors returns all nodes that can reach startID (upstream callers).
func Ancestors(idx *AdjacencyIndex, startID string, maxDepth int) []TraversalResult {
	return Traverse(idx, startID, "in", BFS, maxDepth)
}

// Descendants returns all nodes reachable from startID (downstream callees).
func Descendants(idx *AdjacencyIndex, startID string, maxDepth int) []TraversalResult {
	return Traverse(idx, startID, "out", BFS, maxDepth)
}
