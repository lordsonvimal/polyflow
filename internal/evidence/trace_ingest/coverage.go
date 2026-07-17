package trace_ingest

import (
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// CoverageRow is one row in the coverage report, keyed by edge type.
type CoverageRow struct {
	Kind      string  // graph edge type string (e.g. "http_call", "publishes")
	Total     int     // static edges of this kind (verified + candidate)
	Verified  int     // confirmed by runtime or contract evidence
	Candidate int     // static-only, not yet confirmed
	Gap       int     // observed_only_gap: runtime saw, static missed
	Pct       float64 // Verified / Total * 100 (0 when Total == 0)
}

// ObservedOnlyGap is a channel observed at runtime with no matching static edge.
type ObservedOnlyGap struct {
	Kind string
	Key  string
	From string
	To   string
}

// CoverageReport is the full coverage summary.
type CoverageReport struct {
	// Rows are per-kind rows, sorted by Kind for determinism (bug-class rule 2).
	Rows              []CoverageRow
	TotalChannels     int
	VerifiedChannels  int
	CandidateChannels int
	GapChannels       int
	// LedgerByReason maps ingest-ledger reason strings to span counts.
	LedgerByReason map[string]int
	// ObservedOnlyGaps lists gap edges sorted by (Kind, Key, From, To).
	ObservedOnlyGaps []ObservedOnlyGap
}

// ComputeCoverage tallies coverage from edges with pre-stamped VerificationState.
// Use for cumulative coverage (all sessions combined, from the graph store).
func ComputeCoverage(edges []graph.Edge, ledger []IngestLedgerEntry) CoverageReport {
	return buildReport(edges, ledger, nil)
}

// ComputeSessionCoverage computes what a single session's flow records cover
// against the full edge set. An edge is "session-verified" if a flow record
// matches it by (kindToEdgeType(flow.Kind), flow.Key). The stored
// VerificationState is IGNORED — this shows per-session contribution only.
// A session with no flow records reports 0% without modifying any input.
func ComputeSessionCoverage(flows []FlowRecord, edges []graph.Edge, ledger []IngestLedgerEntry) CoverageReport {
	return buildReport(edges, ledger, flows)
}

// buildReport is the shared implementation.
// When flows is nil, tally by stored VerificationState (cumulative mode).
// When flows is non-nil (possibly empty), join flow records against edges (session mode).
func buildReport(edges []graph.Edge, ledger []IngestLedgerEntry, flows []FlowRecord) CoverageReport {
	// Session mode: build observed set from flow records.
	type observedKey struct{ edgeType, label string }
	var observed map[observedKey]bool
	if flows != nil {
		observed = make(map[observedKey]bool, len(flows))
		for _, f := range flows {
			et := string(kindToEdgeType(f.Kind))
			observed[observedKey{et, f.Key}] = true
		}
	}

	type kindStats struct{ total, verified, candidate, gap int }
	byKind := make(map[string]*kindStats)
	var kindOrder []string // insertion order; sorted before building rows

	getKind := func(k string) *kindStats {
		if _, exists := byKind[k]; !exists {
			byKind[k] = &kindStats{}
			kindOrder = append(kindOrder, k)
		}
		return byKind[k]
	}

	var gaps []ObservedOnlyGap

	for _, e := range edges {
		kind := string(e.Type)
		st := getKind(kind)

		// Determine effective state.
		state := e.VerificationState
		if flows != nil && state != graph.StateObservedOnlyGap {
			// Session mode: re-derive from observed set.
			if observed[observedKey{kind, e.Label}] {
				state = graph.StateVerified
			} else {
				state = graph.StateCandidate
			}
		}

		switch state {
		case graph.StateVerified:
			st.total++
			st.verified++
		case graph.StateObservedOnlyGap:
			st.gap++
			gaps = append(gaps, ObservedOnlyGap{
				Kind: kind,
				Key:  e.Label,
				From: e.From,
				To:   e.To,
			})
		default: // candidate
			st.total++
			st.candidate++
		}
	}

	// Tally ledger entries by reason.
	ledgerByReason := make(map[string]int, 6)
	for _, l := range ledger {
		ledgerByReason[l.Reason]++
	}

	// Build rows sorted by Kind (bug-class rule 2 — never map iteration order).
	sort.Strings(kindOrder)
	rows := make([]CoverageRow, 0, len(kindOrder))
	var totalCh, verifiedCh, candidateCh, gapCh int
	for _, k := range kindOrder {
		st := byKind[k]
		pct := 0.0
		if st.total > 0 {
			pct = float64(st.verified) / float64(st.total) * 100
		}
		rows = append(rows, CoverageRow{
			Kind:      k,
			Total:     st.total,
			Verified:  st.verified,
			Candidate: st.candidate,
			Gap:       st.gap,
			Pct:       pct,
		})
		totalCh += st.total
		verifiedCh += st.verified
		candidateCh += st.candidate
		gapCh += st.gap
	}

	// Sort gaps by (Kind, Key, From, To) — deterministic (bug-class rule 2).
	sort.Slice(gaps, func(i, j int) bool {
		if gaps[i].Kind != gaps[j].Kind {
			return gaps[i].Kind < gaps[j].Kind
		}
		if gaps[i].Key != gaps[j].Key {
			return gaps[i].Key < gaps[j].Key
		}
		if gaps[i].From != gaps[j].From {
			return gaps[i].From < gaps[j].From
		}
		return gaps[i].To < gaps[j].To
	})

	return CoverageReport{
		Rows:              rows,
		TotalChannels:     totalCh,
		VerifiedChannels:  verifiedCh,
		CandidateChannels: candidateCh,
		GapChannels:       gapCh,
		LedgerByReason:    ledgerByReason,
		ObservedOnlyGaps:  gaps,
	}
}
