package impact

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/budget"
	"github.com/lordsonvimal/polyflow/internal/graph"
)

// DefaultBudget is the token budget applied when a caller does not ask for
// one. An unbounded blast radius is a context-window hazard: on
// fleet-datascience `impact --target JobMessage` is 23.8 KB of JSON, and the
// worst function in the graph reaches 131 files. At this budget a small blast
// radius still returns full per-node detail (it fits), while a large one rolls
// up to the per-file summary instead of dumping the verbose form. Pass a
// negative max-tokens to opt back out.
const DefaultBudget = 2000

// FileRollup aggregates the blast-radius callers that landed in one file —
// the low-token-budget representation of an impact answer.
type FileRollup struct {
	File      string   `json:"file"`
	Service   string   `json:"service"`
	Nodes     int      `json:"nodes"`
	MinDepth  int      `json:"min_depth"`
	EdgeTypes []string `json:"edge_types"`
	// BestVerificationState is the highest-confidence verification state among
	// the callers in this file — used as a sort tie-breaker within equal depth.
	BestVerificationState string `json:"best_verification_state,omitempty"`
	// StructuralOnly is true when every caller landing in this file arrived
	// via a class-wide, action-blind edge (a Rails filter chain, an
	// include/extend/prepend mixin) rather than a genuine call from the
	// target's own body. Ranked after real files regardless of depth — see
	// the sort in rollupCallers.
	StructuralOnly bool `json:"structural_only,omitempty"`
}

// isStructuralEdge reports whether a caller arrived via a class-wide,
// action-blind edge: a rails_filter callback (registered once per class,
// attributed to every action) or an inherits/mixin edge. Both are real
// relationships, but not evidence that the impact target itself calls into
// the file — see docs/ruby-activerecord-association-plan.md Tier IR.
func isStructuralEdge(c Caller) bool {
	return c.EdgeType == string(graph.EdgeTypeInherits) || c.EdgeMeta["via"] == "rails_filter"
}

// Summary is the file-grouped rollup of an impact result, emitted when the
// full per-node detail exceeds the token budget (or on --summary). Entry
// points compact to "label — file:line" strings; the unresolved and
// verification_summary sections are carried whole — never trimmed.
type Summary struct {
	Target               *graph.Node           `json:"target"`
	Summary              bool                  `json:"summary"` // always true: marks the rollup shape
	Ranking              string                `json:"ranking"` // "structural,depth,verification"
	Files                []FileRollup          `json:"files"`
	EntryPoints          []string              `json:"entry_points"`
	ServicesAffected     []string              `json:"services_affected"`
	CrossServiceTriggers []CrossServiceTrigger `json:"cross_service_triggers"`
	Depth                int                   `json:"depth"`
	Direction            string                `json:"direction"`
	TotalCallers         int                   `json:"total_callers"`

	Unresolved          []graph.UnresolvedRef     `json:"unresolved"`
	UnresolvedNote      string                    `json:"unresolved_note,omitempty"`
	VerificationSummary graph.VerificationSummary `json:"verification_summary"`
	Trust               graph.TrustStamp          `json:"trust"`
	Budget              *budget.Info              `json:"budget,omitempty"`
}

// rollupCallers groups blast-radius callers by file, the low-token
// representation shared by the node-target and diff summaries.
func rollupCallers(callers []Caller) []FileRollup {
	type key struct{ service, file string }
	entries := make(map[key]*FileRollup)

	allStructural := make(map[key]bool)

	for _, c := range callers {
		k := key{c.Service, c.File}
		e, ok := entries[k]
		if !ok {
			e = &FileRollup{File: c.File, Service: c.Service, MinDepth: c.Depth}
			entries[k] = e
			allStructural[k] = true
		}
		e.Nodes++
		if c.Depth < e.MinDepth {
			e.MinDepth = c.Depth
		}
		if c.EdgeType != "" {
			e.EdgeTypes = appendUnique(e.EdgeTypes, c.EdgeType)
		}
		if graph.VerificationRank(c.VerificationState) < graph.VerificationRank(e.BestVerificationState) {
			e.BestVerificationState = c.VerificationState
		}
		if !isStructuralEdge(c) {
			allStructural[k] = false
		}
	}

	files := make([]FileRollup, 0, len(entries))
	for k, e := range entries {
		sort.Strings(e.EdgeTypes)
		e.StructuralOnly = allStructural[k]
		files = append(files, *e)
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].StructuralOnly != files[j].StructuralOnly {
			return !files[i].StructuralOnly
		}
		if files[i].MinDepth != files[j].MinDepth {
			return files[i].MinDepth < files[j].MinDepth
		}
		ri, rj := graph.VerificationRank(files[i].BestVerificationState), graph.VerificationRank(files[j].BestVerificationState)
		if ri != rj {
			return ri < rj
		}
		if files[i].File != files[j].File {
			return files[i].File < files[j].File
		}
		return files[i].Service < files[j].Service
	})
	return files
}

// maxSummaryMetaValue is the longest meta value the rollup shape carries.
// Above it the value is a serialised payload rather than a label: a 27-field
// Go struct's `fields` meta is 2.4 KB, which at the default budget is 30% of
// the whole answer spent describing the node the caller already named. Small
// metas — route path, handler, end_line — stay, because they are what makes
// the target line identifiable.
const maxSummaryMetaValue = 200

// compactTarget copies n with oversized meta values dropped. The copy matters:
// the node aliases the shared adjacency index, so mutating in place would
// corrupt every later query in a long-lived process (the MCP server).
func compactTarget(n *graph.Node) *graph.Node {
	if n == nil {
		return nil
	}
	var dropped []string
	for k, v := range n.Meta {
		if len(v) > maxSummaryMetaValue {
			dropped = append(dropped, k)
		}
	}
	if len(dropped) == 0 {
		return n
	}
	c := *n
	c.Meta = make(map[string]string, len(n.Meta))
	for k, v := range n.Meta {
		if len(v) <= maxSummaryMetaValue {
			c.Meta[k] = v
		}
	}
	sort.Strings(dropped) // map iteration must never reach output
	c.Meta["meta_omitted"] = strings.Join(dropped, ",")
	return &c
}

// Summarize rolls the per-node blast radius up into per-file entries.
func (r *Result) Summarize() *Summary {
	entryPoints := make([]string, 0, len(r.EntryPoints))
	for _, ep := range r.EntryPoints {
		entryPoints = append(entryPoints, fmt.Sprintf("%s — %s:%d", ep.Label, ep.File, ep.Line))
	}

	return &Summary{
		Target:               compactTarget(r.Target),
		Summary:              true,
		Ranking:              "structural,depth,verification",
		Files:                rollupCallers(r.Callers),
		EntryPoints:          entryPoints,
		ServicesAffected:     r.ServicesAffected,
		CrossServiceTriggers: r.CrossServiceTriggers,
		Depth:                r.Depth,
		Direction:            r.Direction,
		TotalCallers:         r.TotalCallers,
		Unresolved:           r.Unresolved,
		UnresolvedNote:       r.UnresolvedNote,
		VerificationSummary:  r.VerificationSummary,
		Trust:                r.Trust,
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
		// Trim the caveat before the answer. The unresolved list used to be
		// exempt from budgeting on the grounds that blind spots must never be
		// hidden to save tokens — sound when a budget was opt-in, but once one
		// applies by default it inverts into the caveat evicting the answer: on
		// fleet-datascience `impact --target JobMessage` came back with 1 of 25
		// files because 6 KB of unresolved refs had first claim on the budget.
		// The blind-spot SIGNAL (the count and the note) still always survives;
		// only the per-ref detail is trimmed, and it says how much it dropped.
		s.Unresolved = trimUnresolved(s.Unresolved, maxTokens, s.Budget)

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

// unresolvedReserve is the share of a token budget the answer keeps for
// itself; the unresolved list may claim at most the remainder. Three quarters,
// because the file list is what was asked for and the unresolved list is a
// caveat on it — at an even split, JobMessage still returned only 12 of its 25
// files while printing 24 unresolved refs.
const unresolvedReserve = 0.75

// trimUnresolved shrinks an unresolved detail list so it cannot claim more
// than (1-unresolvedReserve) of the budget, returning the kept prefix. The
// caller's UnresolvedNote is left describing the true total — an agent must
// not read a trimmed list as a complete one — and info records how many refs
// went. Shared by every budgeted impact shape: node, file and diff all attach
// the same ledger and all became budget-bound by default in D.3.
func trimUnresolved(refs []graph.UnresolvedRef, maxTokens int, info *budget.Info) []graph.UnresolvedRef {
	total := len(refs)
	if total == 0 {
		return refs
	}
	limit := int(float64(maxTokens) * (1 - unresolvedReserve))
	keep := budget.TrimToFit(total, limit, func(n int) int { return budget.Estimate(refs[:n]) })
	if budget.Estimate(refs[:keep]) > limit {
		// TrimToFit floors at one entry; here even that is too big.
		keep = 0
	}
	if omitted := total - keep; omitted > 0 {
		info.AppendNote(fmt.Sprintf(
			"%d of %d unresolved references omitted to fit the budget (the count above is the true total)",
			omitted, total))
	}
	return refs[:keep]
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
