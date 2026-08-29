package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lordsonvimal/polyflow/internal/deadcode"
)

type deadcodeInput struct {
	Service string `json:"service,omitempty" jsonschema:"restrict the scan to this service"`
	File    string `json:"file,omitempty" jsonschema:"restrict the scan to this file"`
}

func (s *Server) deadcode(ctx context.Context, req *mcp.CallToolRequest, in deadcodeInput) (*mcp.CallToolResult, any, error) {
	store, idx, _ := s.snapshot()
	unresolved, err := store.ListUnresolvedRefs(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := deadcode.Build(idx, deadcode.Options{Service: in.Service, File: in.File, UnresolvedRefs: unresolved})
	return jsonResult(out)
}
