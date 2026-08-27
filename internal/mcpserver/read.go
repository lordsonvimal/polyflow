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
	Target        string   `json:"target,omitempty" jsonschema:"node id (from search/hierarchy/context/resolve) or a partial name/label to resolve. Omit when using targets for a batch read"`
	Targets       []string `json:"targets,omitempty" jsonschema:"read multiple symbols/files in one call instead of one read per target — pass node ids or names here (up to 20). target_service/target_type/max_lines apply to every entry. A per-target failure (e.g. not found) is reported in that entry's error field, not as a call failure, so the rest of the batch still comes back"`
	TargetService string   `json:"target_service,omitempty" jsonschema:"restrict a name resolution to this service"`
	TargetType    string   `json:"target_type,omitempty" jsonschema:"restrict a name resolution to this node type"`
	MaxLines      int      `json:"max_lines,omitempty" jsonschema:"safety cap on lines returned per target (default 200)"`
}

// maxBatchTargets caps a single batch read call — large fan-outs belong in
// separate calls so one bad request can't blow the response's token budget.
const maxBatchTargets = 20

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
	// Requested echoes the batch entry's input target so a caller can match
	// results back to requests without relying on response order.
	Requested string `json:"requested,omitempty"`
	// Error is set instead of the above fields when this one entry of a
	// batch failed to resolve — a single bad target must not fail the whole
	// call and hide the other, valid, results.
	Error string `json:"error,omitempty"`
}

type readBatchOutput struct {
	Results []readOutput `json:"results"`
}

// read returns the EXACT source span of a symbol by node id (or a resolved
// name), using meta["end_line"] when present and a bounded window otherwise —
// so an agent reads one function/struct instead of re-opening the whole file.
// Passing targets instead of target reads several symbols in one call.
func (s *Server) read(ctx context.Context, req *mcp.CallToolRequest, in readInput) (*mcp.CallToolResult, any, error) {
	if len(in.Targets) > 0 {
		if in.Target != "" {
			return nil, nil, fmt.Errorf("pass either target or targets, not both")
		}
		if len(in.Targets) > maxBatchTargets {
			return nil, nil, fmt.Errorf("targets: at most %d per call, got %d", maxBatchTargets, len(in.Targets))
		}
		store, idx, _ := s.snapshot()
		results := make([]readOutput, len(in.Targets))
		for i, t := range in.Targets {
			out, err := readOne(ctx, store, idx, t, in.TargetService, in.TargetType, in.MaxLines)
			if err != nil {
				out = readOutput{Error: err.Error()}
			}
			out.Requested = t
			results[i] = out
		}
		return jsonResult(readBatchOutput{Results: results})
	}

	if in.Target == "" {
		return nil, nil, fmt.Errorf("target is required")
	}
	store, idx, _ := s.snapshot()
	out, err := readOne(ctx, store, idx, in.Target, in.TargetService, in.TargetType, in.MaxLines)
	if err != nil {
		return nil, nil, err
	}
	return jsonResult(out)
}

// readOne resolves and reads a single target — the shared body behind both
// the single-target and batch paths of read.
func readOne(ctx context.Context, store Store, idx *graph.AdjacencyIndex, target, targetService, targetType string, maxLines int) (readOutput, error) {
	if maxLines <= 0 {
		maxLines = 200
	}

	// Direct id hit first (the common case: search/hierarchy handed us an id);
	// otherwise fall back to the same name-resolution the other tools use.
	n := idx.Nodes[target]
	resolutionNote := ""
	if n == nil {
		resolved, candidates, exactMatch, err := resolveNode(ctx, store, idx, target, targetService, targetType)
		if err != nil {
			return readOutput{}, err
		}
		if resolved == nil {
			return readOutput{TargetCandidates: candidates}, nil
		}
		resolutionNote = graph.ResolutionNote(target, exactMatch)
		n = resolved
		// Prefer the indexed copy (carries Meta) when the resolver returned a
		// store row.
		if full := idx.Nodes[resolved.ID]; full != nil {
			n = full
		}
	}

	if n.Line <= 0 || n.File == "" {
		return readOutput{}, fmt.Errorf("node %s has no source location to read", n.ID)
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

	return readOutput{
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
	}, nil
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
