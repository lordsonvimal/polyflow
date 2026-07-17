package trace_ingest

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/evidence"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// RuntimeProvider is the evidence.Provider for OTLP trace sessions (R.1+).
// It reads span files from capturesDir/<session>/spans.otlp.json, maps them to
// flow records in contract-engine channel-key vocabulary, and emits runtime
// edges and ingest-ledger entries through the F.0 evidence substrate.
//
// Graceful degradation: an empty or missing captures directory returns an empty
// Evidence — the indexer proceeds with static-only (candidate) edges.
type RuntimeProvider struct {
	capturesDir string
	// sessions restricts which session names are read.  Empty means all
	// sessions found under capturesDir.
	sessions []string
}

// NewRuntimeProvider creates a RuntimeProvider that reads sessions from
// capturesDir.  Pass nil (or empty) sessions to include every session present.
func NewRuntimeProvider(capturesDir string, sessions []string) *RuntimeProvider {
	return &RuntimeProvider{capturesDir: capturesDir, sessions: sessions}
}

// Name implements evidence.Provider.
func (p *RuntimeProvider) Name() string { return "runtime" }

// Collect reads all configured sessions, maps spans to flow records, and
// returns runtime-sourced edges and ingest-ledger entries.
//
// Multi-session union: flow records from all sessions are aggregated by
// (kind, key, from_service, to_service); a channel confirmed by any session
// stamps a source on every matching static edge.  Sessions are processed in
// sorted name order for determinism (bug-class rule 2).
func (p *RuntimeProvider) Collect(_ context.Context, ws *workspace.WorkspaceConfig) (evidence.Evidence, error) {
	sessionNames, err := p.resolveSessionNames()
	if err != nil || len(sessionNames) == 0 {
		return evidence.Evidence{}, nil // graceful degradation
	}

	// Process sessions in stable sorted order (bug-class rule 2).
	sort.Strings(sessionNames)

	// Aggregate flow records across sessions: same key = merge refs.
	type flowKey struct{ kind, key, from, to string }
	merged := make(map[flowKey]*FlowRecord)
	var mergeOrder []flowKey

	var allLedger []graph.UnresolvedRef

	for _, name := range sessionNames {
		spansPath := fmt.Sprintf("%s/%s/spans.otlp.json", p.capturesDir, name)
		spans, parseErr := ReadSessionSpans(spansPath)
		if parseErr != nil {
			// Malformed session file — ledger the entire session.
			allLedger = append(allLedger, graph.UnresolvedRef{
				Service: "unknown",
				File:    spansPath,
				Name:    name,
				Kind:    "otlp_malformed",
			})
			continue
		}

		flows, ledger := MapSpans(spans, name, ws)

		// Merge flow records.
		for _, flow := range flows {
			fk := flowKey{string(flow.Kind), flow.Key, flow.FromService, flow.ToService}
			if rec, exists := merged[fk]; exists {
				for _, ref := range flow.Refs {
					rec.Refs = appendRef(rec.Refs, ref)
				}
			} else {
				cp := flow
				merged[fk] = &cp
				mergeOrder = append(mergeOrder, fk)
			}
		}

		// Convert ingest ledger entries to graph.UnresolvedRef.
		for _, entry := range ledger {
			allLedger = append(allLedger, ledgerEntryToUnresolved(entry, p.capturesDir))
		}
	}

	// Build edges from merged flow records.
	edges := make([]graph.Edge, 0, len(mergeOrder))
	for _, fk := range mergeOrder {
		rec := merged[fk]
		sortRefs(rec.Refs)
		edges = append(edges, flowRecordToEdge(rec))
	}

	// Sort edges by ID for deterministic output (bug-class rule 2).
	sort.Slice(edges, func(i, j int) bool {
		return edges[i].ID < edges[j].ID
	})

	return evidence.Evidence{
		Edges:      edges,
		Unresolved: allLedger,
	}, nil
}

// resolveSessionNames returns the session names to process.  When p.sessions
// is empty, every subdirectory of p.capturesDir is a session.
func (p *RuntimeProvider) resolveSessionNames() ([]string, error) {
	if len(p.sessions) > 0 {
		return p.sessions, nil
	}
	entries, err := os.ReadDir(p.capturesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// flowRecordToEdge converts a merged FlowRecord to a graph.Edge.
// The edge ID is deterministic from (kind, key, from_service, to_service).
func flowRecordToEdge(rec *FlowRecord) graph.Edge {
	sources := make([]graph.SourceRef, 0, len(rec.Refs))
	for _, ref := range rec.Refs {
		sources = append(sources, graph.SourceRef{
			Provider:   "runtime",
			Confidence: graph.ConfidenceObserved,
			Ref:        ref.Session + "/" + ref.TraceID,
			ObservedAt: ref.ObservedAt,
			CodeFile:   ref.CodeFile,
			CodeFunc:   ref.CodeFunc,
		})
	}
	// Sort sources for determinism (bug-class rule 2): by (ref, observed_at).
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Ref != sources[j].Ref {
			return sources[i].Ref < sources[j].Ref
		}
		return sources[i].ObservedAt < sources[j].ObservedAt
	})

	id := fmt.Sprintf("runtime:%s:%s:%s->%s",
		string(rec.Kind), rec.Key, rec.FromService, rec.ToService)

	return graph.Edge{
		ID:      id,
		From:    rec.FromService,
		To:      rec.ToService,
		Type:    kindToEdgeType(rec.Kind),
		Label:   rec.Key,
		Sources: sources,
	}
}

// kindToEdgeType maps a contract.Kind to the corresponding graph.EdgeType.
// R.1 handles HTTP; R.3/R.4 will extend for SSE and messaging.
func kindToEdgeType(k contract.Kind) graph.EdgeType {
	switch k {
	case contract.KindHTTP:
		return graph.EdgeTypeHTTPCall
	case contract.KindSSE:
		return graph.EdgeTypeSSEEndpoint
	default:
		return graph.EdgeTypeHTTPCall
	}
}

// ledgerEntryToUnresolved converts an IngestLedgerEntry to the
// graph.UnresolvedRef shape pinned in the runtime-flow plan.
func ledgerEntryToUnresolved(entry IngestLedgerEntry, capturesDir string) graph.UnresolvedRef {
	svc := entry.Session // session name as a service proxy; "unknown" when empty
	if svc == "" {
		svc = "unknown"
	}
	return graph.UnresolvedRef{
		Service: svc,
		File:    fmt.Sprintf("%s/%s/spans.otlp.json", capturesDir, entry.Session),
		Line:    0,
		Name:    entry.TraceID + "/" + entry.SpanID,
		Kind:    "otlp_" + entry.Reason,
	}
}
