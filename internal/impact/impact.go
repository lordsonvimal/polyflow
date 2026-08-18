// Package impact answers "what is impacted if I change X": the backward
// blast radius of a node, plus entry points, affected services, and
// cross-service triggers. Shared by the CLI and the MCP server so both
// speak the same output contract.
package impact

import (
	"encoding/json"
	"sort"
	"time"

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

	// Structural is true when the path from the target to this node crosses
	// a contains/declares/instantiates/uses_type edge — the node is reached
	// through the code's shape (e.g. "constructs a struct that has this
	// method"), not a verified call chain. See graph.TraversalResult.Structural.
	Structural bool `json:"structural,omitempty"`

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
	// Direction is the question this answer is to: "backward" (what breaks),
	// "forward" (what this reaches) or "both". Always present.
	Direction    string `json:"direction"`
	TotalCallers int    `json:"total_callers"`

	// Unresolved lists references in the traversed files that the indexer
	// could not resolve — the blast radius may be under-reported where these
	// appear. Always present ([] when clean).
	Unresolved     []graph.UnresolvedRef `json:"unresolved"`
	UnresolvedNote string                `json:"unresolved_note,omitempty"`

	// VerificationSummary aggregates edge provenance counts. Always present
	// (never absent — absence would look like certainty); survives any token budget.
	VerificationSummary graph.VerificationSummary `json:"verification_summary"`

	// Trust reports the workspace's last measured eval recall (plan-14 T.0).
	// Always present; Measured=false or Stale=true means this answer is
	// unaudited. Callers set this after Build (Build has no DB access).
	// Survives any token budget.
	Trust graph.TrustStamp `json:"trust"`

	// TargetCandidates lists every exact-label match when >1 candidate exists,
	// sorted by (service, file). Always present ([] when unambiguous). Agents
	// should re-query with target_service/--target-service when non-empty.
	TargetCandidates []graph.TargetCandidate `json:"target_candidates"`

	// Targets is set instead of a single Target when the query matched the
	// same label in >1 service and target_service was left unspecified (e.g.
	// a client-side call and its server-side handler sharing a name across an
	// HTTP contract). Target still holds the first resolved root for
	// backward-compat with callers that only read Target; Targets lists every
	// root whose blast radius was unioned into Callers below. Omitted (nil)
	// in the single-root case.
	Targets []*graph.Node `json:"targets,omitempty"`

	// ResolutionNote is set when Target came from a full-text-search guess
	// rather than a confirmed exact-label match — see graph.ResolutionNote.
	// Empty (omitted) on an ordinary exact resolution.
	ResolutionNote string `json:"resolution_note,omitempty"`

	// Budget records the token-budgeting decision when a budget was set and
	// the detail shape was emitted.
	Budget *budget.Info `json:"budget,omitempty"`
}

// Options shapes a Build call. The zero value is the historical behaviour
// (backward, raw walk) so a caller that only wants a blast radius need not
// think about any of it.
type Options struct {
	Depth   int    // <= 0 means unlimited
	Service string // filter results to one service ("" = all)
	// Direction selects which question is being asked: "backward" (default)
	// is "what breaks if I change this", "forward" is "what does this reach"
	// — what an agent needs to read. "both" unions them.
	Direction string
	// Policy shapes the walk; see graph.TraversalPolicy. Callers wanting the
	// blast-radius shape pass graph.BlastRadiusPolicy().
	Policy graph.TraversalPolicy

	VerboseSources bool
	StaleAfter     time.Duration
}

// Build computes the blast radius of root under opts. verboseSources controls
// whether per-caller Sources contains compact "provider:ref" strings (false,
// default) or full SourceRef structs (true, --verbose-sources). StaleAfter is
// the workspace-configured freshness threshold (0 = no stale check).
//
// The result field is still called Callers for compatibility with the output
// contract; under Direction "forward" they are callees.
func Build(idx *graph.AdjacencyIndex, root *graph.Node, opts Options) *Result {
	ancestors := traverseFrom(idx, root.ID, opts)
	verboseSources := opts.VerboseSources

	callers, entryPoints, servicesAffected, triggers, edges := assemble(idx, ancestors, verboseSources)

	return &Result{
		Target:               root,
		Callers:              callers,
		EntryPoints:          entryPoints,
		ServicesAffected:     servicesAffected,
		CrossServiceTriggers: triggers,
		Depth:                opts.Depth,
		Direction:            direction(opts.Direction),
		TotalCallers:         len(callers),
		Unresolved:           []graph.UnresolvedRef{},
		VerificationSummary:  graph.BuildVerificationSummaryAt(edges, opts.StaleAfter, time.Now()),
	}
}

// BuildMulti computes and unions the blast radius of every node in roots —
// one traversal per root, merged before assembly. Built for the case an
// exact-label query matches the same symbol in >1 service (a client call and
// its server-side handler sharing a name across an HTTP contract): rather
// than making the agent call Build once per service with target_service
// pinned, the MCP impact handler resolves every service's root up front and
// calls this once so the blast radii come back merged in a single response.
//
// A node reachable from more than one root keeps its lowest-depth hit — the
// same rule Build's own "both"-direction merge uses — so a symbol close to
// one root and distant from another reports the closer, stronger claim.
func BuildMulti(idx *graph.AdjacencyIndex, roots []*graph.Node, opts Options) *Result {
	if len(roots) == 1 {
		r := Build(idx, roots[0], opts)
		return r
	}

	merged := make(map[string]graph.TraversalResult)
	for _, root := range roots {
		for _, a := range traverseFrom(idx, root.ID, opts) {
			if existing, ok := merged[a.Node.ID]; !ok || a.Depth < existing.Depth {
				merged[a.Node.ID] = a
			}
		}
	}
	ancestors := make([]graph.TraversalResult, 0, len(merged))
	for _, a := range merged {
		ancestors = append(ancestors, a)
	}
	// Map iteration must never reach output (bug-class rule 2): order by
	// (depth, node ID) for a deterministic result independent of map order.
	sort.Slice(ancestors, func(i, j int) bool {
		if ancestors[i].Depth != ancestors[j].Depth {
			return ancestors[i].Depth < ancestors[j].Depth
		}
		return ancestors[i].Node.ID < ancestors[j].Node.ID
	})

	callers, entryPoints, servicesAffected, triggers, edges := assemble(idx, ancestors, opts.VerboseSources)

	return &Result{
		Target:               roots[0],
		Targets:              roots,
		Callers:              callers,
		EntryPoints:          entryPoints,
		ServicesAffected:     servicesAffected,
		CrossServiceTriggers: triggers,
		Depth:                opts.Depth,
		Direction:            direction(opts.Direction),
		TotalCallers:         len(callers),
		Unresolved:           []graph.UnresolvedRef{},
		VerificationSummary:  graph.BuildVerificationSummaryAt(edges, opts.StaleAfter, time.Now()),
	}
}

// traverseFrom runs the direction/service-filter logic shared by Build and
// BuildMulti for a single root ID.
func traverseFrom(idx *graph.AdjacencyIndex, rootID string, opts Options) []graph.TraversalResult {
	var ancestors []graph.TraversalResult
	switch opts.Direction {
	case "forward":
		ancestors = graph.TraverseWithPolicy(idx, rootID, "out", graph.BFS, opts.Depth, opts.Policy)
	case "both":
		ancestors = graph.TraverseWithPolicy(idx, rootID, "in", graph.BFS, opts.Depth, opts.Policy)
		seen := make(map[string]bool, len(ancestors))
		for _, a := range ancestors {
			seen[a.Node.ID] = true
		}
		// A node reachable both ways keeps its backward hit: "it breaks" is
		// the stronger claim and the one the depth was measured for.
		for _, d := range graph.TraverseWithPolicy(idx, rootID, "out", graph.BFS, opts.Depth, opts.Policy) {
			if !seen[d.Node.ID] {
				ancestors = append(ancestors, d)
			}
		}
	default:
		ancestors = graph.TraverseWithPolicy(idx, rootID, "in", graph.BFS, opts.Depth, opts.Policy)
	}

	if opts.Service != "" {
		filtered := ancestors[:0]
		for _, a := range ancestors {
			if a.Node.Service == opts.Service {
				filtered = append(filtered, a)
			}
		}
		ancestors = filtered
	}
	return ancestors
}

// direction normalises the reported direction so the output never omits it —
// an absent direction reads as "backward" to some agents and "both" to others.
func direction(d string) string {
	if d == "" {
		return "backward"
	}
	return d
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
			ID:         a.Node.ID,
			Label:      a.Node.Label,
			Type:       string(a.Node.Type),
			Service:    a.Node.Service,
			File:       a.Node.File,
			Line:       a.Node.Line,
			Meta:       a.Node.Meta,
			Depth:      a.Depth,
			Structural: a.Structural,
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
	// Map iteration must never reach output (bug-class rule 2).
	sort.Strings(servicesAffected)

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
	sort.Slice(triggers, func(i, j int) bool { return triggers[i].FromService < triggers[j].FromService })

	return callers, entryPoints, servicesAffected, triggers, edges
}

// marshalSources serialises edge Sources as compact "provider:ref" strings
// with age annotation (default) or full SourceRef structs (verboseSources=true).
// Returns nil when the edge has no Sources.
func marshalSources(sources []graph.SourceRef, verbose bool) json.RawMessage {
	if len(sources) == 0 {
		return nil
	}
	var v any
	if verbose {
		v = graph.SortedSources(sources)
	} else {
		v = graph.CompactSourcesAt(sources, time.Now())
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
