package graph

import "sort"

// Epistemic verdicts.
const (
	EpistemicExact      = "exact"       // no known reason to doubt this answer
	EpistemicLowerBound = "lower_bound" // one or more Causes may hide edges
)

// Epistemic cause tags — each names a section elsewhere in the result that
// already explains the underlying gap; Causes is a pointer to where to look,
// not new information.
const (
	CauseUnresolvedReference = "unresolved_reference" // see `unresolved`
	CauseDynamicDispatch     = "dynamic_dispatch"     // a traversed edge had Confidence partial/unknown
	CauseCandidateEdge       = "candidate_edge"       // see `verification_summary.candidate`
	CauseConflictingEvidence = "conflicting_evidence" // see `verification_summary.conflicting`
	CauseUnmeasuredTrust     = "unmeasured_trust"     // see `trust.measured`
	CauseStaleTrust          = "stale_trust"          // see `trust.stale`
)

// Epistemic is the top-level trust verdict for a query result, derived from
// sections already present elsewhere in the same result (unresolved,
// verification_summary, trust) plus the traversed edges' Confidence. Always
// present — absence would look like certainty. Purely a reduction: adds no
// information beyond what those sections already say, just states the
// conclusion an agent would otherwise have to compute itself on every query.
type Epistemic struct {
	Verdict string   `json:"verdict"`
	Causes  []string `json:"causes"` // sorted, [] when Verdict is exact
}

// BuildEpistemic reduces already-computed signals into one verdict.
// dynamicDispatch is true when any edge traversed for this result carried
// Confidence partial/unknown — callers already hold that per-edge value on
// their own node/caller/hop lists (the same Confidence rendered elsewhere in
// the result), so it is passed as one bool rather than re-walking raw edges
// here.
func BuildEpistemic(unresolved []UnresolvedRef, dynamicDispatch bool, vs VerificationSummary, trust TrustStamp) Epistemic {
	causes := make(map[string]bool)
	if len(unresolved) > 0 {
		causes[CauseUnresolvedReference] = true
	}
	if dynamicDispatch {
		causes[CauseDynamicDispatch] = true
	}
	if vs.Candidate > 0 {
		causes[CauseCandidateEdge] = true
	}
	if vs.Conflicting > 0 {
		causes[CauseConflictingEvidence] = true
	}
	if !trust.Measured {
		causes[CauseUnmeasuredTrust] = true
	}
	if trust.Stale {
		causes[CauseStaleTrust] = true
	}

	out := Epistemic{Verdict: EpistemicExact, Causes: []string{}}
	for c := range causes {
		out.Causes = append(out.Causes, c)
	}
	sort.Strings(out.Causes) // rule 2: deterministic output
	if len(out.Causes) > 0 {
		out.Verdict = EpistemicLowerBound
	}
	return out
}

// HasWeakConfidence reports whether any confidence value in confidences is
// partial or unknown — the dynamic_dispatch signal BuildEpistemic consumes.
// Shared by every result package so the partial/unknown check is defined
// once instead of once per caller/node/hop list shape.
func HasWeakConfidence(confidences []string) bool {
	for _, c := range confidences {
		if c == ConfidencePartial || c == ConfidenceUnknown {
			return true
		}
	}
	return false
}
