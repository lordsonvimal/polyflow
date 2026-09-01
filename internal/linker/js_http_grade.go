package linker

import (
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// GradeJSHTTPProducers is JG.1 — the JS/TS analogue of the path-evidence stamp
// internal/parser/go_wrapper_urls.go:emitResolvedClient applies inline for Go
// SSA-wrapper producers. It runs as a linker pass after every JS/TS host
// resolver (ResolveJSHTTPHosts / Tier JH, ResolveConfigBaseURLPaths / Tier CB,
// LinkReactPropURLs) so a host those passes *do* pin is never mis-graded, and
// before the contract engine so matchProducer's fan-out suppressor
// (internal/contract/engine.go: `path_evidence == "weak" && distinctTargetServices > 1`)
// can see the grade.
//
// The problem it closes: a JS/TS http_client whose host segment stays an
// opaque wildcard (`${this.props.some_url}/x/unlock` → key `*/*/unlock`) and
// whose path pins a single literal segment names a *convention* — `/unlock`,
// `/health`, `/login` — that every service of a given framework implements, not
// a route. Ungraded, it matched a devise `GET /users/unlock` route in an
// unrelated fleet service. `graph.PathEvidence` already grades this
// language-agnostically; only the stamping was Go-only.
//
// Returns the mutated nodes for re-persist; metas are also mutated in place in
// the passed slice.
func GradeJSHTTPProducers(nodes []graph.Node) []graph.Node {
	var changed []graph.Node
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPClient {
			continue
		}
		if n.Language != "javascript" && n.Language != "typescript" {
			continue
		}
		// Idempotent re-run: an existing grade (from a prior pass or a prior
		// run of this one) is authoritative.
		if _, ok := n.Meta["path_evidence"]; ok {
			continue
		}
		// nav links are graded by http.yaml variant 2, which does not consult
		// path_evidence — stamping them would be inert noise.
		if n.Meta["nav_link"] == "true" {
			continue
		}
		// Host resolved by an earlier pass — the path is no longer the only
		// evidence, so the weak-path hedge does not apply.
		if n.Meta["env_var"] != "" || n.Meta["host_default_literal"] != "" ||
			n.Meta["target_service"] != "" || n.Meta["path_resolved_via"] != "" {
			continue
		}

		pattern := n.Meta["path"]
		if pattern == "" {
			pattern = n.Meta["url"]
		}
		// Only an opaque-host pattern is in scope: a leading wildcard is the
		// KeyWalker's marker for "at least one unresolved `${...}`/concat
		// operand at the host position" (patterns/matcher.go,
		// jsReconstructTemplateString/jsReconstructConcat). A root-relative
		// literal path ("/app/foo") already grades strong via PathEvidence's
		// no-wildcard branch and needs no marker.
		if !strings.HasPrefix(pattern, "*") {
			continue
		}

		switch graph.PathEvidence(pattern) {
		case graph.PathEvidenceWeak:
			n.Meta = ensureMeta(n.Meta)
			n.Meta["path_evidence"] = graph.PathEvidenceWeak
			// Parity with go_wrapper_urls.go:202 — one literal segment behind an
			// opaque host is thin even when it resolves in exactly one service
			// (an outbound third-party call `*/emails` collides with a
			// workspace route on the segment alone). Cap at `partial` so a
			// spec-only match never promotes to `verified`; runtime/config
			// evidence, which pins the real host, still can. Do not clobber a
			// ceiling another pass set for its own reason.
			if n.Meta["confidence_ceiling"] == "" {
				n.Meta["confidence_ceiling"] = graph.ConfidencePartial
			}
			changed = append(changed, *n)
		}
	}
	return changed
}
