package pluginloader

import (
	"context"

	"github.com/lordsonvimal/polyflow/internal/graph"
	pb "github.com/lordsonvimal/polyflow/sdk/linkplugin/proto"
)

// CapabilitiesServer backs the three bulk-query capabilities
// (containment/symbol_resolver/dynamic_key_ledger) a plugin component can
// request in manifest.yaml's requires:. It is built fresh per index run
// from that run's current in-memory nodes/edges/unresolved-ledger — not a
// stored-DB read — since a plugin's Link() call happens before this run's
// own output is persisted (see docs/linker-plugin-architecture-plan.md's
// "DB persistence & pass timing").
//
// Every method is bulk (a []string of files in, one response), never
// per-node, mirroring the ContainmentIndex/SymbolResolver/DynamicKeyLedger
// contract in sdk/linkplugin — the interface shape is the
// batching-enforcement mechanism (see the plan's Performance section).
type CapabilitiesServer struct {
	pb.UnimplementedCapabilitiesServer

	idx        *graph.AdjacencyIndex
	unresolved []graph.UnresolvedRef
}

// NewCapabilitiesServer builds the bulk indexes a plugin's Link() call may
// dial back into, from the same in-memory nodes/edges/unresolved-ref slices
// core's own linking pipeline (internal/indexer/link_passes.go) is
// threading through buildLinkPasses at the point this plugin's pass runs.
func NewCapabilitiesServer(nodes []graph.Node, edges []graph.Edge, unresolved []graph.UnresolvedRef) *CapabilitiesServer {
	idx := graph.NewAdjacencyIndex()
	for i := range nodes {
		idx.AddNode(&nodes[i])
	}
	for i := range edges {
		idx.AddEdge(&edges[i])
	}
	return &CapabilitiesServer{idx: idx, unresolved: unresolved}
}

func wantSet(files []string) map[string]bool {
	want := make(map[string]bool, len(files))
	for _, f := range files {
		want[f] = true
	}
	return want
}

// isScopeType reports whether a node type is a "scope" a containment query
// means (class/struct/interface), as opposed to the file-level parent every
// declaration also has via the same contains backbone.
func isScopeType(t graph.NodeType) bool {
	switch t {
	case graph.NodeTypeClass, graph.NodeTypeStruct, graph.NodeTypeInterface:
		return true
	}
	return false
}

// ContainmentBulkResolve answers, for each requested file, the nearest
// enclosing class/struct/interface scope reachable via a `contains` edge —
// the same backbone internal/graph/tree.go's BuildTree walks for the tree
// view, but read directly from this run's in-memory edges rather than a
// persisted AdjacencyIndex.
func (s *CapabilitiesServer) ContainmentBulkResolve(_ context.Context, req *pb.BulkResolveRequest) (*pb.BulkResolveResponse, error) {
	want := wantSet(req.GetFiles())
	scopes := make(map[string]*pb.Scope, len(want))
	for _, edges := range s.idx.OutEdges {
		for _, e := range edges {
			if e.Type != graph.EdgeTypeContains {
				continue
			}
			child := s.idx.Nodes[e.To]
			if child == nil || !want[child.File] {
				continue
			}
			parent := s.idx.Nodes[e.From]
			if parent == nil {
				continue
			}
			existing, ok := scopes[child.File]
			switch {
			case !ok:
				scopes[child.File] = &pb.Scope{Kind: string(parent.Type), Id: parent.ID, Label: parent.Label}
			case !isScopeType(graph.NodeType(existing.GetKind())) && isScopeType(parent.Type):
				// A class/struct/interface parent is a more useful answer
				// to "what scope contains this file" than a bare file
				// parent found via an earlier edge in map iteration order.
				scopes[child.File] = &pb.Scope{Kind: string(parent.Type), Id: parent.ID, Label: parent.Label}
			}
		}
	}
	return &pb.BulkResolveResponse{Scopes: scopes}, nil
}

// SymbolBulkResolve answers, for each requested file, the file's own
// declaration node — a lexical-scope resolution ("what does this file
// define at top level") built from the same node set, without needing a
// stored index. This is a first cut: it does not walk mixin/inheritance
// chains the way internal/linker/ruby_mixin_methods.go's per-language index
// does; a component that needs mixin-aware resolution is expected to widen
// this alongside a real consumer (Phase 1+), not add per-node RPC calls.
func (s *CapabilitiesServer) SymbolBulkResolve(_ context.Context, req *pb.BulkResolveRequest) (*pb.BulkResolveResponse, error) {
	want := wantSet(req.GetFiles())
	scopes := make(map[string]*pb.Scope, len(want))
	for _, n := range s.idx.Nodes {
		if !want[n.File] {
			continue
		}
		if existing, ok := scopes[n.File]; !ok || n.Line < int(mustScopeLine(s.idx, existing)) {
			scopes[n.File] = &pb.Scope{Kind: string(n.Type), Id: n.ID, Label: n.Label}
		}
	}
	return &pb.BulkResolveResponse{Scopes: scopes}, nil
}

// mustScopeLine looks up a previously-recorded scope's source line so
// SymbolBulkResolve can keep the earliest (outermost) declaration per file.
func mustScopeLine(idx *graph.AdjacencyIndex, s *pb.Scope) int64 {
	if n := idx.Nodes[s.GetId()]; n != nil {
		return int64(n.Line)
	}
	return 0
}

// KeyLedgerBulkResolve answers, for each requested file, one unresolved
// dynamic-key ledger entry recorded against it — a thin bulk filter over
// graph.UnresolvedInFiles (internal/graph/unresolved.go), the same helper
// core's own linkers use to read the ledger in bulk.
func (s *CapabilitiesServer) KeyLedgerBulkResolve(_ context.Context, req *pb.BulkResolveRequest) (*pb.BulkResolveResponse, error) {
	want := wantSet(req.GetFiles())
	matches := graph.UnresolvedInFiles(s.unresolved, want)
	scopes := make(map[string]*pb.Scope, len(matches))
	for _, r := range matches {
		scopes[r.File] = &pb.Scope{Kind: r.Kind, Id: r.Name, Label: r.Targets}
	}
	return &pb.BulkResolveResponse{Scopes: scopes}, nil
}
