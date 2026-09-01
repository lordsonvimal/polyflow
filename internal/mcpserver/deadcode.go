package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lordsonvimal/polyflow/internal/deadcode"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

type deadcodeInput struct {
	Service      string `json:"service,omitempty" jsonschema:"restrict the scan to this service"`
	File         string `json:"file,omitempty" jsonschema:"restrict the scan to this file"`
	Transitive   bool   `json:"transitive,omitempty" jsonschema:"also flag callables/types reachable only from other dead code — sound on Go/TS, a lead only on Ruby (partial call graph)"`
	IncludeTypes bool   `json:"include_types,omitempty" jsonschema:"also flag struct/interface/type_alias declarations with no inbound type-use edge"`
}

func (s *Server) deadcode(ctx context.Context, req *mcp.CallToolRequest, in deadcodeInput) (*mcp.CallToolResult, any, error) {
	store, idx, _ := s.snapshot()
	s.mu.RLock()
	unresolved := s.fleetUnresolvedRefs
	s.mu.RUnlock()
	if unresolved == nil {
		var err error
		unresolved, err = store.ListUnresolvedRefs(ctx)
		if err != nil {
			return nil, nil, err
		}
	}
	unresolved = graph.DropExternalFrameworkRefs(unresolved, idx)
	out := deadcode.Build(idx, deadcode.Options{
		Service:        in.Service,
		File:           in.File,
		Transitive:     in.Transitive,
		IncludeTypes:   in.IncludeTypes,
		UnresolvedRefs: unresolved,
	})
	return jsonResult(out)
}
