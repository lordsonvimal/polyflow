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

// Build scans idx for function/method nodes with no inbound invoking edge
// (see invokingEdgeTypes), excluding nodes graph.ClassifyEntrypoint already
// recognises as an entry point (HTTP handlers, routes, workers, subscribers,
// gRPC/GraphQL handlers, and functions tagged meta.root_kind=entrypoint) —
// those are meant to have zero static callers. Structural edges (contains,
// declares, ...) never count as a caller.
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
		// meta.root_kind=callback (referenced as a value / satisfies an
		// external interface — invoked by a framework, not by a literal call
		// site) is a distinct bucket from entrypoint but the same "not
		// actionable dead code" verdict: Go's SSA-referenced functions and a
		// JS object-literal callback value (`{ onProceed: function(){...} }`)
		// both land here. ClassifyEntrypoint already computes this as
		// skippedRootKind; check it alongside ok rather than duplicating the
		// meta read.
		if _, skippedRootKind, ok := graph.ClassifyEntrypoint(n); ok || skippedRootKind == "callback" {
			continue
		}
		// Test functions are invoked by the test runner, not by a static
		// caller in the graph — every one of them is a zero-caller node by
		// construction. Flagging them as dead code is never actionable and
		// drowns out real hits (measured: 70% of a live scan's flagged
		// functions were _test.go/.spec. files).
		if graph.IsTestFilePath(n.File) {
			continue
		}
		// A method the indexer already determined is invoked by a framework
		// or the standard library through an interface value or reflection
		// (GORM's TableName/Before*/After* hooks, Go's own Stringer/error/
		// Marshaler/Scanner/Handler, ...) is zero-caller by construction, the
		// same way an HTTP handler is. The name list itself is declared
		// per-language/per-package in the pattern registry (see
		// patterns.PatternFile.ReflectDispatchedMethods), package/version-
		// gated per service at index time — not hardcoded here — so a Ruby/
		// JS/Python method sharing one of these names on a repo that never
		// pulled in GORM is untouched and stays a real deadcode candidate.
		if n.Meta[graph.MetaReflectDispatched] == "true" {
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

// invokingEdgeTypes are the edge types that represent a real invocation of
// their target for deadcode's purposes. EdgeTypeSpawns (`go f()`) is a
// genuine caller the same way a direct call is — a goroutine target with no
// inbound EdgeTypeCalls was a systematic false positive (every function only
// ever launched via `go x.method()`, e.g. a scheduler's background loop,
// qualified as zero-caller by construction).
var invokingEdgeTypes = map[graph.EdgeType]bool{
	graph.EdgeTypeCalls:  true,
	graph.EdgeTypeSpawns: true,
}

// hasCaller reports whether n has at least one inbound invoking edge.
func hasCaller(idx *graph.AdjacencyIndex, id string) bool {
	for _, e := range idx.InEdges[id] {
		if invokingEdgeTypes[e.Type] {
			return true
		}
	}
	return false
}
