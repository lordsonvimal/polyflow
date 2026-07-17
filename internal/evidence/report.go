package evidence

import (
	"sort"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// ReconcileReport summarizes the fusion result for polyflow reconcile / doctor.
// All lists are sorted by stable keys (bug-class rule 2 — never map-iteration order).
type ReconcileReport struct {
	TotalEdges       int
	VerifiedEdges    int
	CandidateEdges   int
	GapEdges         int
	ConflictingEdges int
	VerifiedPct      float64 // VerifiedEdges/TotalEdges*100 (0 when TotalEdges==0)

	// ByKind is per-edge-type coverage, sorted by Kind.
	ByKind []KindSummary

	// CandidateList: static-only unconfirmed edges, sorted by (Kind, Key, From, To).
	CandidateList []EdgeSummary

	// GapList: observed_only_gap edges, sorted by (Kind, Key, From, To).
	GapList []EdgeSummary

	// ConflictingList: conflicting edges, sorted by (Kind, Key, From, To).
	ConflictingList []EdgeSummary
}

// KindSummary is one row in the per-kind coverage breakdown.
type KindSummary struct {
	Kind        string
	Total       int     // Verified + Candidate (gap/conflicting excluded — they have no static anchor)
	Verified    int
	Candidate   int
	Gap         int
	Conflicting int
	Pct         float64 // Verified / Total * 100 (0 when Total==0)
}

// EdgeSummary is a minimal representation of one edge for list output.
type EdgeSummary struct {
	Kind    string   // edge type string
	Key     string   // channel label
	From    string   // from node/service ID
	To      string   // to node/service ID
	Sources []string // provider names present on the edge, sorted
}

// BuildReport computes a ReconcileReport from a set of already-reconciled edges.
// It reads VerificationState values that the Reconciler stamped during index; no
// providers are called here. Deterministic: all output is sorted by stable keys
// (bug-class rule 2).
func BuildReport(edges []graph.Edge) ReconcileReport {
	type kindStats struct {
		verified, candidate, gap, conflicting int
	}
	byKind := make(map[string]*kindStats)
	var kindOrder []string
	kindSeen := make(map[string]bool)

	getKind := func(k string) *kindStats {
		if !kindSeen[k] {
			kindSeen[k] = true
			kindOrder = append(kindOrder, k)
			byKind[k] = &kindStats{}
		}
		return byKind[k]
	}

	var candidates, gaps, conflicting []EdgeSummary

	for _, e := range edges {
		kind := string(e.Type)
		st := getKind(kind)
		providers := edgeProviders(e)

		switch e.VerificationState {
		case graph.StateVerified:
			st.verified++
		case graph.StateObservedOnlyGap:
			st.gap++
			gaps = append(gaps, EdgeSummary{Kind: kind, Key: e.Label, From: e.From, To: e.To, Sources: providers})
		case graph.StateConflicting:
			st.conflicting++
			conflicting = append(conflicting, EdgeSummary{Kind: kind, Key: e.Label, From: e.From, To: e.To, Sources: providers})
		default: // candidate (or empty — treat as candidate)
			st.candidate++
			candidates = append(candidates, EdgeSummary{Kind: kind, Key: e.Label, From: e.From, To: e.To, Sources: providers})
		}
	}

	// Sort edge lists by (Kind, Key, From, To) — deterministic (bug-class rule 2).
	sortEdgeSummaries(candidates)
	sortEdgeSummaries(gaps)
	sortEdgeSummaries(conflicting)

	// Build kind rows sorted by Kind.
	sort.Strings(kindOrder)
	kindRows := make([]KindSummary, 0, len(kindOrder))
	var totalEdges, verified, candidate, gap, conf int
	for _, k := range kindOrder {
		st := byKind[k]
		total := st.verified + st.candidate // gap/conflicting have no static anchor
		pct := 0.0
		if total > 0 {
			pct = float64(st.verified) / float64(total) * 100
		}
		kindRows = append(kindRows, KindSummary{
			Kind:        k,
			Total:       total,
			Verified:    st.verified,
			Candidate:   st.candidate,
			Gap:         st.gap,
			Conflicting: st.conflicting,
			Pct:         pct,
		})
		totalEdges += total
		verified += st.verified
		candidate += st.candidate
		gap += st.gap
		conf += st.conflicting
	}

	pct := 0.0
	if totalEdges > 0 {
		pct = float64(verified) / float64(totalEdges) * 100
	}

	return ReconcileReport{
		TotalEdges:       totalEdges,
		VerifiedEdges:    verified,
		CandidateEdges:   candidate,
		GapEdges:         gap,
		ConflictingEdges: conf,
		VerifiedPct:      pct,
		ByKind:           kindRows,
		CandidateList:    candidates,
		GapList:          gaps,
		ConflictingList:  conflicting,
	}
}

func sortEdgeSummaries(s []EdgeSummary) {
	sort.Slice(s, func(i, j int) bool {
		if s[i].Kind != s[j].Kind {
			return s[i].Kind < s[j].Kind
		}
		if s[i].Key != s[j].Key {
			return s[i].Key < s[j].Key
		}
		if s[i].From != s[j].From {
			return s[i].From < s[j].From
		}
		return s[i].To < s[j].To
	})
}

// edgeProviders returns a sorted, deduplicated list of provider names from e.Sources.
func edgeProviders(e graph.Edge) []string {
	seen := make(map[string]bool, len(e.Sources))
	var names []string
	for _, s := range e.Sources {
		if !seen[s.Provider] {
			seen[s.Provider] = true
			names = append(names, s.Provider)
		}
	}
	sort.Strings(names)
	return names
}
