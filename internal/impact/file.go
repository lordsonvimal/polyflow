package impact

import (
	"fmt"

	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// FileResult is the file-granularity impact output (`impact --file` and the
// MCP impact tool's file mode). It is already a per-file rollup, so token
// budgeting trims the impacted list rather than switching shapes.
type FileResult struct {
	File      string                  `json:"file"`
	Service   string                  `json:"service"`
	Direction string                  `json:"direction"`
	Depth     int                     `json:"depth"`
	Impacted  []graph.FileImpactEntry `json:"impacted"`

	Unresolved     []graph.UnresolvedRef `json:"unresolved"`
	UnresolvedNote string                `json:"unresolved_note,omitempty"`
	Budget         *budget.Info          `json:"budget,omitempty"`
}

// BuildFile computes the file-granularity blast radius of path under opts
// (Depth, Service, Direction and Policy; the rest do not apply at file
// granularity). It errors when the file has no nodes in the index.
func BuildFile(idx *graph.AdjacencyIndex, path string, opts Options) (*FileResult, error) {
	seeds := graph.NodesInFile(idx, opts.Service, path)
	if len(seeds) == 0 {
		return nil, fmt.Errorf("file not found in index: %s", path)
	}
	dir := direction(opts.Direction)
	return &FileResult{
		File:       seeds[0].File,
		Service:    seeds[0].Service,
		Direction:  dir,
		Depth:      opts.Depth,
		Impacted:   graph.FileImpactWithPolicy(idx, opts.Service, path, dir, opts.Depth, opts.Policy),
		Unresolved: []graph.UnresolvedRef{},
	}, nil
}

// AttachUnresolved scopes the workspace's unresolved-reference ledger to the
// files touched by this traversal and records the matches on the result.
func (r *FileResult) AttachUnresolved(refs []graph.UnresolvedRef) {
	files := make(map[string]bool, len(r.Impacted)+1)
	files[r.File] = true
	for _, e := range r.Impacted {
		files[e.File] = true
	}
	r.Unresolved = graph.UnresolvedInFiles(refs, files)
	r.UnresolvedNote = graph.UnresolvedNote(len(r.Unresolved))
}

// ApplyBudget trims the impacted list to fit maxTokens (<= 0 means
// unlimited: no-op). The unresolved list is trimmed first — see
// Summary.trimUnresolvedToFit for why the caveat yields to the answer — but
// the blind-spot count in UnresolvedNote always survives.
func (r *FileResult) ApplyBudget(maxTokens int) {
	if maxTokens <= 0 {
		return
	}
	if est := budget.Estimate(r); est <= maxTokens {
		r.Budget = &budget.Info{MaxTokens: maxTokens, EstimatedTokens: est, Level: budget.LevelSummary}
		return
	}
	r.Budget = &budget.Info{MaxTokens: maxTokens, Level: budget.LevelSummary}
	r.Unresolved = trimUnresolved(r.Unresolved, maxTokens, r.Budget)
	all := r.Impacted
	keep := budget.TrimToFit(len(all), maxTokens, func(n int) int {
		r.Impacted = all[:n]
		return budget.Estimate(r)
	})
	r.Impacted = all[:keep]
	if omitted := len(all) - keep; omitted > 0 {
		r.Budget.OmittedFiles = omitted
		r.Budget.AppendNote(fmt.Sprintf("%d more files omitted to fit the budget", omitted))
	}
	r.Budget.EstimatedTokens = budget.Estimate(r)
}
