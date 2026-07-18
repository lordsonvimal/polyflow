// Package impact answers "what is impacted if I change X": the backward
// blast radius of a node, plus entry points, affected services, and
// cross-service triggers. Shared by the CLI and the MCP server so both
// speak the same output contract.
package impact

import (
	"encoding/json"

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

	// F.0 provenance (A.1): always present when the edge has been fused.
	VerificationState   string          `json:"verification_state,omitempty"`
	VerifiedGranularity string          `json:"verified_granularity,omitempty"`
	Sources             json.RawMessage `json:"sources,omitempty"` // compact "provider:ref" strings; full SourceRef with verboseSources
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

	// VerificationSummary aggregates edge provenance counts. Always present
	// (never absent — absence would look like certainty); survives any token budget.
	VerificationSummary graph.VerificationSummary `json:"verification_summary"`

	// Budget records the token-budgeting decision when a budget was set and
	// the detail shape was emitted.
	Budget *budget.Info `json:"budget,omitempty"`
}

// Build computes the blast radius of root: its ancestors up to depth
// (<= 0 means unlimited), optionally filtered to one service. verboseSources
// controls whether per-caller Sources contains compact "provider:ref" strings
// (false, default) or full SourceRef structs (true, --verbose-sources).
func Build(idx *graph.AdjacencyIndex, root *graph.Node, depth int, service string, verboseSources bool) *Result {
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

	callers, entryPoints, servicesAffected, triggers, edges := assemble(idx, ancestors, verboseSources)

	return &Result{
		Target:               root,
		Callers:              callers,
		EntryPoints:          entryPoints,
		ServicesAffected:     servicesAffected,
		CrossServiceTriggers: triggers,
		Depth:                depth,
		TotalCallers:         len(callers),
		Unresolved:           []graph.UnresolvedRef{},
		VerificationSummary:  graph.BuildVerificationSummary(edges),
	}
}

// assemble turns a traversed ancestor set into the shared output pieces:
// callers with edge context and provenance, entry points (ancestors with no
// incoming edges), the affected-service set, cross-service triggers (edges
// arriving at any ancestor from a different service), and the collected edges
// used to compute the VerificationSummary.
func assemble(idx *graph.AdjacencyIndex, ancestors []graph.TraversalResult, verboseSources bool) ([]Caller, []*graph.Node, []string, []CrossServiceTrigger, []graph.Edge) {
	callers := make([]Caller, 0, len(ancestors))
	var edges []graph.Edge
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
			c.VerificationState = a.Via.VerificationState
			c.VerifiedGranularity = a.Via.VerifiedGranularity
			c.Sources = marshalSources(a.Via.Sources, verboseSources)
			edges = append(edges, *a.Via)
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

	return callers, entryPoints, servicesAffected, triggers, edges
}

// marshalSources serialises edge Sources as compact "provider:ref" strings
// (default) or full SourceRef structs (verboseSources=true). Returns nil when
// the edge has no Sources.
func marshalSources(sources []graph.SourceRef, verbose bool) json.RawMessage {
	if len(sources) == 0 {
		return nil
	}
	var v any
	if verbose {
		v = graph.SortedSources(sources)
	} else {
		v = graph.CompactSources(sources)
	}
	b, _ := json.Marshal(v)
	return json.RawMessage(b)
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
