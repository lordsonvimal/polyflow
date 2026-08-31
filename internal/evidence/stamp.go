package evidence

import "github.com/lordsonvimal/polyflow/internal/graph"

// StampStatic writes the provenance an edge would carry if the static pipeline
// were its only evidence: a single "static" SourceRef and VerificationState
// "candidate" (computeState's outcome for a static-only Sources list, whatever
// the edge's confidence). fromRef is staticRef(fromNode) — "<file>:<line>".
//
// The indexer calls this as edges are written by the link passes so the DB
// already holds correct provenance for the ~99% of edges no non-static
// provider touches; the F.0 reconciler then only re-upserts the edges it
// actually changes (gap edges, spec/runtime/config confirmations) instead of
// re-writing the entire edge table. Keep in lockstep with
// StaticProvider.Collect + computeState — a divergence shows up directly as a
// sources_json / verification_state diff.
func StampStatic(e *graph.Edge, fromRef string) {
	conf := e.Confidence
	if conf == "" {
		conf = graph.ConfidenceCandidate
	}
	e.Sources = []graph.SourceRef{{
		Provider:   "static",
		Confidence: conf,
		Ref:        fromRef,
	}}
	e.VerificationState = graph.StateCandidate
	e.VerifiedGranularity = ""
}

// StaticEdgeRef returns the provenance ref for an edge's From node, matching
// staticRef. Exported so the indexer can compute it against its own node set.
func StaticEdgeRef(fromNode *graph.Node) string {
	return staticRef(fromNode)
}
