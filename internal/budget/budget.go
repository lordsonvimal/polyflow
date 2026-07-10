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

// Snippet returns up to n source lines of file (workspace-relative, resolved
// against root) starting at 1-based line start. Any failure — missing file,
// line past EOF, non-positive inputs — returns "": snippets are best-effort
// sugar, never an error path.
func Snippet(root, file string, start, n int) string {
	if n <= 0 || start <= 0 || file == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(root, file))
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
