// Package trace produces multi-hop flow traces from the graph: a flat
// traversal listing plus enumerated linear chains (A → B → C → D), each hop
// carrying full node/edge metadata (including package + resolved_version for
// version-gated matches) so agents get complete, version-aware answers.
package trace

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// MaxChains caps chain enumeration: path counts are combinatorial in dense
// graphs, and past this point more chains stop being informative.
const MaxChains = 100

// Hop is one node in a trace, together with the edge that led to it.
// The edge fields are empty on a chain's first hop.
type Hop struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Label    string            `json:"label"`
	Service  string            `json:"service"`
	File     string            `json:"file,omitempty"`
	Line     int               `json:"line,omitempty"`
	Language string            `json:"language,omitempty"`
	NodeMeta map[string]string `json:"node_meta,omitempty"`

	EdgeType     string            `json:"edge_type,omitempty"`
	EdgeLabel    string            `json:"edge_label,omitempty"`
	Confidence   string            `json:"confidence,omitempty"`
	EdgeMeta     map[string]string `json:"edge_meta,omitempty"`
	CrossService bool              `json:"cross_service,omitempty"`
	Depth        int               `json:"depth,omitempty"`

	// F.0 provenance (A.1).
	VerificationState   string          `json:"verification_state,omitempty"`
	VerifiedGranularity string          `json:"verified_granularity,omitempty"`
	Sources             json.RawMessage `json:"sources,omitempty"`
}

// Chain is one linear root-to-leaf path. Backward chains are stored
// source-first, so Text always reads left to right in flow order and ends at
// the trace root.
type Chain struct {
	Hops []Hop  `json:"hops"`
	Text string `json:"text"`
}

// Result is the structured output of a trace query.
type Result struct {
	Root      *graph.Node `json:"root"`
	Direction string      `json:"direction"`
	Depth     int         `json:"depth"`
	Nodes     []Hop       `json:"nodes"`
	Chains    []Chain     `json:"chains"`
	EdgeTypes []string    `json:"edge_types"`
	Services  []string    `json:"services"`
	Truncated bool        `json:"truncated,omitempty"`

	// Unresolved lists references in the traced files that the indexer could
	// not resolve — edges that may be missing from this answer. Always
	// present ([] when clean) so its absence is never mistaken for certainty.
	Unresolved     []graph.UnresolvedRef `json:"unresolved"`
	UnresolvedNote string                `json:"unresolved_note,omitempty"`

	// VerificationSummary aggregates edge provenance counts. Always present;
	// survives any token budget cut.
	VerificationSummary graph.VerificationSummary `json:"verification_summary"`
}

// Run traces from rootID in the given direction ("forward", "backward",
// "both") up to depth hops (<= 0 means unlimited). It returns nil if rootID
// is not in the index. verboseSources controls whether per-hop Sources
// contains compact "provider:ref" strings (false, default) or full SourceRef
// structs (true, --verbose-sources). staleAfter is the workspace-configured
// freshness threshold (0 = no stale check).
func Run(idx *graph.AdjacencyIndex, rootID, direction string, depth int, verboseSources bool, staleAfter time.Duration) *Result {
	root, ok := idx.Nodes[rootID]
	if !ok {
		return nil
	}

	r := &Result{Root: root, Direction: direction, Depth: depth, Unresolved: []graph.UnresolvedRef{}}

	var allEdges []graph.Edge
	if direction == "backward" || direction == "both" {
		hops, edges := toHops(idx, graph.Ancestors(idx, rootID, depth), verboseSources)
		r.Nodes = append(r.Nodes, hops...)
		allEdges = append(allEdges, edges...)
		chains, truncated := enumerateChains(idx, rootID, "in", depth, MaxChains-len(r.Chains), verboseSources)
		r.Chains = append(r.Chains, chains...)
		r.Truncated = r.Truncated || truncated
	}
	if direction == "forward" || direction == "both" {
		hops, edges := toHops(idx, graph.Descendants(idx, rootID, depth), verboseSources)
		r.Nodes = append(r.Nodes, hops...)
		allEdges = append(allEdges, edges...)
		chains, truncated := enumerateChains(idx, rootID, "out", depth, MaxChains-len(r.Chains), verboseSources)
		r.Chains = append(r.Chains, chains...)
		r.Truncated = r.Truncated || truncated
	}

	edgeTypes := map[string]bool{}
	services := map[string]bool{root.Service: true}
	for _, h := range r.Nodes {
		if h.EdgeType != "" {
			edgeTypes[h.EdgeType] = true
		}
		services[h.Service] = true
	}
	r.EdgeTypes = sortedKeys(edgeTypes)
	r.Services = sortedKeys(services)
	r.VerificationSummary = graph.BuildVerificationSummaryAt(allEdges, staleAfter, time.Now())
	return r
}

// AttachUnresolved scopes the workspace's unresolved-reference ledger to the
// files touched by this trace and records the matches on the result. Chain
// hops are included alongside Nodes to keep the scope exact even if chain
// enumeration and traversal diverge.
func (r *Result) AttachUnresolved(refs []graph.UnresolvedRef) {
	files := make(map[string]bool, len(r.Nodes)+1)
	if r.Root != nil {
		files[r.Root.File] = true
	}
	for _, h := range r.Nodes {
		files[h.File] = true
	}
	for _, c := range r.Chains {
		for _, h := range c.Hops {
			files[h.File] = true
		}
	}
	r.Unresolved = graph.UnresolvedInFiles(refs, files)
	r.UnresolvedNote = graph.UnresolvedNote(len(r.Unresolved))
}

// toHops converts traversal results to hops with full node + edge metadata.
// Returns the hop slice and the edges traversed (for VerificationSummary).
func toHops(idx *graph.AdjacencyIndex, results []graph.TraversalResult, verboseSources bool) ([]Hop, []graph.Edge) {
	out := make([]Hop, 0, len(results))
	var edges []graph.Edge
	for _, tr := range results {
		if tr.Node == nil {
			continue
		}
		h := nodeHop(tr.Node)
		h.Depth = tr.Depth
		if tr.Via != nil {
			applyEdge(&h, tr.Via, idx, verboseSources)
			edges = append(edges, *tr.Via)
		}
		out = append(out, h)
	}
	return out, edges
}

func nodeHop(n *graph.Node) Hop {
	return Hop{
		ID:       n.ID,
		Type:     string(n.Type),
		Label:    labelOrID(n),
		Service:  n.Service,
		File:     n.File,
		Line:     n.Line,
		Language: n.Language,
		NodeMeta: n.Meta,
	}
}

func labelOrID(n *graph.Node) string {
	if n.Label != "" {
		return n.Label
	}
	return n.ID
}

// applyEdge fills the edge fields of a hop, marking service crossings.
func applyEdge(h *Hop, e *graph.Edge, idx *graph.AdjacencyIndex, verboseSources bool) {
	h.EdgeType = string(e.Type)
	h.EdgeLabel = e.Label
	h.Confidence = e.Confidence
	h.EdgeMeta = e.Meta
	h.VerificationState = e.VerificationState
	h.VerifiedGranularity = e.VerifiedGranularity
	h.Sources = marshalSources(e.Sources, verboseSources)
	from, to := idx.Nodes[e.From], idx.Nodes[e.To]
	if from != nil && to != nil && from.Service != to.Service {
		h.CrossService = true
	}
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

// enumerateChains DFS-enumerates simple paths from rootID following edges in
// the given direction ("out" or "in"). A chain ends when the frontier node
// has no further edges, all next nodes are already on the path (cycle), or
// the depth limit is hit. Backward ("in") chains are reversed before
// rendering so they read source → root. Enumeration is deterministic: edges
// are visited sorted by (type, neighbor ID). Returns truncated=true when the
// maxChains cap cut enumeration short.
func enumerateChains(idx *graph.AdjacencyIndex, rootID, direction string, maxDepth, maxChains int, verboseSources bool) ([]Chain, bool) {
	if maxChains <= 0 {
		return nil, true
	}

	var out []Chain
	truncated := false

	// path holds the node IDs on the current DFS path; vias[i] is the edge
	// that led to path[i] (nil for the root).
	var path []string
	var vias []*graph.Edge
	onPath := map[string]bool{}

	var walk func(nodeID string, via *graph.Edge, depth int)
	walk = func(nodeID string, via *graph.Edge, depth int) {
		if len(out) >= maxChains {
			truncated = true
			return
		}
		path = append(path, nodeID)
		vias = append(vias, via)
		onPath[nodeID] = true
		defer func() {
			path = path[:len(path)-1]
			vias = vias[:len(vias)-1]
			delete(onPath, nodeID)
		}()

		extended := false
		if maxDepth <= 0 || depth < maxDepth {
			for _, e := range sortedEdges(idx, nodeID, direction) {
				next := e.To
				if direction == "in" {
					next = e.From
				}
				if onPath[next] {
					continue
				}
				if _, ok := idx.Nodes[next]; !ok {
					continue
				}
				extended = true
				walk(next, e, depth+1)
				if len(out) >= maxChains {
					truncated = true
					return
				}
			}
		}
		if !extended && len(path) > 1 {
			out = append(out, buildChain(idx, path, vias, direction, verboseSources))
		}
	}

	walk(rootID, nil, 0)
	return out, truncated
}

// buildChain snapshots the current DFS path into a Chain. For backward
// traversal the path is reversed so hops read source → root, and each hop's
// edge is the one leading INTO it in flow direction.
func buildChain(idx *graph.AdjacencyIndex, path []string, vias []*graph.Edge, direction string, verboseSources bool) Chain {
	n := len(path)
	hops := make([]Hop, n)
	for i, id := range path {
		pos := i
		if direction == "in" {
			pos = n - 1 - i
		}
		hops[pos] = nodeHop(idx.Nodes[id])
	}
	if direction == "in" {
		// vias[i] connects path[i-1] (closer to root) with path[i]. In flow
		// order path[i] precedes path[i-1], so the edge belongs to the hop at
		// position n-1-(i-1) = n-i.
		for i := 1; i < n; i++ {
			if vias[i] != nil {
				applyEdge(&hops[n-i], vias[i], idx, verboseSources)
			}
		}
	} else {
		for i := 1; i < n; i++ {
			if vias[i] != nil {
				applyEdge(&hops[i], vias[i], idx, verboseSources)
			}
		}
	}
	// Mark cross-service transitions relative to the previous hop in flow
	// order (edge-based detection already covers most, but hint chains can
	// hop through synthetic nodes).
	for i := 1; i < n; i++ {
		if hops[i].Service != hops[i-1].Service {
			hops[i].CrossService = true
		}
	}
	c := Chain{Hops: hops}
	c.Text = renderChain(hops)
	return c
}

// renderChain prints a chain as a single line:
//
//	(svc-a) Publish -[publishes]-> user.events -[subscribes]-> ‖svc-b‖ Consume
//
// Each hop is labeled with its edge type; a ‖service‖ mark appears whenever
// the flow crosses a service boundary. Edges with partial/unknown confidence
// carry a trailing "?" on the edge type.
func renderChain(hops []Hop) string {
	var b strings.Builder
	for i, h := range hops {
		if i == 0 {
			fmt.Fprintf(&b, "(%s) %s", h.Service, h.Label)
			continue
		}
		marker := ""
		if h.Confidence == graph.ConfidencePartial || h.Confidence == graph.ConfidenceUnknown {
			marker = "?"
		}
		edgeType := h.EdgeType
		if edgeType == "" {
			edgeType = "?"
		}
		fmt.Fprintf(&b, " -[%s%s]-> ", edgeType, marker)
		if h.CrossService {
			fmt.Fprintf(&b, "‖%s‖ ", h.Service)
		}
		b.WriteString(h.Label)
	}
	return b.String()
}

// sortedEdges returns the node's edges in the given direction ordered by
// (type, neighbor ID) for deterministic chain enumeration.
func sortedEdges(idx *graph.AdjacencyIndex, nodeID, direction string) []*graph.Edge {
	var edges []*graph.Edge
	if direction == "in" {
		edges = idx.InEdges[nodeID]
	} else {
		edges = idx.OutEdges[nodeID]
	}
	sorted := make([]*graph.Edge, len(edges))
	copy(sorted, edges)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Type != sorted[j].Type {
			return sorted[i].Type < sorted[j].Type
		}
		ni, nj := sorted[i].To, sorted[j].To
		if direction == "in" {
			ni, nj = sorted[i].From, sorted[j].From
		}
		return ni < nj
	})
	return sorted
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
