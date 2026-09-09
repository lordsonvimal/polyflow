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
//
// SA.1: layer and rule record which layer-contract tier and concrete producer
// minted the edge. Callers stamp a coarse uniform value (Layer "L5", Rule =
// link-pass name) today; per-layer refinement lands with SA.2/SA.3/SA.5. An
// empty rule on a static source is a schema error caught by
// ValidateStaticProvenance.
func StampStatic(e *graph.Edge, fromRef, layer, rule string) {
	conf := e.Confidence
	if conf == "" {
		conf = graph.ConfidenceCandidate
	}
	// A producer that already stamped a precise layer/rule (SA.2+) on the
	// edge's first source keeps it — the uniform value is only a fallback.
	if len(e.Sources) == 1 && e.Sources[0].Provider == "static" {
		if e.Sources[0].Layer != "" {
			layer = e.Sources[0].Layer
		}
		if e.Sources[0].Rule != "" {
			rule = e.Sources[0].Rule
		}
	}
	e.Sources = []graph.SourceRef{{
		Provider:   "static",
		Confidence: conf,
		Ref:        fromRef,
		Layer:      layer,
		Rule:       rule,
	}}
	e.VerificationState = graph.StateCandidate
	e.VerifiedGranularity = ""
}

// StaticEdgeRef returns the provenance ref for an edge's From node, matching
// staticRef. Exported so the indexer can compute it against its own node set.
func StaticEdgeRef(fromNode *graph.Node) string {
	return staticRef(fromNode)
}
