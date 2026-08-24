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

	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// MaxChains caps chain enumeration: path counts are combinatorial in dense
// graphs, and past this point more chains stop being informative.
const MaxChains = 100

// ExploreChains caps how many leaf chains enumerateChains walks before
// giving up, independent of MaxChains (the *display* cap). Noise-class
// filtering means a chain can be explored and classified without ever
// occupying a display slot, so the explore budget must exceed the display
// cap or a root buried in noisy fan-out would show 0 chains instead of the
// real signal a few hops further in.
const ExploreChains = MaxChains * 5

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

	// HiddenByClass tallies chains excluded by noise-class filtering (Tier
	// NV), keyed by graph.NoiseClass. Empty when no include-set was applied
	// or nothing was filtered.
	HiddenByClass map[graph.NoiseClass]int `json:"hidden_by_class,omitempty"`

	// Unresolved lists references in the traced files that the indexer could
	// not resolve — edges that may be missing from this answer. Always
	// present ([] when clean) so its absence is never mistaken for certainty.
	Unresolved     []graph.UnresolvedRef `json:"unresolved"`
	UnresolvedNote string                `json:"unresolved_note,omitempty"`

	// VerificationSummary aggregates edge provenance counts. Always present;
	// survives any token budget cut.
	VerificationSummary graph.VerificationSummary `json:"verification_summary"`

	// Trust reports the workspace's last measured eval recall (plan-14 T.0).
	// Always present; Measured=false or Stale=true means this answer is
	// unaudited. Callers set this after Run (Run has no DB access).
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

	// ResolutionNote is set when Root came from a full-text-search guess
	// rather than a confirmed exact-label match — see graph.ResolutionNote.
	// Empty (omitted) on an ordinary exact resolution.
	ResolutionNote string `json:"resolution_note,omitempty"`

	// Budget records the token-budgeting decision when ApplyBudget trimmed
	// Nodes/Chains to fit. Omitted when no budget was applied or the result
	// already fit.
	Budget *budget.Info `json:"budget,omitempty"`
}

// Run traces from rootID in the given direction ("forward", "backward",
// "both") up to depth hops (<= 0 means unlimited). It returns nil if rootID
// is not in the index. verboseSources controls whether per-hop Sources
// contains compact "provider:ref" strings (false, default) or full SourceRef
// structs (true, --verbose-sources). staleAfter is the workspace-configured
// freshness threshold (0 = no stale check). include controls which
// noise-classified chains (Tier NV) are visible; a nil/empty include hides
// every noise class, matching graph.DefaultNoiseInclude's zero value.
// exploreChains overrides ExploreChains's explore budget when > 0 (a
// heavily filter-chain-gated root can exhaust the default 500-chain budget
// on noise alone before ever reaching a behavioral chain further down the
// same subtree — this is the caller's escape hatch, not a hidden constant).
func Run(idx *graph.AdjacencyIndex, rootID, direction string, depth int, verboseSources bool, staleAfter time.Duration, include graph.NoiseInclude, exploreChains int) *Result {
	root, ok := idx.Nodes[rootID]
	if !ok {
		return nil
	}
	if exploreChains <= 0 {
		exploreChains = ExploreChains
	}

	r := &Result{Root: root, Direction: direction, Depth: depth, Unresolved: []graph.UnresolvedRef{}, HiddenByClass: map[graph.NoiseClass]int{}}

	var allEdges []graph.Edge
	if direction == "backward" || direction == "both" {
		hops, edges := toHops(idx, graph.Ancestors(idx, rootID, depth), verboseSources)
		r.Nodes = append(r.Nodes, hops...)
		allEdges = append(allEdges, edges...)
		chains, hidden, truncated := enumerateChains(idx, rootID, "in", depth, MaxChains-len(r.Chains), exploreChains, include, verboseSources)
		r.Chains = append(r.Chains, chains...)
		mergeHidden(r.HiddenByClass, hidden)
		r.Truncated = r.Truncated || truncated
	}
	if direction == "forward" || direction == "both" {
		hops, edges := toHops(idx, graph.Descendants(idx, rootID, depth), verboseSources)
		r.Nodes = append(r.Nodes, hops...)
		allEdges = append(allEdges, edges...)
		chains, hidden, truncated := enumerateChains(idx, rootID, "out", depth, MaxChains-len(r.Chains), exploreChains, include, verboseSources)
		r.Chains = append(r.Chains, chains...)
		mergeHidden(r.HiddenByClass, hidden)
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

// ChainsOnly runs the same chain enumeration Run does but skips computing
// Nodes/EdgeTypes/Services/VerificationSummary — Run's flat traversal-listing
// half, built from toHops(Ancestors/Descendants(...)). BuildFlowChains (the
// search/embedding corpus builder, internal/semantic/corpus.go) is called
// once per entrypoint node on every index and only ever reads .Chains from
// Run's result; toHops alone measured ~19% of total allocations on a real
// fleet index because of this — a flat listing built and immediately
// discarded, once per entrypoint. Returns nil if rootID is not in the index,
// mirroring Run. verboseSources is fixed false: chain hops built here never
// carry Sources (matches BuildFlowChains's only call site).
func ChainsOnly(idx *graph.AdjacencyIndex, rootID, direction string, depth int, include graph.NoiseInclude, exploreChains int) []Chain {
	if _, ok := idx.Nodes[rootID]; !ok {
		return nil
	}
	if exploreChains <= 0 {
		exploreChains = ExploreChains
	}

	var chains []Chain
	if direction == "backward" || direction == "both" {
		c, _, _ := enumerateChains(idx, rootID, "in", depth, MaxChains-len(chains), exploreChains, include, false)
		chains = append(chains, c...)
	}
	if direction == "forward" || direction == "both" {
		c, _, _ := enumerateChains(idx, rootID, "out", depth, MaxChains-len(chains), exploreChains, include, false)
		chains = append(chains, c...)
	}
	return chains
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

// FinalizeEpistemic computes the epistemic verdict (EE.0) from this result's
// already-populated Unresolved, VerificationSummary and Trust sections, plus
// the confidence of the traversed edges. Call after Trust is set and
// AttachUnresolved has run — the order every call site already uses — and
// before ApplyBudget/Compact, since epistemic must survive any token-budget
// cut, the same as verification_summary/trust.
func (r *Result) FinalizeEpistemic() *Result {
	confidences := make([]string, 0, len(r.Nodes))
	for _, h := range r.Nodes {
		confidences = append(confidences, h.Confidence)
	}
	r.Epistemic = graph.BuildEpistemic(r.Unresolved, graph.HasWeakConfidence(confidences), r.VerificationSummary, r.Trust)
	return r
}

// CompactResult is Result's default wire shape: chains rendered as arrow-chain
// text (file:line label -[edge_type]-> file:line label -> ...) and nodes as
// one-line summaries, instead of a Hop object per node repeated once in Nodes
// and again inside every Chain that touches it. Every other field survives —
// VerificationSummary, Trust, TargetCandidates and Budget are cheap and carry
// trust signals a plain string can't, so dropping them to save bytes would
// cost more than it saves.
//
// Added after a live bench trial's raw trace response for a single depth-12
// call measured 16-20KB, dominated by three redundant sources: absolute paths
// repeated on every hop, a struct/interface node's full field-by-type-tag
// list embedded in node_meta even when the trace question never asked about
// the type's shape, and Chains re-embedding every hop's full Hop object per
// path instead of referencing the already-sent Nodes list.
type CompactResult struct {
	Root      string   `json:"root"`
	Direction string   `json:"direction"`
	Depth     int      `json:"depth"`
	Nodes     []string `json:"nodes"`
	Chains    []string `json:"chains"`
	EdgeTypes []string `json:"edge_types"`
	Services  []string `json:"services"`
	Truncated bool     `json:"truncated,omitempty"`

	HiddenByClass map[graph.NoiseClass]int `json:"hidden_by_class,omitempty"`

	Unresolved     []graph.UnresolvedRef `json:"unresolved"`
	UnresolvedNote string                `json:"unresolved_note,omitempty"`

	VerificationSummary graph.VerificationSummary `json:"verification_summary"`
	Trust               graph.TrustStamp          `json:"trust"`
	Epistemic           graph.Epistemic           `json:"epistemic"`
	TargetCandidates    []graph.TargetCandidate   `json:"target_candidates"`
	ResolutionNote      string                    `json:"resolution_note,omitempty"`
	Budget              *budget.Info              `json:"budget,omitempty"`
}

// Compact converts r to its token-lean wire shape. Call after ApplyBudget:
// budgeting decides which chains/nodes survive against the full (more
// expensive) representation, which is the conservative direction — a result
// that fits the budget as Hops fits it as arrow-chain text too, just smaller.
func (r *Result) Compact() *CompactResult {
	chains := make([]string, len(r.Chains))
	for i, c := range r.Chains {
		chains[i] = c.Text
	}
	nodes := make([]string, len(r.Nodes))
	for i, h := range r.Nodes {
		nodes[i] = hopLocLabel(h) + " [" + h.Type + "]"
	}
	root := ""
	if r.Root != nil {
		root = fmt.Sprintf("(%s) %s [%s]", r.Root.Service, hopLocLabel(nodeHop(r.Root)), r.Root.Type)
	}
	return &CompactResult{
		Root:                root,
		Direction:           r.Direction,
		Depth:               r.Depth,
		Nodes:               nodes,
		Chains:              chains,
		EdgeTypes:           r.EdgeTypes,
		Services:            r.Services,
		Truncated:           r.Truncated,
		HiddenByClass:       r.HiddenByClass,
		Unresolved:          r.Unresolved,
		UnresolvedNote:      r.UnresolvedNote,
		VerificationSummary: r.VerificationSummary,
		Trust:               r.Trust,
		Epistemic:           r.Epistemic,
		TargetCandidates:    r.TargetCandidates,
		ResolutionNote:      r.ResolutionNote,
		Budget:              r.Budget,
	}
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
// are visited sorted by (type, neighbor ID).
//
// Noise classification is monotonic — once a chain crosses one non-included
// hop, no amount of further descent can make it visible again — so a noisy
// edge is pruned the moment it's reached, before spending any explore budget
// walking its subtree. This keeps a root gated by heavy filter_chain/mixin
// fan-out (e.g. a Rails before_action chain whose target itself has
// thousands of descendants) from exhausting maxExplore on one noisy branch
// before ever reaching a sibling edge that leads to real signal. Each pruned
// edge counts once against hidden, whatever its subtree's real size — an
// undercount below the prune point is an acceptable trade for not paying to
// enumerate a subtree that can never produce a visible chain.
//
// Returns truncated=true when maxExplore or maxDisplay cut enumeration
// short.
func enumerateChains(idx *graph.AdjacencyIndex, rootID, direction string, maxDepth, maxDisplay, maxExplore int, include graph.NoiseInclude, verboseSources bool) ([]Chain, map[graph.NoiseClass]int, bool) {
	hidden := map[graph.NoiseClass]int{}
	if maxDisplay <= 0 || maxExplore <= 0 {
		return nil, hidden, true
	}

	var out []Chain
	explored := 0
	truncated := false

	// path holds the node IDs on the current DFS path; vias[i] is the edge
	// that led to path[i] (nil for the root).
	var path []string
	var vias []*graph.Edge
	onPath := map[string]bool{}

	var walk func(nodeID string, via *graph.Edge, depth int)
	walk = func(nodeID string, via *graph.Edge, depth int) {
		if explored >= maxExplore {
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
				dst, ok := idx.Nodes[next]
				if !ok {
					continue
				}
				if class := graph.ClassifyEdgeNoise(e, dst); !include.Allows(class) {
					explored++
					hidden[class]++
					if explored >= maxExplore {
						truncated = true
						return
					}
					continue
				}
				extended = true
				walk(next, e, depth+1)
				if explored >= maxExplore {
					truncated = true
					return
				}
			}
		}
		if !extended && len(path) > 1 {
			explored++
			if len(out) < maxDisplay {
				out = append(out, buildChain(idx, path, vias, direction, verboseSources))
			} else {
				truncated = true
			}
		}
	}

	walk(rootID, nil, 0)
	return out, hidden, truncated
}

// mergeHidden adds src's counts into dst in place.
func mergeHidden(dst, src map[graph.NoiseClass]int) {
	for k, v := range src {
		dst[k] += v
	}
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
			fmt.Fprintf(&b, "(%s) %s", h.Service, hopLocLabel(h))
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
		b.WriteString(hopLocLabel(h))
	}
	return b.String()
}

// hopLocLabel renders "file:line label" for a hop — the filename disambiguates
// same-named symbols across files/services (a real collision hit twice in
// live bench trials: RemoveConfig exists as both a maple-agent handler and a
// maple-manager client method with the same label).
func hopLocLabel(h Hop) string {
	if h.File == "" {
		return h.Label
	}
	if h.Line > 0 {
		return fmt.Sprintf("%s:%d %s", h.File, h.Line, h.Label)
	}
	return fmt.Sprintf("%s %s", h.File, h.Label)
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

// ApplyBudget trims Chains then Nodes to fit maxTokens (<=0 = unlimited,
// left untouched). Chains go first: MaxChains already caps chain count, but
// each hop carries full node+edge metadata and is duplicated across every
// chain it appears in, so Chains is the more expensive of the two lists per
// item and also the more skippable — Nodes plus EdgeTypes/Services already
// answers "what's involved" once Chains is gone, just without path
// ordering. Both lists always keep at least 1 entry so the answer never
// goes empty.
//
// Added after a live bench trial measured a depth-10 "both"-direction trace
// on a busy AMQP channel hub producing 346K–471K characters — over Claude
// Code's own tool-output limit, which hard-rejected the call outright (no
// truncated preview, unlike a merely oversized-but-under-limit result) and
// forced the agent to abandon polyflow for ~20 manual grep/Read calls to
// reconstruct by hand what one trace call was supposed to answer.
func (r *Result) ApplyBudget(maxTokens int) *Result {
	if maxTokens <= 0 {
		return r
	}
	if budget.Estimate(r) <= maxTokens {
		r.Budget = &budget.Info{MaxTokens: maxTokens, EstimatedTokens: budget.Estimate(r), Level: budget.LevelDetail}
		return r
	}

	allChains := r.Chains
	n := budget.TrimToFit(len(allChains), maxTokens, func(n int) int {
		r.Chains = allChains[:n]
		return budget.Estimate(r)
	})
	r.Chains = allChains[:n]
	if n < len(allChains) {
		r.Truncated = true
	}
	if budget.Estimate(r) <= maxTokens {
		r.Budget = &budget.Info{
			MaxTokens: maxTokens, EstimatedTokens: budget.Estimate(r), Level: budget.LevelDetail,
			Note: fmt.Sprintf("chains trimmed to %d of %d to fit max_tokens", n, len(allChains)),
		}
		return r
	}

	// The prefix cut runs against a shrunk budget, reserving
	// nodeBackfillReserve for the backfill pass below — same shape as
	// impact/summary.go's file backfill. Without the reservation, TrimToFit's
	// binary search picks the largest prefix that fits and leaves only
	// incidental slack, starving backfill down to whatever near-empty node
	// happens to be cheapest instead of a real deep node like an exchange
	// declaration or health-check gate (E.1 xsvc-exec-config-build-roundtrip
	// bench trial: max_tokens truncation dropped 2 real hops that would have
	// fit for free in the leftover headroom).
	allNodes := r.Nodes
	cutBudget := int(float64(maxTokens) * (1 - nodeBackfillReserve))
	m := budget.TrimToFit(len(allNodes), cutBudget, func(m int) int {
		r.Nodes = allNodes[:m]
		return budget.Estimate(r)
	})
	r.Nodes = allNodes[:m]
	used := budget.Estimate(r)

	// Backfill: BFS order means survival depends entirely on depth, so a
	// cheap one-hop node just past the cut is dropped for free alongside
	// genuinely large ones. Splice back any omitted node cheap enough to fit
	// the leftover headroom (the full maxTokens, not the shrunk cutBudget),
	// then re-sort by depth so the traversal order in the response still
	// reads shallow-to-deep.
	admitted := budget.Backfill(len(allNodes), m, maxTokens, used, func(i int) int {
		return budget.Estimate(allNodes[i])
	})
	for _, i := range admitted {
		r.Nodes = append(r.Nodes, allNodes[i])
	}
	if len(admitted) > 0 {
		sort.SliceStable(r.Nodes, func(i, j int) bool { return r.Nodes[i].Depth < r.Nodes[j].Depth })
	}
	if m < len(allNodes) {
		r.Truncated = true
	}

	note := fmt.Sprintf("chains trimmed to %d of %d, nodes trimmed to %d of %d to fit max_tokens",
		n, len(allChains), len(r.Nodes), len(allNodes))
	if len(admitted) > 0 {
		note = fmt.Sprintf("%s (%d cheap node(s) admitted out of depth order to use leftover budget)", note, len(admitted))
	}
	r.Budget = &budget.Info{
		MaxTokens: maxTokens, EstimatedTokens: budget.Estimate(r), Level: budget.LevelSummary,
		Note: note,
	}
	return r
}

// nodeBackfillReserve is the share of a token budget set aside so the
// backfill pass in ApplyBudget has room to admit a cheap, deeper node
// instead of starving on whatever incidental slack the prefix cut left
// behind. Mirrors impact/summary.go's fileBackfillReserve.
const nodeBackfillReserve = 0.10

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
