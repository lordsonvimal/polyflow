package graph

import (
	"fmt"
	"sort"
	"strings"
)

// VerificationSummary is the top-level provenance section emitted by every
// query command. Always present (never absent — an absent section would look
// like certainty); survives any token budget cut.
type VerificationSummary struct {
	Verified        int    `json:"verified"`
	Candidate       int    `json:"candidate"`
	ObservedOnlyGap int    `json:"observed_only_gap"`
	Conflicting     int    `json:"conflicting"`
	Note            string `json:"note,omitempty"`
}

// BuildVerificationSummary computes the summary from a slice of edges.
// Edges with no VerificationState (pre-F.0 static-only edges) are not
// counted — they are neither verified nor candidate in the fusion sense.
func BuildVerificationSummary(edges []Edge) VerificationSummary {
	vs := VerificationSummary{}
	for _, e := range edges {
		switch e.VerificationState {
		case StateVerified:
			vs.Verified++
		case StateCandidate:
			vs.Candidate++
		case StateObservedOnlyGap:
			vs.ObservedOnlyGap++
		case StateConflicting:
			vs.Conflicting++
		}
	}
	vs.Note = buildVerificationNote(vs)
	return vs
}

func buildVerificationNote(vs VerificationSummary) string {
	var parts []string
	if vs.Candidate > 0 {
		parts = append(parts, fmt.Sprintf(
			"%d candidate edges are static-only; verify before relying on them.", vs.Candidate))
	}
	if vs.ObservedOnlyGap > 0 {
		parts = append(parts, fmt.Sprintf(
			"%d observed-only gaps indicate flows the static graph missed.", vs.ObservedOnlyGap))
	}
	return strings.Join(parts, " ")
}

// VerificationSummaryLine renders a one-line summary suitable for text/chain
// output formats. Returns "" when all counts are zero (nothing to report).
func VerificationSummaryLine(vs VerificationSummary) string {
	total := vs.Verified + vs.Candidate + vs.ObservedOnlyGap + vs.Conflicting
	if total == 0 {
		return ""
	}
	return fmt.Sprintf(
		"verification: verified=%d candidate=%d observed_only_gap=%d conflicting=%d",
		vs.Verified, vs.Candidate, vs.ObservedOnlyGap, vs.Conflicting)
}

// CompactSources converts a SourceRef slice to compact "provider:ref" strings,
// sorted by (provider, ref) for deterministic output (rule 2).
func CompactSources(sources []SourceRef) []string {
	if len(sources) == 0 {
		return nil
	}
	sorted := sortedSourceRefs(sources)
	out := make([]string, len(sorted))
	for i, s := range sorted {
		out[i] = s.Provider + ":" + s.Ref
	}
	return out
}

// SortedSources returns a copy of sources sorted by (provider, ref) for
// deterministic verbose output (rule 2).
func SortedSources(sources []SourceRef) []SourceRef {
	if len(sources) == 0 {
		return nil
	}
	return sortedSourceRefs(sources)
}

func sortedSourceRefs(sources []SourceRef) []SourceRef {
	sorted := make([]SourceRef, len(sources))
	copy(sorted, sources)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Provider != sorted[j].Provider {
			return sorted[i].Provider < sorted[j].Provider
		}
		return sorted[i].Ref < sorted[j].Ref
	})
	return sorted
}

// VerificationRank returns the tie-break rank for a verification state:
// 0 (best) = verified, 1 = observed_only_gap, 2 = candidate,
// 3 = conflicting, 4 = empty/unknown. Lower is better; used by
// RelatedFiles and rollupCallers to break ties within equal primary rank.
func VerificationRank(state string) int {
	switch state {
	case StateVerified:
		return 0
	case StateObservedOnlyGap:
		return 1
	case StateCandidate:
		return 2
	case StateConflicting:
		return 3
	default:
		return 4
	}
}
