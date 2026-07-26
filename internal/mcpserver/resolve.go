package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

type resolveInput struct {
	Query         string `json:"query" jsonschema:"search query: node id, label, or natural-language description"`
	TargetService string `json:"target_service,omitempty" jsonschema:"restrict candidates to this service"`
	TargetType    string `json:"target_type,omitempty" jsonschema:"restrict candidates to this node type"`
	Limit         int    `json:"limit,omitempty" jsonschema:"max candidates returned (default 10)"`
}

// resolveCandidate is one ranked candidate node, enriched from the adjacency
// index so context/impact/trace/flows can be called directly against its ID
// without a further lookup.
type resolveCandidate struct {
	ID        string  `json:"id"`
	Type      string  `json:"type"`
	Label     string  `json:"label"`
	Service   string  `json:"service"`
	File      string  `json:"file"`
	Line      int     `json:"line"`
	Score     float64 `json:"score,omitempty"`
	Retrieval string  `json:"retrieval,omitempty"`
}

type resolveOutput struct {
	Root             *graph.Node             `json:"root"`
	Candidates       []resolveCandidate      `json:"candidates"`
	TargetCandidates []graph.TargetCandidate `json:"target_candidates"`
}

// resolve ranks candidate nodes for a query, reusing the hybrid Searcher when
// wired (ranked by score/retrieval) and graph.ResolveTarget for the same
// exact-match disambiguation contract (target_candidates) the other query
// tools use — cuts a round-trip before context/impact/trace/flows.
func (s *Server) resolve(ctx context.Context, req *mcp.CallToolRequest, in resolveInput) (*mcp.CallToolResult, any, error) {
	if in.Query == "" {
		return nil, nil, fmt.Errorf("query is required")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}

	store, idx, searcher := s.snapshot()

	root, targetCandidates, err := resolveNode(ctx, store, in.Query, in.TargetService, in.TargetType)
	if err != nil {
		return nil, nil, err
	}

	var candidates []resolveCandidate
	if searcher != nil {
		resp, serr := searcher.Search(ctx, in.Query, limit)
		if serr == nil {
			for _, h := range resp.Nodes {
				n := idx.Nodes[h.Entity.NodeID]
				if n == nil {
					n = idx.Nodes[h.Entity.ID]
				}
				if n == nil {
					continue
				}
				if in.TargetService != "" && n.Service != in.TargetService {
					continue
				}
				if in.TargetType != "" && string(n.Type) != in.TargetType {
					continue
				}
				candidates = append(candidates, resolveCandidate{
					ID: n.ID, Type: string(n.Type), Label: n.Label, Service: n.Service,
					File: n.File, Line: n.Line, Score: h.Score, Retrieval: h.Retrieval,
				})
			}
		}
	}

	if len(candidates) == 0 {
		nodes, serr := store.SearchNodes(ctx, in.Query, limit)
		if serr != nil {
			return nil, nil, serr
		}
		for _, n := range nodes {
			if in.TargetService != "" && n.Service != in.TargetService {
				continue
			}
			if in.TargetType != "" && string(n.Type) != in.TargetType {
				continue
			}
			candidates = append(candidates, resolveCandidate{
				ID: n.ID, Type: string(n.Type), Label: n.Label, Service: n.Service, File: n.File, Line: n.Line,
			})
		}
	}
	if candidates == nil {
		candidates = []resolveCandidate{}
	}

	return jsonResult(resolveOutput{Root: root, Candidates: candidates, TargetCandidates: targetCandidates})
}
