package mcpserver

import (
	"context"
	"fmt"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

type readInput struct {
	Target        string `json:"target" jsonschema:"node id (from search/hierarchy/context/resolve) or a partial name/label to resolve"`
	TargetService string `json:"target_service,omitempty" jsonschema:"restrict a name resolution to this service"`
	TargetType    string `json:"target_type,omitempty" jsonschema:"restrict a name resolution to this node type"`
	MaxLines      int    `json:"max_lines,omitempty" jsonschema:"safety cap on lines returned (default 200)"`
}

type readOutput struct {
	ID               string                  `json:"id,omitempty"`
	Type             string                  `json:"type,omitempty"`
	Label            string                  `json:"label,omitempty"`
	Service          string                  `json:"service,omitempty"`
	File             string                  `json:"file"`
	StartLine        int                     `json:"start_line"`
	EndLine          int                     `json:"end_line"`
	Source           string                  `json:"source"`
	SpanKnown        bool                    `json:"span_known"`
	Truncated        bool                    `json:"truncated,omitempty"`
	TargetCandidates []graph.TargetCandidate `json:"target_candidates,omitempty"`
	// ResolutionNote is set when Target came from a full-text-search guess
	// rather than a confirmed exact-label match — see graph.ResolutionNote.
	// The source below may not be the symbol the caller meant.
	ResolutionNote string `json:"resolution_note,omitempty"`
}

// read returns the EXACT source span of a symbol by node id (or a resolved
// name), using meta["end_line"] when present and a bounded window otherwise —
// so an agent reads one function/struct instead of re-opening the whole file.
func (s *Server) read(ctx context.Context, req *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, any, error) {
	if in.Target == "" {
		return nil, nil, fmt.Errorf("target is required")
	}
	maxLines := in.MaxLines
	if maxLines <= 0 {
		maxLines = 200
	}

	store, idx, _ := s.snapshot()

	// Direct id hit first (the common case: search/hierarchy handed us an id);
	// otherwise fall back to the same name-resolution the other tools use.
	n := idx.Nodes[in.Target]
	resolutionNote := ""
	if n == nil {
		resolved, candidates, exactMatch, err := resolveNode(ctx, store, in.Target, in.TargetService, in.TargetType)
		if err != nil {
			return nil, nil, err
		}
		if resolved == nil {
			return jsonResult(readOutput{TargetCandidates: candidates})
		}
		resolutionNote = graph.ResolutionNote(in.Target, exactMatch)
		n = resolved
		// Prefer the indexed copy (carries Meta) when the resolver returned a
		// store row.
		if full := idx.Nodes[resolved.ID]; full != nil {
			n = full
		}
	}

	if n.Line <= 0 || n.File == "" {
		return nil, nil, fmt.Errorf("node %s has no source location to read", n.ID)
	}

	end := 0
	spanKnown := false
	if v, ok := n.Meta["end_line"]; ok {
		if e, err := strconv.Atoi(v); err == nil && e >= n.Line {
			end = e
			spanKnown = true
		}
	}

	src, truncated := budget.SnippetSpan(".", n.File, n.Line, end, maxLines)
	resolvedEnd := end
	if resolvedEnd < n.Line {
		// Window fallback: report the actual last line returned.
		resolvedEnd = n.Line + countLines(src) - 1
	}

	return jsonResult(readOutput{
		ID:             n.ID,
		Type:           string(n.Type),
		Label:          n.Label,
		Service:        n.Service,
		File:           n.File,
		StartLine:      n.Line,
		EndLine:        resolvedEnd,
		Source:         src,
		SpanKnown:      spanKnown,
		Truncated:      truncated,
		ResolutionNote: resolutionNote,
	})
}

// countLines returns the number of newline-separated lines in s (0 for empty).
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := 1
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			n++
		}
	}
	return n
}
