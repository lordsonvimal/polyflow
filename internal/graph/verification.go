package graph

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// VerificationSummary is the top-level provenance section emitted by every
// query command. Always present (never absent — an absent section would look
// like certainty); survives any token budget cut.
type VerificationSummary struct {
	Verified        int    `json:"verified"`
	Candidate       int    `json:"candidate"`
	ObservedOnlyGap int    `json:"observed_only_gap"`
	Conflicting     int    `json:"conflicting"`
	// StaleEvidence is the count of verified edges whose runtime sources are
	// all older than the workspace stale_after threshold (C.2). Never changes
	// VerificationState — staleness is a visibility concern only.
	StaleEvidence int    `json:"stale_evidence,omitempty"`
	Note          string `json:"note,omitempty"`
}

// BuildVerificationSummaryAt computes the summary from a slice of edges,
// additionally counting verified edges with only stale runtime sources when
// staleAfter > 0 and now is non-zero. Never changes VerificationState.
func BuildVerificationSummaryAt(edges []Edge, staleAfter time.Duration, now time.Time) VerificationSummary {
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
		if staleAfter > 0 && !now.IsZero() && e.VerificationState == StateVerified {
			if edgeRuntimeSourcesAllStale(e.Sources, staleAfter, now) {
				vs.StaleEvidence++
			}
		}
	}
	vs.Note = buildVerificationNote(vs)
	return vs
}

// BuildVerificationSummary computes the summary without freshness checking.
// Edges with no VerificationState (pre-F.0 static-only edges) are not
// counted — they are neither verified nor candidate in the fusion sense.
func BuildVerificationSummary(edges []Edge) VerificationSummary {
	return BuildVerificationSummaryAt(edges, 0, time.Time{})
}

// edgeRuntimeSourcesAllStale returns true when e has at least one runtime
// source and ALL runtime sources are older than staleAfter.
func edgeRuntimeSourcesAllStale(sources []SourceRef, staleAfter time.Duration, now time.Time) bool {
	runtimeCount := 0
	staleCount := 0
	for _, s := range sources {
		if s.Provider != "runtime" {
			continue
		}
		runtimeCount++
		if s.ObservedAt != 0 && now.Sub(time.Unix(s.ObservedAt, 0)) > staleAfter {
			staleCount++
		}
	}
	return runtimeCount > 0 && runtimeCount == staleCount
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
	if vs.StaleEvidence > 0 {
		parts = append(parts, fmt.Sprintf(
			"%d verified edges have stale runtime evidence; consider re-capturing.", vs.StaleEvidence))
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

// AgeString renders a unix-seconds timestamp as a human-readable age string
// ("43d old", "2h old", "5m old", "just now"). Returns "" when now is zero
// or ts is 0 or ts is in the future (clock skew tolerance).
func AgeString(ts int64, now time.Time) string {
	if ts == 0 || now.IsZero() {
		return ""
	}
	age := now.Sub(time.Unix(ts, 0))
	if age < 0 {
		return ""
	}
	days := int(age.Hours() / 24)
	if days > 0 {
		return fmt.Sprintf("%dd old", days)
	}
	hours := int(age.Hours())
	if hours > 0 {
		return fmt.Sprintf("%dh old", hours)
	}
	mins := int(age.Minutes())
	if mins > 0 {
		return fmt.Sprintf("%dm old", mins)
	}
	return "just now"
}

// CompactSourcesAt converts a SourceRef slice to compact "provider:ref"
// strings, sorted by (provider, ref) for deterministic output (rule 2).
// When now is non-zero, runtime sources with ObservedAt set are annotated
// with their age: "runtime:sess/trace (43d old)".
func CompactSourcesAt(sources []SourceRef, now time.Time) []string {
	if len(sources) == 0 {
		return nil
	}
	sorted := sortedSourceRefs(sources)
	out := make([]string, len(sorted))
	for i, s := range sorted {
		str := s.Provider + ":" + s.Ref
		if !now.IsZero() && s.Provider == "runtime" && s.ObservedAt != 0 {
			if age := AgeString(s.ObservedAt, now); age != "" {
				str += " (" + age + ")"
			}
		}
		out[i] = str
	}
	return out
}

// CompactSources converts a SourceRef slice to compact "provider:ref" strings,
// sorted by (provider, ref) for deterministic output (rule 2). No age
// annotation — use CompactSourcesAt to add age rendering.
func CompactSources(sources []SourceRef) []string {
	return CompactSourcesAt(sources, time.Time{})
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
