// Package yield computes the resolution-yield scorecard (X.3): a single set
// of numbers answering "does polyflow resolve this repo's flows", scoped per
// edge Class × Scope so X.0–X.2 (and future phases) can be graded and
// regression-gated instead of asserted.
package yield

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// Scope distinguishes same-service edges from cross-service edges.
type Scope string

const (
	ScopeInternal Scope = "internal"
	ScopeCross    Scope = "cross"
)

// ReasonUndecidableDispatch marks ledger sites the indexer correctly declined
// to resolve because doing so would require runtime type information (Ruby
// `send`, Go interface method values). Per the X.3 spec these are excluded
// from the Resolvable denominator entirely — they are neither Resolved nor
// Unresolved for scorecard purposes, just absent.
const ReasonUndecidableDispatch = "undecidable_dispatch"

// reasonUnmatchedEdge is the reason code attached to unresolved sites that
// surface as a visible edge to a synthetic "unresolved:<svc>" node (contract
// rules with unmatched: unknown_edge) rather than a ledger entry. Attaching
// it keeps "Unresolved == sum(Reasons)" true for every row, so an agent (or
// the CI gate) reading Reasons always sees where every unresolved count came
// from — no unresolved site is ever left unlabeled.
const reasonUnmatchedEdge = "unmatched_edge"

// ledgerKindClass maps unresolved-ledger reason kinds (graph.UnresolvedRef.Kind)
// to the edge Class + Scope they attribute to. Only kinds that are inherently
// scoped to one resolution-flow class are listed: the contract-rule domain
// names used with `unmatched: ledger` (jobs.yaml/kafka.yaml/nats.yaml/
// pusher.yaml/redis_pubsub.yaml/websocket.yaml/graphql.yaml), the linker's
// generic in-file call reference, the route-convention/dynamic-template
// ledger kinds (X.1), and X.2's async-job kinds share "job".
//
// Kinds absent from this table (inherits_unresolved, implements_unresolved,
// import_ref, global_collision, factory_dynamic, alias_reassigned,
// selector_dynamic, dom_ref, rails_helper_unresolved, …) are type-relation
// or DOM-wiring ledger entries: they have their own producers and are not
// silently dropped by being ledgered there — they are simply outside the
// resolution-flow population this scorecard measures.
var ledgerKindClass = map[string]struct {
	Class graph.EdgeType
	Scope Scope
}{
	"job":                         {graph.EdgeTypeJobEnqueue, ScopeCross},
	"kafka":                       {graph.EdgeTypeKafkaPublish, ScopeCross},
	"nats":                        {graph.EdgeTypeNATSPublish, ScopeCross},
	"pusher":                      {graph.EdgeTypePusherTrigger, ScopeCross},
	"redis_pubsub":                {graph.EdgeTypeRedisPublish, ScopeCross},
	"websocket":                   {graph.EdgeTypeWSSend, ScopeCross},
	"graphql":                     {graph.EdgeTypeGraphQLCall, ScopeCross},
	"call_ref":                    {graph.EdgeTypeCalls, ScopeInternal},
	"route_convention_unresolved": {graph.EdgeTypeHTTPCall, ScopeCross},
	"dynamic_url":                 {graph.EdgeTypeHTTPCall, ScopeCross},
	"dynamic_topic":               {graph.EdgeTypePublishes, ScopeCross},
	"dynamic_queue":               {graph.EdgeTypeJobEnqueue, ScopeCross},
	"dynamic_channel":             {graph.EdgeTypePublishes, ScopeCross},
	"dynamic_event":               {graph.EdgeTypePublishes, ScopeCross},
}

// nonResolutionEdgeTypes lists structural/bookkeeping edge classes that sit
// outside the cross-service/internal resolution population this scorecard
// measures: the containment backbone, variable tracking, type-relation, and
// DOM-wiring edge families. Every other graph.EdgeType is counted.
var nonResolutionEdgeTypes = map[graph.EdgeType]bool{
	graph.EdgeTypeContains:      true,
	graph.EdgeTypeDeclares:      true,
	graph.EdgeTypeReads:         true,
	graph.EdgeTypeWrites:        true,
	graph.EdgeTypeCaptures:      true,
	graph.EdgeTypeFlowsTo:       true,
	graph.EdgeTypeUsesType:      true,
	graph.EdgeTypeInherits:      true,
	graph.EdgeTypeImplements:    true,
	graph.EdgeTypeInstantiates:  true,
	graph.EdgeTypeImports:       true,
	graph.EdgeTypeDefinedIn:     true,
	graph.EdgeTypeComponentImpl: true,
	graph.EdgeTypeDatastarBind:  true,
	graph.EdgeTypeNavigatesTo:   true,
	graph.EdgeTypeDOMRead:       true,
	graph.EdgeTypeDOMWrite:      true,
	graph.EdgeTypeDOMCreate:     true,
	graph.EdgeTypeDOMRemove:     true,
	graph.EdgeTypeDOMListen:     true,
}

// Row is the resolution-yield scorecard for one edge Class within one Scope.
type Row struct {
	Class           graph.EdgeType
	Scope           Scope
	Resolvable      int // excludes is_test [X.0] and undecidable_dispatch dynamic dispatch
	ResolvedStatic  int
	ResolvedRuntime int            // verification_state ∈ {verified, observed_only_gap} [X.6]
	External        int            // resolved to a typed external_service node
	Unresolved      int            // ledgered, with reasons
	Reasons         map[string]int // reason-code → count (from UnresolvedRef.Kind)
}

// Report is the full scorecard: one Row per (Class, Scope) that had any
// activity, plus the three headline ratios and the CI-gate verdict.
type Report struct {
	Rows                  []Row   // deterministic: sorted by (Class, Scope)
	InternalYield         float64 // must be 1.0
	CrossYieldStatic      float64 // must be >= 0.95
	CrossYieldWithRuntime float64 // must be 1.0 or fully reason-ledgered
	Pass                  bool
	Failures              []string
}

type rowKey struct {
	class graph.EdgeType
	scope Scope
}

// Compute builds the resolution-yield scorecard from the store's current
// index: adjacency for edge-based resolved/unresolved/external counts, plus
// the unresolved ledger for reason codes. Deterministic — Rows are sorted by
// (Class, Scope); no map iteration order reaches Rows, Failures, or the
// float ratios (bug-class #2).
func Compute(ctx context.Context, s *graph.SQLiteStore) (Report, error) {
	idx, err := s.BuildIndex(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("build index: %w", err)
	}
	refs, err := s.ListUnresolvedRefs(ctx)
	if err != nil {
		return Report{}, fmt.Errorf("list unresolved refs: %w", err)
	}

	rows := make(map[rowKey]*Row)
	var order []rowKey
	get := func(k rowKey) *Row {
		r, ok := rows[k]
		if !ok {
			r = &Row{Class: k.class, Scope: k.scope, Reasons: make(map[string]int)}
			rows[k] = r
			order = append(order, k)
		}
		return r
	}

	for _, e := range idx.AllEdges() {
		if nonResolutionEdgeTypes[e.Type] {
			continue
		}
		fromNode, toNode := idx.Nodes[e.From], idx.Nodes[e.To]
		if fromNode == nil || toNode == nil {
			// A dangling edge is a graph-integrity bug caught elsewhere
			// (bug-class #10); the scorecard just skips what it can't attribute.
			continue
		}
		if fromNode.Meta[graph.MetaIsTest] == "true" {
			continue // X.0: test-DSL producers excluded from the denominator
		}

		scope := ScopeCross
		if fromNode.Service != "" && fromNode.Service == toNode.Service {
			scope = ScopeInternal
		}
		row := get(rowKey{e.Type, scope})

		switch {
		case toNode.Type == graph.NodeTypeExternalService:
			row.External++
		case isUnresolvedNode(toNode):
			row.Unresolved++
			row.Reasons[reasonUnmatchedEdge]++
		case e.VerificationState == graph.StateVerified || e.VerificationState == graph.StateObservedOnlyGap:
			row.ResolvedRuntime++
		default:
			row.ResolvedStatic++
		}
	}

	for _, ref := range refs {
		if ref.Kind == ReasonUndecidableDispatch {
			continue // excluded from the denominator entirely (X.3 spec)
		}
		mapping, ok := ledgerKindClass[ref.Kind]
		if !ok {
			continue // outside this report's population; see ledgerKindClass doc
		}
		row := get(rowKey{mapping.Class, mapping.Scope})
		row.Unresolved++
		row.Reasons[ref.Kind]++
	}

	for _, r := range rows {
		r.Resolvable = r.ResolvedStatic + r.ResolvedRuntime + r.External + r.Unresolved
	}

	sort.Slice(order, func(i, j int) bool {
		if order[i].class != order[j].class {
			return order[i].class < order[j].class
		}
		return order[i].scope < order[j].scope
	})

	report := Report{Rows: make([]Row, 0, len(order))}
	var (
		internalResolvable, internalResolved                   int
		crossResolvable, crossResolvedStatic, crossResolvedAll int
		crossUnresolvedLackingReason                           int
	)
	for _, k := range order {
		r := *rows[k]
		report.Rows = append(report.Rows, r)
		switch r.Scope {
		case ScopeInternal:
			internalResolvable += r.Resolvable
			internalResolved += r.ResolvedStatic + r.ResolvedRuntime + r.External
		case ScopeCross:
			crossResolvable += r.Resolvable
			crossResolvedStatic += r.ResolvedStatic + r.External
			crossResolvedAll += r.ResolvedStatic + r.ResolvedRuntime + r.External
			reasoned := 0
			for _, n := range r.Reasons {
				reasoned += n
			}
			if reasoned < r.Unresolved {
				crossUnresolvedLackingReason += r.Unresolved - reasoned
			}
		}
	}

	report.InternalYield = ratio(internalResolved, internalResolvable)
	report.CrossYieldStatic = ratio(crossResolvedStatic, crossResolvable)
	report.CrossYieldWithRuntime = ratio(crossResolvedAll, crossResolvable)

	report.Pass = true
	const eps = 1e-9
	if report.InternalYield < 1.0-eps {
		report.Pass = false
		report.Failures = append(report.Failures, fmt.Sprintf("internal_yield %.4f < 1.0", report.InternalYield))
	}
	if report.CrossYieldStatic < 0.95-eps {
		report.Pass = false
		report.Failures = append(report.Failures, fmt.Sprintf("cross_yield_static %.4f < 0.95", report.CrossYieldStatic))
	}
	if crossUnresolvedLackingReason > 0 {
		report.Pass = false
		report.Failures = append(report.Failures, fmt.Sprintf("%d unresolved cross-service site(s) lack a reason code", crossUnresolvedLackingReason))
	}

	return report, nil
}

// IsFlowEdge reports whether an edge type participates in the resolution-flow
// population this package measures (the complement of nonResolutionEdgeTypes).
// Shared with internal/mcpserver's `flows` traversal (X.4) so a flow answer
// and the yield scorecard agree on what counts as a flow hop rather than
// re-deriving the classification.
func IsFlowEdge(t graph.EdgeType) bool {
	return !nonResolutionEdgeTypes[t]
}

// CoverageBlock is a compact, per-answer projection of the resolution-yield
// scorecard scoped to one traversed subgraph (a flows/entrypoints answer)
// rather than the whole store: counts of verified/candidate/observed_only_gap
// hops plus the unresolved endpoints (with reasons) reached by the traversal.
// This is the token-saving lever — it tells the agent exactly which residue
// warrants a grep so it stops defensively re-verifying the rest.
type CoverageBlock struct {
	Verified        int                   `json:"verified"`
	Candidate       int                   `json:"candidate"`
	ObservedOnlyGap int                   `json:"observed_only_gap"`
	Unresolved      []graph.UnresolvedRef `json:"unresolved"`
	UnresolvedNote  string                `json:"unresolved_note,omitempty"`
}

// ComputeCoverage tallies verification states across a set of traversed edges
// and pairs the tally with the unresolved-ledger entries the caller has
// already scoped to the touched files (graph.UnresolvedInFiles). It reuses
// the same verified/observed_only_gap/else-candidate classification Compute
// uses per edge, without requiring a whole-graph BuildIndex — the caller
// supplies just the edges its traversal actually walked.
func ComputeCoverage(edges []graph.Edge, unresolvedInScope []graph.UnresolvedRef) CoverageBlock {
	cb := CoverageBlock{Unresolved: unresolvedInScope}
	if cb.Unresolved == nil {
		cb.Unresolved = []graph.UnresolvedRef{}
	}
	for _, e := range edges {
		switch e.VerificationState {
		case graph.StateVerified:
			cb.Verified++
		case graph.StateObservedOnlyGap:
			cb.ObservedOnlyGap++
		default:
			cb.Candidate++
		}
	}
	cb.UnresolvedNote = graph.UnresolvedNote(len(cb.Unresolved))
	return cb
}

func isUnresolvedNode(n *graph.Node) bool {
	return n.Type == graph.NodeTypeService && (n.ID == "unresolved" || strings.HasPrefix(n.ID, "unresolved:"))
}

func ratio(num, den int) float64 {
	if den == 0 {
		return 1.0
	}
	return float64(num) / float64(den)
}
