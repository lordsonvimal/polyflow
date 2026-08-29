package pluginloader

import (
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/sdk/linkplugin"
	lpgraph "github.com/lordsonvimal/polyflow/sdk/linkplugin/graph"
)

// toSDKNodes converts core's internal/graph.Node (the shape a Link() call's
// batch is built from) into the SDK's vendored copy a plugin subprocess
// speaks over the wire — see docs/linker-plugin-architecture-plan.md's
// "Pinned Go surface": sdk/linkplugin/graph is a deliberate copy, not an
// import, of internal/graph, so this conversion is the seam where core's
// representation is free to diverge from the wire contract.
func toSDKNodes(nodes []graph.Node) []lpgraph.Node {
	out := make([]lpgraph.Node, len(nodes))
	for i, n := range nodes {
		out[i] = lpgraph.Node{
			ID:       n.ID,
			Type:     string(n.Type),
			Label:    n.Label,
			Service:  n.Service,
			File:     n.File,
			Line:     n.Line,
			EndLine:  n.EndLine,
			Language: n.Language,
			Meta:     n.Meta,
		}
	}
	return out
}

// LinkResult mirrors linkplugin.Result in internal/graph's vocabulary, the
// shape internal/indexer's linking pipeline already speaks. Callers outside
// this package never import sdk/linkplugin or sdk/linkplugin/graph directly
// — pluginloader stays "the only package outside sdk/linkplugin that speaks
// the plugin wire protocol" (see this package's doc comment).
type LinkResult struct {
	Edges      []graph.Edge
	Unresolved []graph.UnresolvedRef
	Retract    []string
}

func fromSDKResult(r linkplugin.Result) LinkResult {
	edges := make([]graph.Edge, len(r.Edges))
	for i, e := range r.Edges {
		edges[i] = graph.Edge{
			ID:                  e.ID,
			From:                e.From,
			To:                  e.To,
			Type:                graph.EdgeType(e.Type),
			Label:               e.Label,
			Confidence:          e.Confidence,
			Method:              e.Method,
			Path:                e.Path,
			Meta:                e.Meta,
			VerificationState:   e.VerificationState,
			VerifiedGranularity: e.VerifiedGranularity,
		}
		for _, s := range e.Sources {
			edges[i].Sources = append(edges[i].Sources, graph.SourceRef{
				Provider:   s.Provider,
				Confidence: s.Confidence,
				Ref:        s.Ref,
				ObservedAt: s.ObservedAt,
				CodeFile:   s.CodeFile,
				CodeFunc:   s.CodeFunc,
			})
		}
	}
	unresolved := make([]graph.UnresolvedRef, len(r.Unresolved))
	for i, u := range r.Unresolved {
		unresolved[i] = graph.UnresolvedRef{
			Service: u.Service,
			File:    u.File,
			Line:    u.Line,
			Name:    u.Name,
			Kind:    u.Kind,
			Targets: u.Targets,
		}
	}
	return LinkResult{Edges: edges, Unresolved: unresolved, Retract: r.Retract}
}

func toSDKResultMap(m map[string]LinkResult) map[string]linkplugin.Result {
	out := make(map[string]linkplugin.Result, len(m))
	for k, v := range m {
		out[k] = toSDKResult(v)
	}
	return out
}

func toSDKResult(r LinkResult) linkplugin.Result {
	edges := make([]lpgraph.Edge, len(r.Edges))
	for i, e := range r.Edges {
		edges[i] = lpgraph.Edge{
			ID:                  e.ID,
			From:                e.From,
			To:                  e.To,
			Type:                string(e.Type),
			Label:               e.Label,
			Confidence:          e.Confidence,
			Method:              e.Method,
			Path:                e.Path,
			Meta:                e.Meta,
			VerificationState:   e.VerificationState,
			VerifiedGranularity: e.VerifiedGranularity,
		}
		for _, s := range e.Sources {
			edges[i].Sources = append(edges[i].Sources, lpgraph.SourceRef{
				Provider:   s.Provider,
				Confidence: s.Confidence,
				Ref:        s.Ref,
				ObservedAt: s.ObservedAt,
				CodeFile:   s.CodeFile,
				CodeFunc:   s.CodeFunc,
			})
		}
	}
	unresolved := make([]lpgraph.UnresolvedRef, len(r.Unresolved))
	for i, u := range r.Unresolved {
		unresolved[i] = lpgraph.UnresolvedRef{
			Service: u.Service,
			File:    u.File,
			Line:    u.Line,
			Name:    u.Name,
			Kind:    u.Kind,
			Targets: u.Targets,
		}
	}
	return linkplugin.Result{Edges: edges, Unresolved: unresolved, Retract: r.Retract}
}
