package evidence

import (
	"context"
	"fmt"
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// Reconciler merges evidence from multiple Providers into a single
// provenance-tracked graph.  With only the static provider (F.0), every edge
// is stamped as candidate.  When F.1+ providers are added, the key-join
// upgrades matching edges to verified and surfaces gaps.
//
// All operations are total-recomputation: VerificationState is derived from
// Sources[] on each run, so a removed session/spec can never leave stale state.
type Reconciler struct {
	providers []Provider
}

// NewReconciler creates a Reconciler from an ordered list of providers.
// All provider names are validated at construction; an unknown name is an
// immediate error (bug-class rule 3).
func NewReconciler(providers ...Provider) (*Reconciler, error) {
	for _, p := range providers {
		if err := ValidateProviderName(p.Name()); err != nil {
			return nil, err
		}
	}
	return &Reconciler{providers: providers}, nil
}

// ReconcileResult is the output of Reconcile.
type ReconcileResult struct {
	Nodes      []graph.Node
	Edges      []graph.Edge
	Unresolved []graph.UnresolvedRef
}

// channelKey is the join key for two edges sharing a logical channel.
// The primary key is (Type, Label) — the contract-engine's normalized
// channel key.  An empty Label falls back to (From, To) so containment
// and other structural edges are still correctly keyed.
type channelKey struct {
	edgeType graph.EdgeType
	label    string
	from     string
	to       string
}

func keyOf(e *graph.Edge) channelKey {
	if e.Label != "" {
		return channelKey{edgeType: e.Type, label: e.Label}
	}
	return channelKey{edgeType: e.Type, from: e.From, to: e.To}
}

// Reconcile collects evidence from all providers and fuses it into a single
// edge set with provenance and verification state.
//
// Multi-valued join (bug-class rule 1): every static edge sharing a channel
// key receives a source stamp from a confirming provider — never only the
// first found.
//
// Deterministic output (bug-class rule 2): minted synthetic nodes/edges use
// stable IDs; Sources[] and the edge slice are sorted by stable keys.
func (r *Reconciler) Reconcile(ctx context.Context, ws *workspace.WorkspaceConfig) (ReconcileResult, error) {
	// Collect from all providers in order.
	type collected struct {
		name string
		ev   Evidence
	}
	all := make([]collected, 0, len(r.providers))
	for _, p := range r.providers {
		ev, err := p.Collect(ctx, ws)
		if err != nil {
			return ReconcileResult{}, fmt.Errorf("evidence provider %q: %w", p.Name(), err)
		}
		all = append(all, collected{p.Name(), ev})
	}

	// Separate static evidence from non-static.  Static edges carry the base
	// edge set (completeness skeleton).  Non-static providers confirm or gap.
	var staticEv Evidence
	var nonStaticEv []collected
	for _, c := range all {
		if c.name == "static" {
			staticEv = c.ev
		} else {
			nonStaticEv = append(nonStaticEv, c)
		}
	}

	// Build the working edge map (multi-valued, by channel key) from the
	// static evidence.  Edges are stored as pointers so mutations below are
	// visible through the map without re-insertion.
	type edgeSlot struct {
		edge *graph.Edge
	}
	// edgeByKey: channel → all static edges on that channel (in input order).
	// map[channelKey][]*graph.Edge — multi-valued, never first-seen-wins.
	edgeByKey := make(map[channelKey][]*graph.Edge, len(staticEv.Edges))
	// edgeByID: for dedup and direct lookup (merge, not duplicate).
	edgeByID := make(map[string]*graph.Edge, len(staticEv.Edges))

	workingEdges := make([]graph.Edge, len(staticEv.Edges))
	copy(workingEdges, staticEv.Edges)
	for i := range workingEdges {
		ep := &workingEdges[i]
		edgeByID[ep.ID] = ep
		k := keyOf(ep)
		edgeByKey[k] = append(edgeByKey[k], ep)
	}

	// Collect all nodes from all providers.
	var allNodes []graph.Node
	nodeByID := make(map[string]bool, len(staticEv.Nodes))
	// nodeService: static node ID → owning service, for service-scoped joins.
	nodeService := make(map[string]string, len(staticEv.Nodes))
	for _, n := range staticEv.Nodes {
		allNodes = append(allNodes, n)
		nodeByID[n.ID] = true
		nodeService[n.ID] = n.Service
	}

	// Build the set of static unresolved refs that non-static providers claim to
	// have handled (matched by Service+File+Line+Name). These are removed from
	// the static unresolved set and replaced by the provider's own entries.
	type clearKey struct{ service, file, name string; line int }
	clearSet := make(map[clearKey]bool)

	// Collect all unresolved from all providers.
	var allUnresolved []graph.UnresolvedRef

	// Process non-static providers: join their edges onto the static set.
	// Gap edges are allocated individually and tracked by ID so a duplicate
	// confirmation merges Sources instead of being dropped, and so pointers
	// stay valid while workingEdges may still reallocate.
	gapByID := make(map[string]*graph.Edge)
	var gapOrder []string
	for _, c := range nonStaticEv {
		for i := range c.ev.Edges {
			pe := &c.ev.Edges[i]
			k := keyOf(pe)
			// Channel confirmed: append source to every static edge on this
			// channel (multi-valued — bug-class rule 1), scoped to the
			// declaring service when both sides carry service identity.
			matched := false
			for _, sp := range edgeByKey[k] {
				if !serviceCompatible(pe.From, sp, nodeService) {
					continue
				}
				sp.Sources = appendSource(sp.Sources, pe.Sources...)
				matched = true
			}
			if !matched {
				// No static edge on this channel (for this service):
				// observed_only_gap — mint synthetic nodes/edges with
				// deterministic IDs derived from (kind, key, from, to).
				from, to := pe.From, pe.To
				if from == "" {
					from = endpointNodeID(pe)
				}
				if to == "" {
					to = endpointNodeID(pe)
				}
				synthID := syntheticEdgeID(pe, from, to)
				if existing := gapByID[synthID]; existing != nil {
					// Same channel confirmed again: merge, never drop.
					existing.Sources = appendSource(existing.Sources, pe.Sources...)
					continue
				}
				gap := &graph.Edge{
					ID:                synthID,
					From:              from,
					To:                to,
					Type:              pe.Type,
					Label:             pe.Label,
					Confidence:        graph.ConfidenceCandidate,
					Sources:           append([]graph.SourceRef(nil), pe.Sources...),
					VerificationState: graph.StateObservedOnlyGap,
				}
				gapByID[synthID] = gap
				gapOrder = append(gapOrder, synthID)
				// Mint endpoint nodes if they don't already exist, tagged with
				// the provider that surfaced them.
				for _, nodeID := range []string{from, to} {
					if !nodeByID[nodeID] {
						label := nodeID
						if pe.Label != "" && nodeID == endpointNodeID(pe) {
							label = pe.Label
						}
						allNodes = append(allNodes, graph.Node{
							ID:    nodeID,
							Type:  graph.NodeTypeService,
							Label: label,
							Meta:  map[string]string{"source": c.name},
						})
						nodeByID[nodeID] = true
					}
				}
			}
		}
		// Absorb nodes and unresolved from this provider.
		for _, n := range c.ev.Nodes {
			if !nodeByID[n.ID] {
				allNodes = append(allNodes, n)
				nodeByID[n.ID] = true
			}
		}
		allUnresolved = append(allUnresolved, c.ev.Unresolved...)
		for _, u := range c.ev.ClearsUnresolved {
			clearSet[clearKey{u.Service, u.File, u.Name, u.Line}] = true
		}
	}
	for _, id := range gapOrder {
		workingEdges = append(workingEdges, *gapByID[id])
	}

	// Add static unresolved entries, skipping any that a non-static provider
	// has claimed via ClearsUnresolved (those are replaced by the provider's
	// own entries, e.g. config_not_found instead of dynamic_url).
	for _, u := range staticEv.Unresolved {
		if !clearSet[clearKey{u.Service, u.File, u.Name, u.Line}] {
			allUnresolved = append(allUnresolved, u)
		}
	}

	// Recompute VerificationState for every edge from Sources[] (total).
	for i := range workingEdges {
		ep := &workingEdges[i]
		// Sort Sources deterministically before persisting (bug-class rule 2).
		sortSources(ep.Sources)
		ep.VerificationState = computeState(ep.Sources)
		// Granularity: channel by default; upgraded to site only when a runtime
		// source carries code.filepath (code-level attribution from span attrs).
		// Two static call sites on one channel + one span without code.filepath
		// → both stay channel — the span confirms the channel, not the site.
		if ep.VerificationState == graph.StateVerified {
			ep.VerifiedGranularity = computeGranularity(ep.Sources)
		} else {
			ep.VerifiedGranularity = ""
		}
	}

	// Sort the edge slice by ID for deterministic output (bug-class rule 2).
	// SliceStable preserves the original relative order of same-ID duplicates
	// (e.g. multiple addEventListener calls at the same line in minified JS),
	// so the SQLite last-write-wins outcome is deterministic across runs.
	sort.SliceStable(workingEdges, func(i, j int) bool {
		return workingEdges[i].ID < workingEdges[j].ID
	})

	return ReconcileResult{
		Nodes:      allNodes,
		Edges:      workingEdges,
		Unresolved: allUnresolved,
	}, nil
}

// appendSource appends src refs, deduped by (provider, ref).
func appendSource(existing []graph.SourceRef, src ...graph.SourceRef) []graph.SourceRef {
	type key struct{ p, r string }
	seen := make(map[key]bool, len(existing))
	for _, s := range existing {
		seen[key{s.Provider, s.Ref}] = true
	}
	for _, s := range src {
		k := key{s.Provider, s.Ref}
		if !seen[k] {
			existing = append(existing, s)
			seen[k] = true
		}
	}
	return existing
}

// sortSources sorts in-place by (Provider, Ref, ObservedAt) for determinism.
func sortSources(s []graph.SourceRef) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].Provider != s[j].Provider {
			return s[i].Provider < s[j].Provider
		}
		if s[i].Ref != s[j].Ref {
			return s[i].Ref < s[j].Ref
		}
		return s[i].ObservedAt < s[j].ObservedAt
	})
}

// computeState derives the VerificationState from the Sources slice.
//
// verified:          static ∩ (runtime ∨ contract)
// candidate:         static-only (no confirmation)
// observed_only_gap: runtime or contract evidence with no matching static edge
// conflicting:       sources disagree (reserved for F.4)
func computeState(sources []graph.SourceRef) string {
	hasStatic := false
	hasConfirm := false
	for _, s := range sources {
		switch s.Provider {
		case "static":
			hasStatic = true
		case "runtime", "contract", "config":
			hasConfirm = true
		}
	}
	switch {
	case hasStatic && hasConfirm:
		return graph.StateVerified
	case hasStatic:
		return graph.StateCandidate
	case hasConfirm:
		// Non-static confirming evidence: gap — static missed this edge.
		return graph.StateObservedOnlyGap
	default:
		return graph.StateCandidate
	}
}

// computeGranularity returns GranularitySite if any runtime source carries a
// CodeFile (code-level attribution from span attributes), otherwise
// GranularityChannel.  Channel is always the safe default: a span confirms the
// channel exists, not which call site triggered it.
func computeGranularity(sources []graph.SourceRef) string {
	for _, s := range sources {
		if s.Provider == "runtime" && s.CodeFile != "" {
			return graph.GranularitySite
		}
	}
	return graph.GranularityChannel
}

// serviceCompatible reports whether a non-static edge declared by declService
// may confirm the static edge sp. A spec found in service A must not verify a
// same-shaped edge between unrelated services B→C — but contract evidence only
// knows one side of the channel, and direction differs per format (an OpenAPI
// spec names the serving side, an AsyncAPI publish names the publishing side),
// so either endpoint matching the declaring service is a confirmation. When
// service identity is unavailable on both sides, fall back to the unscoped
// join (recall over precision).
func serviceCompatible(declService string, sp *graph.Edge, nodeService map[string]string) bool {
	if declService == "" {
		return true
	}
	fromSvc, toSvc := nodeService[sp.From], nodeService[sp.To]
	if fromSvc == "" && toSvc == "" {
		return true
	}
	return fromSvc == declService || toSvc == declService
}

// endpointNodeID derives a deterministic node ID for the anonymous side of a
// gap edge (contract evidence often knows only the declaring service). The
// channel key is included so distinct channels never share an endpoint node.
func endpointNodeID(e *graph.Edge) string {
	return fmt.Sprintf("gap-endpoint:%s:%s", string(e.Type), e.Label)
}

// syntheticEdgeID derives a deterministic ID for a gap edge from
// (kind, key, from, to). Never a counter or map-order-dependent value
// (bug-class rule 2); the channel key (Label) is part of the identity so two
// declared operations from one service never collapse into one gap edge.
func syntheticEdgeID(e *graph.Edge, from, to string) string {
	return fmt.Sprintf("gap:%s:%s:%s->%s", string(e.Type), e.Label, from, to)
}
