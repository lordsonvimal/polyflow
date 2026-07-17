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
		// Total recomputation: replace Sources, never append to a stale list.
		stamped[i].Sources = []graph.SourceRef{{
			Provider:   "static",
			Confidence: conf,
			Ref:        ref,
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
