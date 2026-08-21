// Package deadcode finds function/method nodes with zero inbound calls
// edges: a fixed-shape, full-graph scan rather than an ad-hoc query
// language, matching the rest of polyflow's tool set.
package deadcode

import (
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Item is one flagged zero-caller function or method.
type Item struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Type    string `json:"type"`
	Service string `json:"service"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line,omitempty"`
}

// Result is the output of a dead-code scan.
type Result struct {
	Functions []Item `json:"functions"`
	Total     int    `json:"total"`
}

// Options scopes a Build scan.
type Options struct {
	Service string // "" = every service
	File    string // "" = every file
}

// Build scans idx for function/method nodes with no inbound `calls` edge,
// excluding nodes graph.ClassifyEntrypoint already recognises as an entry
// point (HTTP handlers, routes, workers, subscribers, gRPC/GraphQL handlers,
// and functions tagged meta.root_kind=entrypoint) — those are meant to have
// zero static callers. Structural edges (contains, declares, ...) never
// count as a caller: only graph.EdgeTypeCalls does.
func Build(idx *graph.AdjacencyIndex, opts Options) *Result {
	var items []Item
	for _, n := range idx.Nodes {
		if n.Type != graph.NodeTypeFunction && n.Type != graph.NodeTypeMethod {
			continue
		}
		if opts.Service != "" && n.Service != opts.Service {
			continue
		}
		if opts.File != "" && n.File != opts.File {
			continue
		}
		if _, _, ok := graph.ClassifyEntrypoint(n); ok {
			continue
		}
		if hasCaller(idx, n.ID) {
			continue
		}
		items = append(items, Item{
			ID:      n.ID,
			Label:   n.Label,
			Type:    string(n.Type),
			Service: n.Service,
			File:    n.File,
			Line:    n.Line,
			EndLine: n.EndLine,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Service != items[j].Service {
			return items[i].Service < items[j].Service
		}
		if items[i].File != items[j].File {
			return items[i].File < items[j].File
		}
		if items[i].Line != items[j].Line {
			return items[i].Line < items[j].Line
		}
		return items[i].ID < items[j].ID
	})
	if items == nil {
		items = []Item{}
	}

	return &Result{Functions: items, Total: len(items)}
}

// hasCaller reports whether n has at least one inbound graph.EdgeTypeCalls
// edge — the only edge type that represents a real caller for this scan.
func hasCaller(idx *graph.AdjacencyIndex, id string) bool {
	for _, e := range idx.InEdges[id] {
		if e.Type == graph.EdgeTypeCalls {
			return true
		}
	}
	return false
}
