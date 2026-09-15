package factpipe

import (
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// hub_js_http_grade.go is the "js_http_grade" hub provider (see hub.go) —
// the Tier FX FX.8.16 migration of internal/linker/js_http_grade.go's
// retired GradeJSHTTPProducers (JG.1).
//
// The roster's "check before attempting, may not belong in Tier FX's scope
// at all" caveat predates `patch:` (FX.8.8, 2026-09-15): this pass only ever
// adds meta keys to an EXISTING http_client node (path_evidence,
// confidence_ceiling) and mints/joins nothing — exactly `patch:`'s shape,
// no edge involved at all. It needs no file I/O and no cross-node lookup
// (unlike every other FX.8 hub so far) — each candidate node is graded from
// its own Meta alone, so this hub is the simplest one yet: a pure filter +
// graph.PathEvidence classification over nodes, ported verbatim from the
// retired Go.
//
// Two predicates, not one, because the "don't clobber a ceiling another
// pass already set" rule is a per-row conditional `patch:`'s add/overwrite
// Meta overlay can't express as a single optionally-empty column (same
// config_baseurl precedent, hub_config_baseurl.go's cbPatchPred/
// cbRegradePred split) — a row only lands in jhgCeilingPred when the
// existing node had no confidence_ceiling yet.
func init() { RegisterHub("js_http_grade", jsHTTPGradeHub) }

const (
	jhgEvidencePred = "js_http_grade_evidence" // (ID) — weak path evidence, always stamped
	jhgCeilingPred  = "js_http_grade_ceiling"  // (ID) — only when no ceiling was set yet
)

func jsHTTPGradeHub(nodes []graph.Node, _ []string, _ string, _ []graph.LinkHint) []Fact {
	var out []Fact
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPClient {
			continue
		}
		if n.Language != "javascript" && n.Language != "typescript" {
			continue
		}
		if _, ok := n.Meta["path_evidence"]; ok {
			continue
		}
		if n.Meta["nav_link"] == "true" {
			continue
		}
		if n.Meta["env_var"] != "" || n.Meta["host_default_literal"] != "" ||
			n.Meta["target_service"] != "" || n.Meta["path_resolved_via"] != "" {
			continue
		}

		pattern := n.Meta["path"]
		if pattern == "" {
			pattern = n.Meta["url"]
		}
		if !strings.HasPrefix(pattern, "*") {
			continue
		}

		if graph.PathEvidence(pattern) != graph.PathEvidenceWeak {
			continue
		}
		out = append(out, Fact{Pred: jhgEvidencePred, Args: []Atom{Node(n.ID)}})
		if n.Meta["confidence_ceiling"] == "" {
			out = append(out, Fact{Pred: jhgCeilingPred, Args: []Atom{Node(n.ID)}})
		}
	}
	return out
}
