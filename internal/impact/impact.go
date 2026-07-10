// Package impact answers "what is impacted if I change X": the backward
// blast radius of a node, plus entry points, affected services, and
// cross-service triggers. Shared by the CLI and the MCP server so both
// speak the same output contract.
package impact

import (
	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Caller is one node in the blast radius with the edge that reached it.
type Caller struct {
	ID         string            `json:"id"`
	Label      string            `json:"label"`
	Type       string            `json:"type"`
	Service    string            `json:"service"`
	File       string            `json:"file"`
	Line       int               `json:"line"`
	Meta       map[string]string `json:"meta,omitempty"`
	EdgeType   string            `json:"edge_type"`
	Confidence string            `json:"confidence,omitempty"`
	EdgeMeta   map[string]string `json:"edge_meta,omitempty"`
	Depth      int               `json:"depth"`
	Snippet    string            `json:"snippet,omitempty"`
}

// CrossServiceTrigger counts edges arriving at the blast radius from
// another service.
type CrossServiceTrigger struct {
	FromService string `json:"from_service"`
	EdgeCount   int    `json:"edge_count"`
}

// Result is the structured output of an impact query.
type Result struct {
	Target               *graph.Node           `json:"target"`
	Callers              []Caller              `json:"callers"`
	EntryPoints          []*graph.Node         `json:"entry_points"`
	ServicesAffected     []string              `json:"services_affected"`
	CrossServiceTriggers []CrossServiceTrigger `json:"cross_service_triggers"`
	Depth                int                   `json:"depth"`
	TotalCallers         int                   `json:"total_callers"`

	// Unresolved lists references in the traversed files that the indexer
	// could not resolve — the blast radius may be under-reported where these
	// appear. Always present ([] when clean).
	Unresolved     []graph.UnresolvedRef `json:"unresolved"`
	UnresolvedNote string                `json:"unresolved_note,omitempty"`

	// Budget records the token-budgeting decision when a budget was set and
	// the detail shape was emitted.
	Budget *budget.Info `json:"budget,omitempty"`
}

// Build computes the blast radius of root: its ancestors up to depth
// (<= 0 means unlimited), optionally filtered to one service.
func Build(idx *graph.AdjacencyIndex, root *graph.Node, depth int, service string) *Result {
	ancestors := graph.Ancestors(idx, root.ID, depth)

	if service != "" {
		filtered := ancestors[:0]
		for _, a := range ancestors {
			if a.Node.Service == service {
				filtered = append(filtered, a)
			}
		}
		ancestors = filtered
	}

	callers, entryPoints, servicesAffected, triggers := assemble(idx, ancestors)

	return &Result{
		Target:               root,
		Callers:              callers,
		EntryPoints:          entryPoints,
		ServicesAffected:     servicesAffected,
		CrossServiceTriggers: triggers,
		Depth:                depth,
		TotalCallers:         len(callers),
		Unresolved:           []graph.UnresolvedRef{},
	}
}

// assemble turns a traversed ancestor set into the shared output pieces:
// callers with edge context, entry points (ancestors with no incoming
// edges), the affected-service set, and cross-service triggers (edges
// arriving at any ancestor from a different service).
func assemble(idx *graph.AdjacencyIndex, ancestors []graph.TraversalResult) ([]Caller, []*graph.Node, []string, []CrossServiceTrigger) {
	callers := make([]Caller, 0, len(ancestors))
	for _, a := range ancestors {
		c := Caller{
			ID:      a.Node.ID,
			Label:   a.Node.Label,
			Type:    string(a.Node.Type),
			Service: a.Node.Service,
			File:    a.Node.File,
			Line:    a.Node.Line,
			Meta:    a.Node.Meta,
			Depth:   a.Depth,
		}
		if a.Via != nil {
			c.EdgeType = string(a.Via.Type)
			c.Confidence = a.Via.Confidence
			c.EdgeMeta = a.Via.Meta
		}
		callers = append(callers, c)
	}

	var entryPoints []*graph.Node
	for _, a := range ancestors {
		if len(idx.InEdges[a.Node.ID]) == 0 {
			entryPoints = append(entryPoints, a.Node)
		}
	}

	svcSet := make(map[string]bool)
	for _, a := range ancestors {
		svcSet[a.Node.Service] = true
	}
	servicesAffected := make([]string, 0, len(svcSet))
	for svc := range svcSet {
		servicesAffected = append(servicesAffected, svc)
	}

	xsCount := make(map[string]int)
	for _, a := range ancestors {
		for _, e := range idx.InEdges[a.Node.ID] {
			fromNode := idx.Nodes[e.From]
			if fromNode != nil && fromNode.Service != a.Node.Service {
				xsCount[fromNode.Service]++
			}
		}
	}
	triggers := make([]CrossServiceTrigger, 0, len(xsCount))
	for svc, cnt := range xsCount {
		triggers = append(triggers, CrossServiceTrigger{FromService: svc, EdgeCount: cnt})
	}

	return callers, entryPoints, servicesAffected, triggers
}

// AttachUnresolved scopes the workspace's unresolved-reference ledger to the
// files touched by this traversal and records the matches on the result.
func (r *Result) AttachUnresolved(refs []graph.UnresolvedRef) {
	files := make(map[string]bool, len(r.Callers)+1)
	if r.Target != nil {
		files[r.Target.File] = true
	}
	for _, c := range r.Callers {
		files[c.File] = true
	}
	r.Unresolved = graph.UnresolvedInFiles(refs, files)
	r.UnresolvedNote = graph.UnresolvedNote(len(r.Unresolved))
}
