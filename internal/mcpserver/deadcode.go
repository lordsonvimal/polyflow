package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lordsonvimal/polyflow/internal/deadcode"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

type deadcodeInput struct {
	Service string `json:"service,omitempty" jsonschema:"restrict the scan to this service"`
	File    string `json:"file,omitempty" jsonschema:"restrict the scan to this file"`
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
	out := deadcode.Build(idx, deadcode.Options{Service: in.Service, File: in.File, UnresolvedRefs: unresolved})
	return jsonResult(out)
}
