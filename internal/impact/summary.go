package impact

import (
	"fmt"
	"sort"

	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// FileRollup aggregates the blast-radius callers that landed in one file —
// the low-token-budget representation of an impact answer.
type FileRollup struct {
	File      string   `json:"file"`
	Service   string   `json:"service"`
	Nodes     int      `json:"nodes"`
	MinDepth  int      `json:"min_depth"`
	EdgeTypes []string `json:"edge_types"`
}

// Summary is the file-grouped rollup of an impact result, emitted when the
// full per-node detail exceeds the token budget (or on --summary). Entry
// points compact to "label — file:line" strings; the unresolved section is
// carried whole — blind spots are never trimmed to save tokens.
type Summary struct {
	Target               *graph.Node           `json:"target"`
	Summary              bool                  `json:"summary"` // always true: marks the rollup shape
	Files                []FileRollup          `json:"files"`
	EntryPoints          []string              `json:"entry_points"`
	ServicesAffected     []string              `json:"services_affected"`
	CrossServiceTriggers []CrossServiceTrigger `json:"cross_service_triggers"`
	Depth                int                   `json:"depth"`
	TotalCallers         int                   `json:"total_callers"`

	Unresolved     []graph.UnresolvedRef `json:"unresolved"`
	UnresolvedNote string                `json:"unresolved_note,omitempty"`
	Budget         *budget.Info          `json:"budget,omitempty"`
}

// rollupCallers groups blast-radius callers by file, the low-token
// representation shared by the node-target and diff summaries.
func rollupCallers(callers []Caller) []FileRollup {
	type key struct{ service, file string }
	entries := make(map[key]*FileRollup)

	for _, c := range callers {
		k := key{c.Service, c.File}
		e, ok := entries[k]
		if !ok {
			e = &FileRollup{File: c.File, Service: c.Service, MinDepth: c.Depth}
			entries[k] = e
		}
		e.Nodes++
		if c.Depth < e.MinDepth {
			e.MinDepth = c.Depth
		}
		if c.EdgeType != "" {
			e.EdgeTypes = appendUnique(e.EdgeTypes, c.EdgeType)
		}
	}

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
	return files
}

// Summarize rolls the per-node blast radius up into per-file entries.
func (r *Result) Summarize() *Summary {
	entryPoints := make([]string, 0, len(r.EntryPoints))
	for _, ep := range r.EntryPoints {
		entryPoints = append(entryPoints, fmt.Sprintf("%s — %s:%d", ep.Label, ep.File, ep.Line))
	}

	return &Summary{
		Target:               r.Target,
		Summary:              true,
		Files:                rollupCallers(r.Callers),
		EntryPoints:          entryPoints,
		ServicesAffected:     r.ServicesAffected,
		CrossServiceTriggers: r.CrossServiceTriggers,
		Depth:                r.Depth,
		TotalCallers:         r.TotalCallers,
		Unresolved:           r.Unresolved,
		UnresolvedNote:       r.UnresolvedNote,
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
// Target and entry points are copied first: they alias the shared adjacency
// index.
func (r *Result) InlineSnippets(root string, lines int) {
	if lines <= 0 {
		return
	}
	if r.Target != nil {
		t := *r.Target
		t.Snippet = budget.Snippet(root, t.File, t.Line, lines)
		r.Target = &t
	}
	for i := range r.Callers {
		r.Callers[i].Snippet = budget.Snippet(root, r.Callers[i].File, r.Callers[i].Line, lines)
	}
	for i, ep := range r.EntryPoints {
		n := *ep
		n.Snippet = budget.Snippet(root, n.File, n.Line, lines)
		r.EntryPoints[i] = &n
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
