// Package budget sizes query results against an agent's token budget: full
// per-node detail when it fits, file-grouped rollups when it does not, and
// optional source-snippet inlining so the agent skips Read round-trips for
// signatures. Estimation is a heuristic (~4 bytes of JSON per token) — good
// enough to pick an output shape, not an exact meter.
package budget

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Output shape levels recorded in Info.Level.
const (
	LevelDetail  = "detail"
	LevelSummary = "summary"
)

// bytesPerToken is the JSON-to-token heuristic ratio.
const bytesPerToken = 4

// Estimate approximates the token cost of v's JSON encoding.
func Estimate(v any) int {
	data, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return (len(data) + bytesPerToken - 1) / bytesPerToken
}

// Info records the budgeting decision on emitted output so the agent knows
// whether it received full detail or a rollup, and what was cut to fit.
type Info struct {
	MaxTokens       int    `json:"max_tokens,omitempty"`
	EstimatedTokens int    `json:"estimated_tokens"`
	Level           string `json:"level"`
	Note            string `json:"note,omitempty"`
	OmittedFiles    int    `json:"omitted_files,omitempty"`
}

// AppendNote adds a sentence to the info's note, separating with "; ".
func (i *Info) AppendNote(note string) {
	if i.Note == "" {
		i.Note = note
		return
	}
	i.Note += "; " + note
}

// TrimToFit finds the largest prefix length n (1 <= n <= count) of a list
// such that estimate(n) — the token cost of the full output with the list
// cut to its first n entries — fits maxTokens. estimate must be monotonic
// in n. At least one entry is always kept, even over budget: an empty
// rollup would hide the blast radius entirely.
func TrimToFit(count, maxTokens int, estimate func(n int) int) int {
	if count <= 1 || estimate(count) <= maxTokens {
		return count
	}
	best := 1
	lo, hi := 1, count-1
	for lo <= hi {
		mid := (lo + hi) / 2
		if estimate(mid) <= maxTokens {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best
}

// Backfill extends a TrimToFit prefix cut with a second pass over the
// omitted tail: items are admitted out of strict-prefix order when they fit
// the leftover headroom. TrimToFit alone lets survival depend entirely on
// sort position — a single item one slot past the cut is dropped for free
// alongside genuinely large ones, even though including it would barely
// move the token count. total is the full list length, kept is the prefix
// length TrimToFit already chose, used is that prefix's actual estimated
// cost, and itemCost estimates a single tail item's marginal cost in
// isolation (a cheap approximation — see Estimate's own doc comment — good
// enough to decide backfill candidates without re-marshalling the whole
// payload per candidate).
//
// The tail (items kept..total-1) is walked in the order the caller already
// sorted it in — Backfill does not re-rank by cost. Earlier callers of this
// function tried a cheapest-first re-sort and it backfired: with many
// near-zero-cost items in the tail (e.g. synthetic entries with no real
// content), cheapest-first let a flood of low-value items win the headroom
// ahead of a moderately-priced but more relevant one just past the cut,
// inverting whatever relevance ordering the caller's sort encodes (depth,
// verification, ...). Skip-continue over the caller's own order respects
// that ordering — an item too expensive for the current headroom is
// skipped, not rejected outright, so a cheaper item further down still gets
// a chance — while never letting a low-priority item preempt a
// higher-priority one it could have displaced.
//
// Returns the admitted indices (a subset of kept..total-1) in ascending
// index order, so the caller can splice them back into the original slice
// before re-sorting for display.
func Backfill(total, kept, maxTokens, used int, itemCost func(i int) int) []int {
	if kept >= total {
		return nil
	}
	headroom := maxTokens - used
	if headroom <= 0 {
		return nil
	}
	var admitted []int
	for i := kept; i < total; i++ {
		c := itemCost(i)
		if c > headroom {
			continue
		}
		admitted = append(admitted, i)
		headroom -= c
	}
	return admitted
}

// Snippet returns up to n source lines of file starting at 1-based line
// start. file may be workspace-relative (resolved against root) or already
// absolute (Z.0: node.File is absolute for out-of-tree/multi-repo services,
// where no single root can make every service's files relative). Any
// failure — missing file, line past EOF, non-positive inputs — returns "":
// snippets are best-effort sugar, never an error path.
func Snippet(root, file string, start, n int) string {
	if n <= 0 || start <= 0 || file == "" {
		return ""
	}
	p := file
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, file)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if start > len(lines) {
		return ""
	}
	end := start - 1 + n
	if end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[start-1:end], "\n")
}

// SnippetSpan returns the exact source of a symbol span [start, end] (1-based,
// inclusive), capping the read at maxLines to bound a runaway span. When end<=0
// (the exact end is unknown) it falls back to a maxLines window from start —
// i.e. Snippet's behaviour. truncated is true when the requested span exceeded
// maxLines and was cut. Like Snippet, any failure returns ("", false): span
// reads are best-effort, never an error path. file may be workspace-relative
// (resolved against root) or already absolute.
func SnippetSpan(root, file string, start, end, maxLines int) (src string, truncated bool) {
	if start <= 0 || file == "" || maxLines <= 0 {
		return "", false
	}
	p := file
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, file)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	lines := strings.Split(string(data), "\n")
	if start > len(lines) {
		return "", false
	}
	// Unknown end → window from start (current Snippet semantics).
	last := end
	if last < start {
		last = start - 1 + maxLines
	}
	if last > len(lines) {
		last = len(lines)
	}
	if last-start+1 > maxLines {
		last = start - 1 + maxLines
		truncated = true
	}
	return strings.Join(lines[start-1:last], "\n"), truncated
}
