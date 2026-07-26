package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

type entrypointsInput struct {
	Service string `json:"service,omitempty" jsonschema:"restrict to entrypoints in this service"`
	Type    string `json:"type,omitempty" jsonschema:"restrict to one entrypoint type: http_handler, subscriber, worker, grpc_handler, graphql_resolver"`
	Feature string `json:"feature,omitempty" jsonschema:"case-insensitive substring filter against the entrypoint label and file path"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max entries returned (default 200, -1 = unlimited)"`
}

// entrypointEntry is one catalogued entry node. Method/Path are populated for
// http_handler nodes (stamped by the route matchers into node Meta).
type entrypointEntry struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Label   string `json:"label"`
	Service string `json:"service"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Method  string `json:"method,omitempty"`
	Path    string `json:"path,omitempty"`
}

type entrypointsOutput struct {
	Entrypoints []entrypointEntry `json:"entrypoints"`
	Total       int               `json:"total"`
	Truncated   bool              `json:"truncated,omitempty"`
}

func (s *Server) entrypoints(ctx context.Context, req *mcp.CallToolRequest, in entrypointsInput) (*mcp.CallToolResult, any, error) {
	if in.Type != "" && !entrypointNodeTypes[graph.NodeType(in.Type)] {
		return nil, nil, fmt.Errorf("unknown entrypoint type: %s (use: http_handler, subscriber, worker, grpc_handler, graphql_resolver)", in.Type)
	}

	_, idx, _ := s.snapshot()
	feature := strings.ToLower(in.Feature)

	var all []entrypointEntry
	for _, n := range idx.Nodes {
		if !entrypointNodeTypes[n.Type] {
			continue
		}
		if in.Type != "" && string(n.Type) != in.Type {
			continue
		}
		if in.Service != "" && n.Service != in.Service {
			continue
		}
		if feature != "" && !strings.Contains(strings.ToLower(n.Label), feature) && !strings.Contains(strings.ToLower(n.File), feature) {
			continue
		}
		all = append(all, entrypointEntry{
			ID:      n.ID,
			Type:    string(n.Type),
			Label:   n.Label,
			Service: n.Service,
			File:    n.File,
			Line:    n.Line,
			Method:  n.Meta["method"],
			Path:    n.Meta["path"],
		})
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].Service != all[j].Service {
			return all[i].Service < all[j].Service
		}
		if all[i].Type != all[j].Type {
			return all[i].Type < all[j].Type
		}
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		if all[i].Line != all[j].Line {
			return all[i].Line < all[j].Line
		}
		return all[i].ID < all[j].ID
	})

	out := entrypointsOutput{Total: len(all)}
	limit := in.Limit
	switch {
	case limit < 0:
		out.Entrypoints = all
	case limit == 0:
		limit = 200
		fallthrough
	default:
		if len(all) > limit {
			out.Entrypoints = all[:limit]
			out.Truncated = true
		} else {
			out.Entrypoints = all
		}
	}
	if out.Entrypoints == nil {
		out.Entrypoints = []entrypointEntry{}
	}
	return jsonResult(out)
}
