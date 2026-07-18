package impact

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/gitdiff"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// DiffTarget is a changed node: a graph node whose declaration or body span
// overlaps a diff hunk.
type DiffTarget struct {
	Node  *graph.Node    `json:"node"`
	Spans []gitdiff.Span `json:"changed_spans"`
}

// UnmappedHunk is a changed span the graph has no node for. Reported, never
// dropped: the blast radius may be under-reported where these appear.
type UnmappedHunk struct {
	File   string        `json:"file"`
	Span   *gitdiff.Span `json:"span,omitempty"` // nil for whole-file gaps (deleted files)
	Reason string        `json:"reason"`
}

// DiffResult is the union blast radius of a set of uncommitted changes:
// every changed node's ancestors merged, each kept at its minimum depth.
// Changed nodes themselves appear only in Targets, not in Callers.
type DiffResult struct {
	Mode                 string                `json:"mode"` // worktree | staged
	Depth                int                   `json:"depth"`
	ChangedFiles         int                   `json:"changed_files"`
	Targets              []DiffTarget          `json:"targets"`
	Unmapped             []UnmappedHunk        `json:"unmapped_hunks"`
	Callers              []Caller              `json:"callers"`
	EntryPoints          []*graph.Node         `json:"entry_points"`
	ServicesAffected     []string              `json:"services_affected"`
	CrossServiceTriggers []CrossServiceTrigger `json:"cross_service_triggers"`
	TotalCallers         int                   `json:"total_callers"`

	Unresolved          []graph.UnresolvedRef        `json:"unresolved"`
	UnresolvedNote      string                       `json:"unresolved_note,omitempty"`
	VerificationSummary graph.VerificationSummary    `json:"verification_summary"`
	Budget              *budget.Info                 `json:"budget,omitempty"`
}

// BuildDiff maps changed spans to graph nodes and computes their union blast
// radius: ancestors of every changed node merged at minimum depth (<= 0 depth
// means unlimited), optionally filtered to one service. Spans that map to no
// node are recorded in Unmapped — never silently dropped. verboseSources
// controls whether per-caller Sources uses compact or full SourceRef structs.
func BuildDiff(idx *graph.AdjacencyIndex, changes []gitdiff.FileChange, depth int, service string, verboseSources bool) *DiffResult {
	r := &DiffResult{
		Mode:         "worktree",
		Depth:        depth,
		ChangedFiles: len(changes),
		Targets:      []DiffTarget{},
		Unmapped:     []UnmappedHunk{},
		Unresolved:   []graph.UnresolvedRef{},
	}

	seedIdx := make(map[string]int) // node ID → index in r.Targets
	for _, ch := range changes {
		if ch.Deleted {
			r.Unmapped = append(r.Unmapped, UnmappedHunk{
				File:   ch.Path,
				Reason: "file deleted — impact on former callers surfaces as unresolved references after reindex",
			})
			continue
		}
		nodes := graph.NodesInFile(idx, "", ch.Path)
		for _, s := range ch.Spans {
			hits := nodesInSpan(nodes, s)
			if len(hits) == 0 {
				sp := s
				reason := "no node overlaps this span"
				if len(nodes) == 0 {
					reason = "file has no nodes in the graph"
				}
				r.Unmapped = append(r.Unmapped, UnmappedHunk{File: ch.Path, Span: &sp, Reason: reason})
				continue
			}
			for _, n := range hits {
				j, ok := seedIdx[n.ID]
				if !ok {
					j = len(r.Targets)
					seedIdx[n.ID] = j
					r.Targets = append(r.Targets, DiffTarget{Node: n})
				}
				r.Targets[j].Spans = append(r.Targets[j].Spans, s)
			}
		}
	}

	// Union the per-seed blast radii, keeping each node at its minimum depth.
	// Seeds are excluded: a changed node reached from another changed node is
	// already listed as a target.
	best := make(map[string]graph.TraversalResult)
	for _, t := range r.Targets {
		for _, a := range graph.Ancestors(idx, t.Node.ID, depth) {
			if _, isSeed := seedIdx[a.Node.ID]; isSeed {
				continue
			}
			if prev, ok := best[a.Node.ID]; !ok || a.Depth < prev.Depth {
				best[a.Node.ID] = a
			}
		}
	}
	ancestors := make([]graph.TraversalResult, 0, len(best))
	for _, a := range best {
		if service != "" && a.Node.Service != service {
			continue
		}
		ancestors = append(ancestors, a)
	}
	sort.Slice(ancestors, func(i, j int) bool {
		if ancestors[i].Depth != ancestors[j].Depth {
			return ancestors[i].Depth < ancestors[j].Depth
		}
		if ancestors[i].Node.File != ancestors[j].Node.File {
			return ancestors[i].Node.File < ancestors[j].Node.File
		}
		return ancestors[i].Node.ID < ancestors[j].Node.ID
	})

	callers, entryPoints, services, triggers, edges := assemble(idx, ancestors, verboseSources)

	// The changed nodes' own services are affected even with zero callers.
	svcSet := make(map[string]bool, len(services))
	for _, s := range services {
		svcSet[s] = true
	}
	for _, t := range r.Targets {
		svcSet[t.Node.Service] = true
	}
	services = services[:0]
	for s := range svcSet {
		services = append(services, s)
	}
	sort.Strings(services)

	r.Callers = callers
	r.EntryPoints = entryPoints
	r.ServicesAffected = services
	r.CrossServiceTriggers = triggers
	r.TotalCallers = len(callers)
	r.VerificationSummary = graph.BuildVerificationSummary(edges)
	return r
}

// nodesInSpan returns the nodes whose declaration span overlaps s: the
// declaration line itself for point nodes (variables, call sites), the whole
// body for nodes carrying end_line meta. When nothing overlaps, it falls back
// to the innermost open-ended scope (function/method/worker with no recorded
// end_line starting before the span) — matcher parity: an unknown span is
// treated as open-ended rather than dropped.
func nodesInSpan(nodes []*graph.Node, s gitdiff.Span) []*graph.Node {
	var hits []*graph.Node
	for _, n := range nodes {
		end := n.Line
		if v, ok := n.Meta["end_line"]; ok {
			if e, err := strconv.Atoi(v); err == nil && e > end {
				end = e
			}
		}
		if n.Line <= s.End && s.Start <= end {
			hits = append(hits, n)
		}
	}
	if len(hits) > 0 {
		return hits
	}

	var enclosing *graph.Node
	for _, n := range nodes {
		switch n.Type {
		case graph.NodeTypeFunction, graph.NodeTypeMethod, graph.NodeTypeWorker:
		default:
			continue
		}
		if _, ok := n.Meta["end_line"]; ok {
			continue // known span already checked above; it does not contain s
		}
		if n.Line > s.Start {
			continue
		}
		if enclosing == nil || n.Line > enclosing.Line {
			enclosing = n
		}
	}
	if enclosing != nil {
		return []*graph.Node{enclosing}
	}
	return nil
}

// AttachUnresolved scopes the workspace's unresolved-reference ledger to the
// files touched by this query: changed files (mapped or not) and every caller
// file in the blast radius.
func (r *DiffResult) AttachUnresolved(refs []graph.UnresolvedRef) {
	files := make(map[string]bool, len(r.Callers)+len(r.Targets))
	for _, t := range r.Targets {
		files[t.Node.File] = true
	}
	for _, u := range r.Unmapped {
		files[u.File] = true
	}
	for _, c := range r.Callers {
		files[c.File] = true
	}
	r.Unresolved = graph.UnresolvedInFiles(refs, files)
	r.UnresolvedNote = graph.UnresolvedNote(len(r.Unresolved))
}

// InlineSnippets attaches source snippets to targets, callers and entry
// points. Target and entry-point nodes are copied first: they alias the
// shared adjacency index.
func (r *DiffResult) InlineSnippets(root string, lines int) {
	if lines <= 0 {
		return
	}
	for i, t := range r.Targets {
		n := *t.Node
		n.Snippet = budget.Snippet(root, n.File, n.Line, lines)
		r.Targets[i].Node = &n
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

// DiffSummary is the file-grouped rollup of a diff impact result. Targets
// and entry points compact to "label — file:line" strings; the unmapped,
// unresolved, and verification_summary sections are carried whole — never trimmed.
type DiffSummary struct {
	Mode                 string                `json:"mode"`
	Summary              bool                  `json:"summary"` // always true: marks the rollup shape
	ChangedFiles         int                   `json:"changed_files"`
	Targets              []string              `json:"targets"`
	Unmapped             []UnmappedHunk        `json:"unmapped_hunks"`
	Files                []FileRollup          `json:"files"`
	EntryPoints          []string              `json:"entry_points"`
	ServicesAffected     []string              `json:"services_affected"`
	CrossServiceTriggers []CrossServiceTrigger `json:"cross_service_triggers"`
	Depth                int                   `json:"depth"`
	TotalCallers         int                   `json:"total_callers"`

	Unresolved          []graph.UnresolvedRef        `json:"unresolved"`
	UnresolvedNote      string                       `json:"unresolved_note,omitempty"`
	VerificationSummary graph.VerificationSummary    `json:"verification_summary"`
	Budget              *budget.Info                 `json:"budget,omitempty"`
}

// Summarize rolls the per-node blast radius up into per-file entries.
func (r *DiffResult) Summarize() *DiffSummary {
	targets := make([]string, 0, len(r.Targets))
	for _, t := range r.Targets {
		targets = append(targets, fmt.Sprintf("%s — %s:%d", t.Node.Label, t.Node.File, t.Node.Line))
	}
	entryPoints := make([]string, 0, len(r.EntryPoints))
	for _, ep := range r.EntryPoints {
		entryPoints = append(entryPoints, fmt.Sprintf("%s — %s:%d", ep.Label, ep.File, ep.Line))
	}

	return &DiffSummary{
		Mode:                 r.Mode,
		Summary:              true,
		ChangedFiles:         r.ChangedFiles,
		Targets:              targets,
		Unmapped:             r.Unmapped,
		Files:                rollupCallers(r.Callers),
		EntryPoints:          entryPoints,
		ServicesAffected:     r.ServicesAffected,
		CrossServiceTriggers: r.CrossServiceTriggers,
		Depth:                r.Depth,
		TotalCallers:         r.TotalCallers,
		Unresolved:           r.Unresolved,
		UnresolvedNote:       r.UnresolvedNote,
		VerificationSummary:  r.VerificationSummary,
	}
}

// ApplyBudget picks the output shape for a token budget: the result itself
// when it fits maxTokens (or no budget is set), otherwise the file-grouped
// summary, trimmed further if even the rollup is over budget. forceSummary
// skips the detail attempt entirely.
func (r *DiffResult) ApplyBudget(maxTokens int, forceSummary bool) any {
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
