package graph

import "sort"

// RelatedFileEntry is one file ranked as related to a set of seed files.
type RelatedFileEntry struct {
	File    string `json:"file"`
	Service string `json:"service"`
	// Refs counts the distinct edges directly connecting a seed-file node and
	// a node in this file, in either direction — the primary rank signal.
	Refs int `json:"refs"`
	// Nodes counts the distinct nodes reached in this file within the depth.
	Nodes    int `json:"nodes"`
	MinDepth int `json:"min_depth"`
	// EdgeTypes lists every edge type seen on paths into this file.
	EdgeTypes []string `json:"edge_types"`
	// BestVerificationState is the highest-confidence verification state seen
	// on any edge touching this file — used as a tie-breaker when Refs, MinDepth,
	// and Nodes are equal. Order: verified > observed_only_gap > candidate > conflicting.
	BestVerificationState string `json:"best_verification_state,omitempty"`
}

// RelatedFiles ranks the files related to the given seed files: the graph
// neighborhood within maxDepth hops in both directions (import-derived
// cross-file edges included — the linker materializes imports as
// calls/reads/writes edges). Rank order: direct references first (a file
// sharing 10 edges with the seed beats one sharing 1), then hop distance,
// then reached-node count. Seed files themselves are excluded. Paths resolve
// like NodesInFile (exact, then "/"+path suffix); paths with no nodes in the
// index are returned in missing rather than silently dropped. maxDepth <= 0
// means unlimited.
func RelatedFiles(idx *AdjacencyIndex, service string, paths []string, maxDepth int) (seedFiles []string, related []RelatedFileEntry, missing []string) {
	var seedNodes []*Node
	seedSet := make(map[string]bool) // service+"\x00"+file
	for _, p := range paths {
		nodes := NodesInFile(idx, service, p)
		if len(nodes) == 0 {
			missing = append(missing, p)
			continue
		}
		seedNodes = append(seedNodes, nodes...)
		for _, n := range nodes {
			key := n.Service + "\x00" + n.File
			if !seedSet[key] {
				seedSet[key] = true
				seedFiles = append(seedFiles, n.File)
			}
		}
	}
	sort.Strings(seedFiles)
	if len(seedNodes) == 0 {
		return seedFiles, nil, missing
	}

	type key struct{ service, file string }
	entries := make(map[key]*RelatedFileEntry)
	entry := func(n *Node, depth int) *RelatedFileEntry {
		k := key{n.Service, n.File}
		e, ok := entries[k]
		if !ok {
			e = &RelatedFileEntry{File: n.File, Service: n.Service, MinDepth: depth}
			entries[k] = e
		}
		return e
	}

	// Direct references: every edge with one endpoint in a seed file and the
	// other outside counts toward the outside file, whichever way the edge
	// points — relatedness is symmetric even though edges are not.
	seenEdge := make(map[string]bool)
	countDirect := func(otherID string, e *Edge) {
		n := idx.Nodes[otherID]
		if n == nil || n.File == "" || seedSet[n.Service+"\x00"+n.File] {
			return
		}
		if seenEdge[e.ID] {
			return
		}
		seenEdge[e.ID] = true
		en := entry(n, 1)
		en.Refs++
		en.EdgeTypes = appendUnique(en.EdgeTypes, string(e.Type))
		if VerificationRank(e.VerificationState) < VerificationRank(en.BestVerificationState) {
			en.BestVerificationState = e.VerificationState
		}
	}
	for _, s := range seedNodes {
		for _, e := range idx.OutEdges[s.ID] {
			countDirect(e.To, e)
		}
		for _, e := range idx.InEdges[s.ID] {
			countDirect(e.From, e)
		}
	}

	// Neighborhood: undirected multi-source BFS from every seed node — a file
	// two hops away through a shared caller is related even though no directed
	// path connects them, and starting from all seeds at once yields the true
	// minimum hop distance per node.
	type qitem struct {
		id    string
		via   *Edge
		depth int
	}
	visited := make(map[string]bool, len(seedNodes))
	queue := make([]qitem, 0, len(seedNodes))
	for _, s := range seedNodes {
		if !visited[s.ID] {
			visited[s.ID] = true
			queue = append(queue, qitem{id: s.ID})
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		if cur.depth > 0 {
			n := idx.Nodes[cur.id]
			if n != nil && n.File != "" && !seedSet[n.Service+"\x00"+n.File] {
				e := entry(n, cur.depth)
				e.Nodes++
				if cur.depth < e.MinDepth {
					e.MinDepth = cur.depth
				}
				if cur.via != nil {
					e.EdgeTypes = appendUnique(e.EdgeTypes, string(cur.via.Type))
					if VerificationRank(cur.via.VerificationState) < VerificationRank(e.BestVerificationState) {
						e.BestVerificationState = cur.via.VerificationState
					}
				}
			}
		}
		if maxDepth > 0 && cur.depth >= maxDepth {
			continue
		}
		for _, e := range idx.OutEdges[cur.id] {
			if !visited[e.To] {
				visited[e.To] = true
				queue = append(queue, qitem{id: e.To, via: e, depth: cur.depth + 1})
			}
		}
		for _, e := range idx.InEdges[cur.id] {
			if !visited[e.From] {
				visited[e.From] = true
				queue = append(queue, qitem{id: e.From, via: e, depth: cur.depth + 1})
			}
		}
	}

	related = make([]RelatedFileEntry, 0, len(entries))
	for _, e := range entries {
		sort.Strings(e.EdgeTypes)
		related = append(related, *e)
	}
	sort.Slice(related, func(i, j int) bool {
		if related[i].Refs != related[j].Refs {
			return related[i].Refs > related[j].Refs
		}
		if related[i].MinDepth != related[j].MinDepth {
			return related[i].MinDepth < related[j].MinDepth
		}
		if related[i].Nodes != related[j].Nodes {
			return related[i].Nodes > related[j].Nodes
		}
		ri, rj := VerificationRank(related[i].BestVerificationState), VerificationRank(related[j].BestVerificationState)
		if ri != rj {
			return ri < rj
		}
		if related[i].File != related[j].File {
			return related[i].File < related[j].File
		}
		return related[i].Service < related[j].Service
	})
	return seedFiles, related, missing
}
