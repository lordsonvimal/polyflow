package graph

import (
	"sort"
	"strings"
)

// FileSummary aggregates the graph nodes contained in one source file.
type FileSummary struct {
	File    string           `json:"file"`
	Service string           `json:"service"`
	Counts  map[NodeType]int `json:"counts"`
}

// ListFiles returns per-file node aggregates for every file that matches q
// (case-insensitive substring; empty q matches all), sorted by path.
// limit <= 0 means unlimited.
func ListFiles(idx *AdjacencyIndex, q string, limit int) []FileSummary {
	type key struct{ service, file string }
	q = strings.ToLower(q)
	byFile := make(map[key]*FileSummary)
	for _, n := range idx.Nodes {
		if n.File == "" {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(n.File), q) {
			continue
		}
		k := key{n.Service, n.File}
		fs, ok := byFile[k]
		if !ok {
			fs = &FileSummary{File: n.File, Service: n.Service, Counts: make(map[NodeType]int)}
			byFile[k] = fs
		}
		fs.Counts[n.Type]++
	}
	out := make([]FileSummary, 0, len(byFile))
	for _, fs := range byFile {
		out = append(out, *fs)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Service < out[j].Service
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// NodesInFile returns all nodes whose File matches path, sorted by line.
// If service is non-empty the match is restricted to that service. An exact
// path match is preferred; when none exists, nodes whose path ends with
// "/"+path are returned (so short relative paths resolve).
func NodesInFile(idx *AdjacencyIndex, service, path string) []*Node {
	var exact, suffix []*Node
	for _, n := range idx.Nodes {
		if service != "" && n.Service != service {
			continue
		}
		if n.File == path {
			exact = append(exact, n)
		} else if strings.HasSuffix(n.File, "/"+path) {
			suffix = append(suffix, n)
		}
	}
	out := exact
	if len(out) == 0 {
		out = suffix
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// FileImpactEntry is one file reached by traversing from a source file's nodes.
type FileImpactEntry struct {
	File      string   `json:"file"`
	Service   string   `json:"service"`
	Nodes     int      `json:"nodes"`
	MinDepth  int      `json:"min_depth"`
	EdgeTypes []string `json:"edge_types"`
}

// FileImpact traverses from every node in the given file and groups the
// reachable nodes by file. direction is "forward" (downstream), "backward"
// (upstream) or "both", matching the trace API. The source file itself is
// excluded from the result. maxDepth <= 0 means unlimited.
func FileImpact(idx *AdjacencyIndex, service, path, direction string, maxDepth int) []FileImpactEntry {
	seeds := NodesInFile(idx, service, path)
	if len(seeds) == 0 {
		return nil
	}
	// The seed file may resolve by suffix; exclude every resolved seed path.
	seedFiles := make(map[string]bool, 1)
	for _, s := range seeds {
		seedFiles[s.Service+"\x00"+s.File] = true
	}

	directions := []string{"out"}
	switch direction {
	case "backward":
		directions = []string{"in"}
	case "both":
		directions = []string{"out", "in"}
	}

	type key struct{ service, file string }
	entries := make(map[key]*FileImpactEntry)
	// Track per-node best depth so overlapping traversals keep the minimum.
	nodeDepth := make(map[string]int)

	for _, dir := range directions {
		for _, seed := range seeds {
			for _, res := range Traverse(idx, seed.ID, dir, BFS, maxDepth) {
				if prev, seen := nodeDepth[res.Node.ID]; seen && prev <= res.Depth {
					continue
				}
				firstVisit := func() bool { _, seen := nodeDepth[res.Node.ID]; return !seen }()
				nodeDepth[res.Node.ID] = res.Depth

				if seedFiles[res.Node.Service+"\x00"+res.Node.File] {
					continue
				}
				k := key{res.Node.Service, res.Node.File}
				e, ok := entries[k]
				if !ok {
					e = &FileImpactEntry{File: res.Node.File, Service: res.Node.Service, MinDepth: res.Depth}
					entries[k] = e
				}
				if firstVisit {
					e.Nodes++
				}
				if res.Depth < e.MinDepth {
					e.MinDepth = res.Depth
				}
				if res.Via != nil {
					e.EdgeTypes = appendUnique(e.EdgeTypes, string(res.Via.Type))
				}
			}
		}
	}

	out := make([]FileImpactEntry, 0, len(entries))
	for _, e := range entries {
		sort.Strings(e.EdgeTypes)
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MinDepth != out[j].MinDepth {
			return out[i].MinDepth < out[j].MinDepth
		}
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Service < out[j].Service
	})
	return out
}

func appendUnique(list []string, v string) []string {
	for _, s := range list {
		if s == v {
			return list
		}
	}
	return append(list, v)
}
