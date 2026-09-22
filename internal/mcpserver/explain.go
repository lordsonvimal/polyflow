package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

type explainInput struct {
	EdgeID string `json:"edge_id" jsonschema:"the id of the edge to explain (from trace, a graph dump, or unknown_edges' entries)"`
}

// explain implements the MCP side of SA.1's `polyflow explain`: the full
// provenance of one edge — evidence sources, the layer/rule chain that minted
// it, and ledger rows at the same site. The agent asking "why is this edge
// here" is the primary consumer, so it is wired here as well as on the CLI.
func (s *Server) explain(ctx context.Context, req *mcp.CallToolRequest, in explainInput) (*mcp.CallToolResult, any, error) {
	if in.EdgeID == "" {
		return nil, nil, fmt.Errorf("edge_id is required")
	}
	store, idx, _ := s.snapshot()

	ledger, err := s.unresolvedRefs(ctx, store)
	if err != nil {
		return nil, nil, fmt.Errorf("list unresolved refs: %w", err)
	}

	ex, ok := graph.ExplainEdge(idx, ledger, in.EdgeID)
	if !ok {
		return jsonResult(map[string]string{"error": fmt.Sprintf("no edge with id %q in the graph", in.EdgeID)})
	}
	return jsonResult(ex)
}
