package evidence

import (
	"context"
	"fmt"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// StaticProvider wraps the output of the indexer's static pipeline as the
// "static" evidence source.  Collect replaces Sources[] on every edge with a
// single static SourceRef derived from the From-node's file+line, so each
// run is a total recomputation (no stale sources from a previous session).
type StaticProvider struct {
	nodes []graph.Node
	edges []graph.Edge
	unres []graph.UnresolvedRef
}

// NewStaticProvider creates a StaticProvider from the completed pipeline output.
// Both slices are held by reference; callers must not modify them after construction.
func NewStaticProvider(nodes []graph.Node, edges []graph.Edge, unres []graph.UnresolvedRef) *StaticProvider {
	return &StaticProvider{nodes: nodes, edges: edges, unres: unres}
}

// Name implements Provider.
func (s *StaticProvider) Name() string { return "static" }

// Collect stamps every edge with a static SourceRef.
// The Ref field is "<file>:<line>" from the From node when available.
// VerificationState is left unset — the Reconciler assigns it from the full
// multi-provider picture.
func (s *StaticProvider) Collect(_ context.Context, _ *workspace.WorkspaceConfig) (Evidence, error) {
	nodeByID := make(map[string]*graph.Node, len(s.nodes))
	for i := range s.nodes {
		nodeByID[s.nodes[i].ID] = &s.nodes[i]
	}

	stamped := make([]graph.Edge, len(s.edges))
	for i, e := range s.edges {
		ref := staticRef(nodeByID[e.From])
		conf := e.Confidence
		if conf == "" {
			conf = graph.ConfidenceCandidate
		}
		stamped[i] = e
		// SA.1: the static provenance (layer/rule) is stamped by the link
		// passes as edges are minted (see StampStatic) and is the one part of
		// the static source the reconciler cannot recompute from node geometry
		// alone — carry it forward from the incoming edge.
		var layer, rule string
		for _, src := range e.Sources {
			if src.Provider == "static" {
				layer, rule = src.Layer, src.Rule
				break
			}
		}
		if rule == "" {
			// Parse-phase (L1) edges are minted by the tree-sitter pattern
			// engine and never pass through a link pass's StampStatic. Stamp a
			// coarse but correct rule here so no static edge reaches
			// persistence without one (ValidateStaticProvenance).
			layer, rule = "L1", "parser/"+string(e.Type)
		}
		// Total recomputation: replace Sources, never append to a stale list.
		stamped[i].Sources = []graph.SourceRef{{
			Provider:   "static",
			Confidence: conf,
			Ref:        ref,
			Layer:      layer,
			Rule:       rule,
		}}
		stamped[i].VerificationState = ""   // set by Reconciler
		stamped[i].VerifiedGranularity = "" // set by Reconciler when confirmed
	}

	return Evidence{
		Nodes:      s.nodes,
		Edges:      stamped,
		Unresolved: s.unres,
	}, nil
}

// ValidateStaticProvenance returns the IDs of edges that carry a "static"
// SourceRef with an empty Rule (SA.1). Every static edge must record which
// layer-contract producer minted it; a static source with no rule is a schema
// error. The Reconciler calls this after it has recomputed every edge's
// Sources[] and fails the run if the result is non-empty.
func ValidateStaticProvenance(edges []graph.Edge) []string {
	var bad []string
	for i := range edges {
		for _, src := range edges[i].Sources {
			if src.Provider == "static" && src.Rule == "" {
				bad = append(bad, edges[i].ID)
				break
			}
		}
	}
	return bad
}

// staticRef returns the provenance ref for a static edge's From node.
func staticRef(n *graph.Node) string {
	if n == nil || n.File == "" {
		return ""
	}
	if n.Line > 0 {
		return fmt.Sprintf("%s:%d", n.File, n.Line)
	}
	return n.File
}
