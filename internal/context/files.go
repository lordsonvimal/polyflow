package context

import (
	"fmt"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// FilesResult is the file-granularity context output (`context --file` and
// the MCP context tool's files mode): the ranked files related to the seed
// files, answering "where is the code connected to X". Like
// impact.FileResult it is already a per-file rollup, so token budgeting
// trims the related list rather than switching shapes.
type FilesResult struct {
	Files   []string                 `json:"files"` // resolved seed files
	Service string                   `json:"service,omitempty"`
	Depth   int                      `json:"depth"`
	Ranking string                   `json:"ranking"` // ranking criteria: "refs,hops,verification"
	Related []graph.RelatedFileEntry `json:"related"`

	Unresolved     []graph.UnresolvedRef `json:"unresolved"`
	UnresolvedNote string                `json:"unresolved_note,omitempty"`
	Budget         *budget.Info          `json:"budget,omitempty"`
}

// BuildFiles ranks the files related to the given seed paths. It errors when
// any path has no nodes in the index — a silently ignored seed would make
// the answer look complete when it is not. limit > 0 caps the ranked list.
func BuildFiles(idx *graph.AdjacencyIndex, service string, paths []string, depth, limit int) (*FilesResult, error) {
	seeds, related, missing := graph.RelatedFiles(idx, service, paths, depth)
	if len(missing) > 0 {
		return nil, fmt.Errorf("file(s) not found in index: %s", strings.Join(missing, ", "))
	}
	if limit > 0 && len(related) > limit {
		related = related[:limit]
	}
	return &FilesResult{
		Files:      seeds,
		Service:    service,
		Depth:      depth,
		Ranking:    "refs,hops,verification",
		Related:    related,
		Unresolved: []graph.UnresolvedRef{},
	}, nil
}

// AttachUnresolved scopes the workspace's unresolved-reference ledger to the
// seed and related files and records the matches on the result.
func (r *FilesResult) AttachUnresolved(refs []graph.UnresolvedRef) {
	files := make(map[string]bool, len(r.Files)+len(r.Related))
	for _, f := range r.Files {
		files[f] = true
	}
	for _, e := range r.Related {
		files[e.File] = true
	}
	r.Unresolved = graph.UnresolvedInFiles(refs, files)
	r.UnresolvedNote = graph.UnresolvedNote(len(r.Unresolved))
}

// ApplyBudget trims the related list to fit maxTokens (<= 0 means unlimited:
// no-op). The unresolved section is carried whole — blind spots are never
// trimmed to save tokens.
func (r *FilesResult) ApplyBudget(maxTokens int) {
	if maxTokens <= 0 {
		return
	}
	if est := budget.Estimate(r); est <= maxTokens {
		r.Budget = &budget.Info{MaxTokens: maxTokens, EstimatedTokens: est, Level: budget.LevelSummary}
		return
	}
	all := r.Related
	keep := budget.TrimToFit(len(all), maxTokens, func(n int) int {
		r.Related = all[:n]
		return budget.Estimate(r)
	})
	r.Related = all[:keep]
	r.Budget = &budget.Info{MaxTokens: maxTokens, Level: budget.LevelSummary}
	if omitted := len(all) - keep; omitted > 0 {
		r.Budget.OmittedFiles = omitted
		r.Budget.AppendNote(fmt.Sprintf("%d more files omitted to fit the budget", omitted))
	}
	r.Budget.EstimatedTokens = budget.Estimate(r)
}
