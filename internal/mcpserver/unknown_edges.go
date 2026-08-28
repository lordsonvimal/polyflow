package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

type unknownEdgesInput struct {
	MinConfidence string `json:"min_confidence,omitempty" jsonschema:"report edges AT OR BELOW this confidence tier: unknown (default), partial, inferred, static"`
	Service       string `json:"service,omitempty" jsonschema:"restrict to producers in this service"`
	EdgeType      string `json:"edge_type,omitempty" jsonschema:"restrict to one edge type, e.g. http_call, publishes (default: every confidence-bearing type)"`
	Limit         int    `json:"limit,omitempty" jsonschema:"max entries returned (default 200, -1 = unlimited)"`
}

// unknownEdgeEntry is one low-confidence edge. FromID feeds directly into
// read/context/trace, the same way entrypoints' and hierarchy's ID fields
// do, so a caller doesn't need a second search call to act on a hit.
type unknownEdgeEntry struct {
	Confidence string `json:"confidence"`
	Type       string `json:"type"`
	From       string `json:"from"`
	FromID     string `json:"from_id"`
	File       string `json:"file,omitempty"`
	Line       int    `json:"line,omitempty"`
	To         string `json:"to"`
	Label      string `json:"label,omitempty"`
}

type unknownEdgesOutput struct {
	Edges        []unknownEdgeEntry `json:"edges"`
	Total        int                `json:"total"`
	ByConfidence map[string]int     `json:"by_confidence"`
	Truncated    bool               `json:"truncated,omitempty"`
}

// unknownEdges implements the MCP side of the unknown-edge audit tooling
// plan: list confidence-bearing edges at or below a threshold, fleet-wide,
// in one call — the bulk-audit case `context` on the synthetic `unresolved`
// node already covers for a single node's traversal budget, but has no way
// to ask "give me all of them." Reuses contract.FilterEdgesByConfidence, the
// same function `polyflow status --unknown-edges` calls, so the two never
// drift on what counts as "still unresolved" (a producer with a
// better-resolved edge elsewhere in the fleet-merged graph is excluded from
// both).
func (s *Server) unknownEdges(ctx context.Context, req *mcp.CallToolRequest, in unknownEdgesInput) (*mcp.CallToolResult, any, error) {
	minConfidence := in.MinConfidence
	if minConfidence == "" {
		minConfidence = graph.ConfidenceUnknown
	}
	_, idx, _ := s.snapshot()
	matched := contract.FilterEdgesByConfidence(idx, minConfidence)

	var all []unknownEdgeEntry
	byConfidence := map[string]int{}
	for _, e := range matched {
		if in.EdgeType != "" && string(e.Type) != in.EdgeType {
			continue
		}
		from := idx.Nodes[e.From]
		if in.Service != "" && (from == nil || from.Service != in.Service) {
			continue
		}
		entry := unknownEdgeEntry{Confidence: e.Confidence, Type: string(e.Type), Label: e.Label, FromID: e.From}
		if from != nil {
			entry.From = from.Label
			entry.File = from.File
			entry.Line = from.Line
		} else {
			entry.From = e.From
		}
		if to := idx.Nodes[e.To]; to != nil {
			entry.To = to.Label
		} else {
			entry.To = e.To
		}
		all = append(all, entry)
		byConfidence[e.Confidence]++
	}

	out := unknownEdgesOutput{Total: len(all), ByConfidence: byConfidence}
	limit := in.Limit
	switch {
	case limit < 0:
		out.Edges = all
	case limit == 0:
		limit = 200
		fallthrough
	default:
		if len(all) > limit {
			out.Edges = all[:limit]
			out.Truncated = true
		} else {
			out.Edges = all
		}
	}
	if out.Edges == nil {
		out.Edges = []unknownEdgeEntry{}
	}
	return jsonResult(out)
}
