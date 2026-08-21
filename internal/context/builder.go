package context

import (
	"encoding/json"
	"time"

	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Result is the structured output of a context query.
type Result struct {
	Target       *graph.Node `json:"target"`
	Task         string      `json:"task"`
	Upstream     []TraceNode `json:"upstream"`
	Downstream   []TraceNode `json:"downstream"`
	CrossService []CrossEdge `json:"cross_service"`
	Depth        int         `json:"depth"`
	TotalNodes   int         `json:"total_nodes"`
	TotalEdges   int         `json:"total_edges"`

	// HiddenByClass tallies upstream/downstream nodes excluded by noise-class
	// filtering (Tier NV), keyed by graph.NoiseClass. Empty when no
	// include-set was applied or nothing was filtered.
	HiddenByClass map[graph.NoiseClass]int `json:"hidden_by_class,omitempty"`

	// Unresolved lists references in the traversed files that the indexer
	// could not resolve — edges that may be missing from this answer. Always
	// present ([] when clean) so its absence is never mistaken for certainty.
	Unresolved     []graph.UnresolvedRef `json:"unresolved"`
	UnresolvedNote string                `json:"unresolved_note,omitempty"`

	// VerificationSummary aggregates edge provenance counts. Always present;
	// survives any token budget cut.
	VerificationSummary graph.VerificationSummary `json:"verification_summary"`

	// Trust reports the workspace's last measured eval recall (plan-14 T.0).
	// Always present; Measured=false or Stale=true means this answer is
	// unaudited. Callers set this after Build (Build has no DB access).
	// Survives any token budget.
	Trust graph.TrustStamp `json:"trust"`

	// Epistemic is the single trust verdict derived from Unresolved,
	// VerificationSummary and Trust (EE.0) — set by FinalizeEpistemic, called
	// after Trust and AttachUnresolved. Always present; survives any token
	// budget.
	Epistemic graph.Epistemic `json:"epistemic"`

	// TargetCandidates lists every exact-label match when >1 candidate exists,
	// sorted by (service, file). Always present ([] when unambiguous). Agents
	// should re-query with target_service/--target-service when non-empty.
	TargetCandidates []graph.TargetCandidate `json:"target_candidates"`

	// Status is "ambiguous" (graph.AmbiguityStatusAmbiguous) when
	// TargetCandidates reflects a genuine multi-candidate pick within one
	// service, as distinct from a cross-service name match — see
	// graph.AmbiguityStatus. Empty (omitted) otherwise.
	Status string `json:"status,omitempty"`

	// ResolutionNote is set when the root came from a full-text-search guess
	// rather than a confirmed exact-label match — see graph.ResolutionNote.
	// Empty (omitted) on an ordinary exact resolution.
	ResolutionNote string `json:"resolution_note,omitempty"`

	// Budget records the token-budgeting decision when --max-tokens was set
	// and the detail shape was emitted.
	Budget *budget.Info `json:"budget,omitempty"`
}

// TraceNode is a node in a traversal result with its edge type and depth.
// Meta carries node metadata (including package + resolved_version for
// version-gated matches); EdgeMeta/Confidence describe the connecting edge.
type TraceNode struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Label      string            `json:"label"`
	Service    string            `json:"service"`
	File       string            `json:"file"`
	Line       int               `json:"line"`
	Language   string            `json:"language"`
	Meta       map[string]string `json:"meta,omitempty"`
	EdgeType   string            `json:"edge_type"`
	Confidence string            `json:"confidence,omitempty"`
	EdgeMeta   map[string]string `json:"edge_meta,omitempty"`
	Depth      int               `json:"depth"`
	Snippet    string            `json:"snippet,omitempty"`

	// F.0 provenance (A.1).
	VerificationState   string          `json:"verification_state,omitempty"`
	VerifiedGranularity string          `json:"verified_granularity,omitempty"`
	Sources             json.RawMessage `json:"sources,omitempty"`
}

// CrossEdge represents a connection that crosses service boundaries.
type CrossEdge struct {
	FromService string            `json:"from_service"`
	ToService   string            `json:"to_service"`
	Label       string            `json:"label"`
	EdgeType    string            `json:"edge_type"`
	Confidence  string            `json:"confidence,omitempty"`
	Method      string            `json:"method,omitempty"`
	Path        string            `json:"path,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
}

// Build produces a context result for the given target node and task.
// Depth <= 0 means unlimited traversal. verboseSources controls whether
// per-node Sources contains compact "provider:ref" strings (false, default)
// or full SourceRef structs (true, --verbose-sources). staleAfter is the
// workspace-configured freshness threshold (0 = no stale check). include
// controls which noise-classified nodes (Tier NV) are visible; a nil/empty
// include hides every noise class, matching graph.DefaultNoiseInclude's zero
// value.
func Build(idx *graph.AdjacencyIndex, targetID, task string, depth int, verboseSources bool, staleAfter time.Duration, include graph.NoiseInclude) *Result {
	upstream, downstream, edges, hidden := traverse(idx, targetID, task, depth, verboseSources, include)

	crossService := extractCrossService(idx, upstream, downstream)

	nodeSet := make(map[string]bool, len(upstream)+len(downstream))
	edgeSet := make(map[string]bool, len(upstream)+len(downstream))
	for _, n := range upstream {
		nodeSet[n.ID] = true
		if n.EdgeType != "" {
			edgeSet[n.ID+n.EdgeType] = true
		}
	}
	for _, n := range downstream {
		nodeSet[n.ID] = true
		if n.EdgeType != "" {
			edgeSet[n.ID+n.EdgeType] = true
		}
	}

	return &Result{
		Target:              idx.Nodes[targetID],
		Task:                task,
		Upstream:            upstream,
		Downstream:          downstream,
		CrossService:        crossService,
		Depth:               depth,
		TotalNodes:          len(nodeSet) + 1, // +1 for the target itself
		TotalEdges:          len(edgeSet),
		HiddenByClass:       hidden,
		Unresolved:          []graph.UnresolvedRef{},
		VerificationSummary: graph.BuildVerificationSummaryAt(edges, staleAfter, time.Now()),
	}
}

// AttachUnresolved scopes the workspace's unresolved-reference ledger to the
// files touched by this traversal and records the matches on the result.
func (r *Result) AttachUnresolved(refs []graph.UnresolvedRef) {
	files := make(map[string]bool, len(r.Upstream)+len(r.Downstream)+1)
	if r.Target != nil {
		files[r.Target.File] = true
	}
	for _, n := range r.Upstream {
		files[n.File] = true
	}
	for _, n := range r.Downstream {
		files[n.File] = true
	}
	r.Unresolved = graph.UnresolvedInFiles(refs, files)
	r.UnresolvedNote = graph.UnresolvedNote(len(r.Unresolved))
}

// FinalizeEpistemic computes the epistemic verdict (EE.0) from this result's
// already-populated Unresolved, VerificationSummary and Trust sections, plus
// the confidence of the traversed edges. Call after Trust is set and
// AttachUnresolved has run — the order every call site already uses — and
// before ApplyBudget, since epistemic must survive any token-budget cut, the
// same as verification_summary/trust.
func (r *Result) FinalizeEpistemic() *Result {
	confidences := make([]string, 0, len(r.Upstream)+len(r.Downstream))
	for _, n := range r.Upstream {
		confidences = append(confidences, n.Confidence)
	}
	for _, n := range r.Downstream {
		confidences = append(confidences, n.Confidence)
	}
	r.Epistemic = graph.BuildEpistemic(r.Unresolved, graph.HasWeakConfidence(confidences), r.VerificationSummary, r.Trust)
	return r
}

// traverse runs BFS in the appropriate directions for the given task.
// Returns the upstream/downstream node lists, all traversed edges (for
// computing the VerificationSummary), and a tally of nodes hidden by
// noise-class filtering (Tier NV), keyed by graph.NoiseClass.
func traverse(idx *graph.AdjacencyIndex, targetID, task string, depth int, verboseSources bool, include graph.NoiseInclude) (upstream, downstream []TraceNode, edges []graph.Edge, hidden map[graph.NoiseClass]int) {
	hidden = map[graph.NoiseClass]int{}
	switch task {
	case "impact":
		upstream, edges = toTraceNodes(graph.Ancestors(idx, targetID, depth), verboseSources, include, hidden)
	case "generate":
		downstream, edges = toTraceNodes(graph.Descendants(idx, targetID, depth), verboseSources, include, hidden)
	case "debug", "refactor":
		var upEdges, downEdges []graph.Edge
		upstream, upEdges = toTraceNodes(graph.Ancestors(idx, targetID, depth), verboseSources, include, hidden)
		downstream, downEdges = toTraceNodes(graph.Descendants(idx, targetID, depth), verboseSources, include, hidden)
		edges = append(upEdges, downEdges...)
	default:
		var upEdges, downEdges []graph.Edge
		upstream, upEdges = toTraceNodes(graph.Ancestors(idx, targetID, depth), verboseSources, include, hidden)
		downstream, downEdges = toTraceNodes(graph.Descendants(idx, targetID, depth), verboseSources, include, hidden)
		edges = append(upEdges, downEdges...)
	}
	return
}

// marshalSources serialises edge Sources as compact "provider:ref" strings
// with age annotation (default) or full SourceRef structs (verboseSources=true).
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

func toTraceNodes(results []graph.TraversalResult, verboseSources bool, include graph.NoiseInclude, hidden map[graph.NoiseClass]int) ([]TraceNode, []graph.Edge) {
	out := make([]TraceNode, 0, len(results))
	var edges []graph.Edge
	for _, r := range results {
		if r.Node == nil {
			continue
		}
		if r.Via != nil {
			if class := graph.ClassifyEdgeNoise(r.Via, r.Node); !include.Allows(class) {
				hidden[class]++
				continue
			}
		}
		tn := TraceNode{
			ID:       r.Node.ID,
			Type:     string(r.Node.Type),
			Label:    r.Node.Label,
			Service:  r.Node.Service,
			File:     r.Node.File,
			Line:     r.Node.Line,
			Language: r.Node.Language,
			Meta:     r.Node.Meta,
			Depth:    r.Depth,
		}
		if r.Via != nil {
			tn.EdgeType = string(r.Via.Type)
			tn.Confidence = r.Via.Confidence
			tn.EdgeMeta = r.Via.Meta
			tn.VerificationState = r.Via.VerificationState
			tn.VerifiedGranularity = r.Via.VerifiedGranularity
			tn.Sources = marshalSources(r.Via.Sources, verboseSources)
			edges = append(edges, *r.Via)
		}
		out = append(out, tn)
	}
	return out, edges
}

// extractCrossService finds edges in the traversal results that cross service
// boundaries and returns them as CrossEdge entries.
func extractCrossService(idx *graph.AdjacencyIndex, upstream, downstream []TraceNode) []CrossEdge {
	// Collect all node IDs from results (plus implied edges via OutEdges).
	allNodeIDs := make(map[string]bool, len(upstream)+len(downstream))
	for _, n := range upstream {
		allNodeIDs[n.ID] = true
	}
	for _, n := range downstream {
		allNodeIDs[n.ID] = true
	}

	seen := make(map[string]bool)
	var out []CrossEdge
	for nodeID := range allNodeIDs {
		node := idx.Nodes[nodeID]
		if node == nil {
			continue
		}
		// A service node's own Service field is "" by design (it's the
		// containment root, not a member of itself) — comparing that against
		// every file it contains (Service = the service name) makes every
		// one of its `contains` edges look cross-service. Skip: a service
		// node's own outgoing containment edges are same-service by
		// definition, however its bookkeeping field reads.
		if node.Type == graph.NodeTypeService {
			continue
		}
		for _, e := range idx.OutEdges[nodeID] {
			toNode := idx.Nodes[e.To]
			if toNode == nil {
				continue
			}
			if node.Service == toNode.Service {
				continue
			}
			key := e.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, CrossEdge{
				FromService: node.Service,
				ToService:   toNode.Service,
				Label:       e.Label,
				EdgeType:    string(e.Type),
				Confidence:  e.Confidence,
				Method:      e.Method,
				Path:        e.Path,
				Meta:        e.Meta,
			})
		}
	}
	return out
}
