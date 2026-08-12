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
// unresolved and verification_summary sections are carried whole — never trimmed.
type Summary struct {
	Target       *graph.Node  `json:"target"`
	Task         string       `json:"task"`
	Summary      bool         `json:"summary"` // always true: marks the rollup shape
	Files        []FileRollup `json:"files"`
	CrossService []CrossEdge  `json:"cross_service"`
	Depth        int          `json:"depth"`
	TotalNodes   int          `json:"total_nodes"`
	TotalEdges   int          `json:"total_edges"`

	Unresolved          []graph.UnresolvedRef     `json:"unresolved"`
	UnresolvedNote      string                    `json:"unresolved_note,omitempty"`
	VerificationSummary graph.VerificationSummary `json:"verification_summary"`
	Trust               graph.TrustStamp          `json:"trust"`
	Budget              *budget.Info              `json:"budget,omitempty"`
}

// fileBackfillReserve is the share of a token budget set aside so the
// backfill pass in ApplyBudget has room to admit a cheap, deeper file
// instead of starving on whatever incidental slack the prefix cut left
// behind. Mirrors impact.fileBackfillReserve.
const fileBackfillReserve = 0.10

// sortFileRollups orders a rollup by relevance: a real file path before a
// pathless synthetic entry (a channel/topic node — an agent has nowhere to
// Read it), then shallowest reach, then name. The path check sits above
// depth: a pathless entry one hop away is still not something an agent can
// open, so it must not out-rank an actual file merely for being closer.
// Shared by Summarize (so the initial cut already prefers real files) and by
// ApplyBudget's backfill pass, which must restore this order after splicing
// out-of-order tail entries back in. Mirrors impact.sortFileRollups — see
// its comment for why the path check exists at all (the same duplicated
// truncation logic, and the same bug, lived in both packages independently).
func sortFileRollups(files []FileRollup) {
	sort.Slice(files, func(i, j int) bool {
		pi, pj := files[i].File == "", files[j].File == ""
		if pi != pj {
			return !pi
		}
		if files[i].MinDepth != files[j].MinDepth {
			return files[i].MinDepth < files[j].MinDepth
		}
		if files[i].File != files[j].File {
			return files[i].File < files[j].File
		}
		return files[i].Service < files[j].Service
	})
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
	sortFileRollups(files)

	return &Summary{
		Target:              r.Target,
		Task:                r.Task,
		Summary:             true,
		Files:               files,
		CrossService:        r.CrossService,
		Depth:               r.Depth,
		TotalNodes:          r.TotalNodes,
		TotalEdges:          r.TotalEdges,
		Unresolved:          r.Unresolved,
		UnresolvedNote:      r.UnresolvedNote,
		VerificationSummary: r.VerificationSummary,
		Trust:               r.Trust,
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
		// Reserve headroom for the backfill pass before the prefix cut runs
		// — TrimToFit's binary search picks the largest prefix that fits,
		// which by construction leaves only incidental slack. See
		// impact.fileBackfillReserve for the full rationale (same bug, same
		// fix, independently duplicated code).
		cutBudget := int(float64(maxTokens) * (1 - fileBackfillReserve))
		keep := budget.TrimToFit(len(all), cutBudget, func(n int) int {
			s.Files = all[:n]
			return budget.Estimate(s)
		})
		s.Files = all[:keep]
		used := budget.Estimate(s)

		admitted := budget.Backfill(len(all), keep, maxTokens, used, func(i int) int {
			return budget.Estimate(all[i])
		})
		for _, i := range admitted {
			s.Files = append(s.Files, all[i])
		}
		if len(admitted) > 0 {
			sortFileRollups(s.Files)
		}

		if omitted := len(all) - len(s.Files); omitted > 0 {
			s.Budget.OmittedFiles = omitted
			note := fmt.Sprintf("%d more files omitted to fit the budget", omitted)
			if len(admitted) > 0 {
				note = fmt.Sprintf("%s (%d cheap file(s) admitted out of depth order to use leftover budget)", note, len(admitted))
			}
			s.Budget.AppendNote(note)
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
