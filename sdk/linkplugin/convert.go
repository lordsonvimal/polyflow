package linkplugin

import (
	lpgraph "github.com/lordsonvimal/polyflow/sdk/linkplugin/graph"
	pb "github.com/lordsonvimal/polyflow/sdk/linkplugin/proto"
)

func nodeToPB(n lpgraph.Node) *pb.Node {
	return &pb.Node{
		Id:       n.ID,
		Type:     n.Type,
		Label:    n.Label,
		Service:  n.Service,
		File:     n.File,
		Line:     int64(n.Line),
		EndLine:  int64(n.EndLine),
		Language: n.Language,
		Meta:     n.Meta,
	}
}

func nodeFromPB(n *pb.Node) lpgraph.Node {
	return lpgraph.Node{
		ID:       n.GetId(),
		Type:     n.GetType(),
		Label:    n.GetLabel(),
		Service:  n.GetService(),
		File:     n.GetFile(),
		Line:     int(n.GetLine()),
		EndLine:  int(n.GetEndLine()),
		Language: n.GetLanguage(),
		Meta:     n.GetMeta(),
	}
}

func edgeToPB(e lpgraph.Edge) *pb.Edge {
	sources := make([]*pb.SourceRef, 0, len(e.Sources))
	for _, s := range e.Sources {
		sources = append(sources, &pb.SourceRef{
			Provider:   s.Provider,
			Confidence: s.Confidence,
			Ref:        s.Ref,
			ObservedAt: s.ObservedAt,
			CodeFile:   s.CodeFile,
			CodeFunc:   s.CodeFunc,
		})
	}
	return &pb.Edge{
		Id:                  e.ID,
		From:                e.From,
		To:                  e.To,
		Type:                e.Type,
		Label:               e.Label,
		Confidence:          e.Confidence,
		Method:              e.Method,
		Path:                e.Path,
		Meta:                e.Meta,
		Sources:             sources,
		VerificationState:   e.VerificationState,
		VerifiedGranularity: e.VerifiedGranularity,
	}
}

func edgeFromPB(e *pb.Edge) lpgraph.Edge {
	sources := make([]lpgraph.SourceRef, 0, len(e.GetSources()))
	for _, s := range e.GetSources() {
		sources = append(sources, lpgraph.SourceRef{
			Provider:   s.GetProvider(),
			Confidence: s.GetConfidence(),
			Ref:        s.GetRef(),
			ObservedAt: s.GetObservedAt(),
			CodeFile:   s.GetCodeFile(),
			CodeFunc:   s.GetCodeFunc(),
		})
	}
	return lpgraph.Edge{
		ID:                  e.GetId(),
		From:                e.GetFrom(),
		To:                  e.GetTo(),
		Type:                e.GetType(),
		Label:               e.GetLabel(),
		Confidence:          e.GetConfidence(),
		Method:              e.GetMethod(),
		Path:                e.GetPath(),
		Meta:                e.GetMeta(),
		Sources:             sources,
		VerificationState:   e.GetVerificationState(),
		VerifiedGranularity: e.GetVerifiedGranularity(),
	}
}

func unresolvedToPB(u lpgraph.UnresolvedRef) *pb.UnresolvedRef {
	return &pb.UnresolvedRef{
		Service: u.Service,
		File:    u.File,
		Line:    int64(u.Line),
		Name:    u.Name,
		Kind:    u.Kind,
		Targets: u.Targets,
	}
}

func unresolvedFromPB(u *pb.UnresolvedRef) lpgraph.UnresolvedRef {
	return lpgraph.UnresolvedRef{
		Service: u.GetService(),
		File:    u.GetFile(),
		Line:    int(u.GetLine()),
		Name:    u.GetName(),
		Kind:    u.GetKind(),
		Targets: u.GetTargets(),
	}
}

func resultToPB(r Result) *pb.Result {
	edges := make([]*pb.Edge, 0, len(r.Edges))
	for _, e := range r.Edges {
		edges = append(edges, edgeToPB(e))
	}
	unresolved := make([]*pb.UnresolvedRef, 0, len(r.Unresolved))
	for _, u := range r.Unresolved {
		unresolved = append(unresolved, unresolvedToPB(u))
	}
	return &pb.Result{
		Edges:      edges,
		Unresolved: unresolved,
		Retract:    r.Retract,
	}
}

func resultFromPB(r *pb.Result) Result {
	edges := make([]lpgraph.Edge, 0, len(r.GetEdges()))
	for _, e := range r.GetEdges() {
		edges = append(edges, edgeFromPB(e))
	}
	unresolved := make([]lpgraph.UnresolvedRef, 0, len(r.GetUnresolved()))
	for _, u := range r.GetUnresolved() {
		unresolved = append(unresolved, unresolvedFromPB(u))
	}
	return Result{
		Edges:      edges,
		Unresolved: unresolved,
		Retract:    r.GetRetract(),
	}
}
