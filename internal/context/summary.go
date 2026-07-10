package context

import (
	"fmt"
	"sort"

	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// FileRollup aggregates the traversal nodes that landed in one file — the
// low-token-budget representation of a context answer.
type FileRollup struct {
	File      string   `json:"file"`
	Service   string   `json:"service"`
	Direction string   `json:"direction"` // upstream, downstream, or both
	Nodes     int      `json:"nodes"`
	MinDepth  int      `json:"min_depth"`
	EdgeTypes []string `json:"edge_types"`
}

// Summary is the file-grouped rollup of a context result, emitted when the
// full per-node detail exceeds the token budget (or on --summary). The
// unresolved section is carried whole — blind spots are never trimmed to
// save tokens.
type Summary struct {
	Target       *graph.Node  `json:"target"`
	Task         string       `json:"task"`
	Summary      bool         `json:"summary"` // always true: marks the rollup shape
	Files        []FileRollup `json:"files"`
	CrossService []CrossEdge  `json:"cross_service"`
	Depth        int          `json:"depth"`
	TotalNodes   int          `json:"total_nodes"`
	TotalEdges   int          `json:"total_edges"`

	Unresolved     []graph.UnresolvedRef `json:"unresolved"`
	UnresolvedNote string                `json:"unresolved_note,omitempty"`
	Budget         *budget.Info          `json:"budget,omitempty"`
}

// Summarize rolls the per-node traversal detail up into per-file entries.
func (r *Result) Summarize() *Summary {
	type key struct{ service, file string }
	entries := make(map[key]*FileRollup)
	seen := make(map[key]map[string]bool)

	add := func(nodes []TraceNode, direction string) {
		for _, n := range nodes {
			k := key{n.Service, n.File}
			e, ok := entries[k]
			if !ok {
				e = &FileRollup{File: n.File, Service: n.Service, Direction: direction, MinDepth: n.Depth}
				entries[k] = e
				seen[k] = make(map[string]bool)
			}
			if e.Direction != direction {
				e.Direction = "both"
			}
			if !seen[k][n.ID] {
				seen[k][n.ID] = true
				e.Nodes++
			}
			if n.Depth < e.MinDepth {
				e.MinDepth = n.Depth
			}
			if n.EdgeType != "" {
				e.EdgeTypes = appendUnique(e.EdgeTypes, n.EdgeType)
			}
		}
	}
	add(r.Upstream, "upstream")
	add(r.Downstream, "downstream")

	files := make([]FileRollup, 0, len(entries))
	for _, e := range entries {
		sort.Strings(e.EdgeTypes)
		files = append(files, *e)
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].MinDepth != files[j].MinDepth {
			return files[i].MinDepth < files[j].MinDepth
		}
		if files[i].File != files[j].File {
			return files[i].File < files[j].File
		}
		return files[i].Service < files[j].Service
	})

	return &Summary{
		Target:         r.Target,
		Task:           r.Task,
		Summary:        true,
		Files:          files,
		CrossService:   r.CrossService,
		Depth:          r.Depth,
		TotalNodes:     r.TotalNodes,
		TotalEdges:     r.TotalEdges,
		Unresolved:     r.Unresolved,
		UnresolvedNote: r.UnresolvedNote,
	}
}

// ApplyBudget picks the output shape for a token budget: the result itself
// when it fits maxTokens (or no budget is set), otherwise the file-grouped
// summary, trimmed further if even the rollup is over budget. forceSummary
// skips the detail attempt entirely.
func (r *Result) ApplyBudget(maxTokens int, forceSummary bool) any {
	if maxTokens <= 0 && !forceSummary {
		return r
	}
	if !forceSummary {
		if est := budget.Estimate(r); est <= maxTokens {
			r.Budget = &budget.Info{MaxTokens: maxTokens, EstimatedTokens: est, Level: budget.LevelDetail}
			return r
		}
	}
	s := r.Summarize()
	s.Budget = &budget.Info{MaxTokens: maxTokens, Level: budget.LevelSummary}
	if !forceSummary {
		s.Budget.AppendNote("full per-node detail exceeds the token budget; rolled up per file")
	}
	if maxTokens > 0 {
		all := s.Files
		keep := budget.TrimToFit(len(all), maxTokens, func(n int) int {
			s.Files = all[:n]
			return budget.Estimate(s)
		})
		s.Files = all[:keep]
		if omitted := len(all) - keep; omitted > 0 {
			s.Budget.OmittedFiles = omitted
			s.Budget.AppendNote(fmt.Sprintf("%d more files omitted to fit the budget", omitted))
		}
	}
	s.Budget.EstimatedTokens = budget.Estimate(s)
	return s
}

// InlineSnippets attaches source snippets (lines from each node's declaration
// line) so the agent can read signatures without extra file round-trips.
// Target is copied first: it aliases the shared adjacency index.
func (r *Result) InlineSnippets(root string, lines int) {
	if lines <= 0 {
		return
	}
	if r.Target != nil {
		t := *r.Target
		t.Snippet = budget.Snippet(root, t.File, t.Line, lines)
		r.Target = &t
	}
	for i := range r.Upstream {
		r.Upstream[i].Snippet = budget.Snippet(root, r.Upstream[i].File, r.Upstream[i].Line, lines)
	}
	for i := range r.Downstream {
		r.Downstream[i].Snippet = budget.Snippet(root, r.Downstream[i].File, r.Downstream[i].Line, lines)
	}
}

func appendUnique(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}
