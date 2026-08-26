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
		if _, _, ok := graph.ClassifyEntrypoint(n); ok {
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
		// Well-known Go interface methods (GORM's Tabler/hook interfaces,
		// encoding/json's Marshaler, fmt.Stringer, sql.Scanner, ...) are
		// invoked by the framework through an interface value or reflection,
		// never by a literal call site in application source — so they are
		// zero-caller by construction, the same way an HTTP handler is.
		// Gated to Language "go" + NodeTypeMethod: this is a Go naming
		// convention (stdlib and GORM alike), not a cross-language one — a
		// Ruby/JS/Python method that happens to share one of these names
		// (e.g. a JS class's own `value()`) implements no such interface and
		// must stay a real deadcode candidate.
		if n.Type == graph.NodeTypeMethod && n.Language == "go" && reflectDispatchedMethod[n.Label] {
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

// reflectDispatchedMethod names Go methods that a framework or the standard
// library dispatches through an interface value or reflection rather than a
// literal call site: GORM's Tabler and model-lifecycle hooks (TableName,
// Before*/After*), encoding/json's Marshaler/Unmarshaler, fmt.Stringer,
// database/sql's Scanner/Valuer, and net/http's Handler. Mirrors the same
// reserved-name list staticcheck's U1000 check exempts, for the same reason.
var reflectDispatchedMethod = map[string]bool{
	"TableName":     true,
	"BeforeCreate":  true,
	"AfterCreate":   true,
	"BeforeUpdate":  true,
	"AfterUpdate":   true,
	"BeforeSave":    true,
	"AfterSave":     true,
	"BeforeDelete":  true,
	"AfterDelete":   true,
	"AfterFind":     true,
	"String":        true,
	"Error":         true,
	"MarshalJSON":   true,
	"UnmarshalJSON": true,
	"MarshalText":   true,
	"UnmarshalText": true,
	"Scan":          true,
	"Value":         true,
	"ServeHTTP":     true,
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
