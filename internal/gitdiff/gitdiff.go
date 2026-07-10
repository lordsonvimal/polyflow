// Package gitdiff extracts changed line spans from git diffs, feeding the
// diff-aware impact query ("will my current changes impact anything").
// Spans are new-side line numbers, matching the working tree the indexer
// parsed.
package gitdiff

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Span is an inclusive 1-based range of changed lines on the new side of a
// diff. A pure deletion (no new-side lines) maps to the single line where
// the cut happened, so enclosing-scope lookup still works.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// FileChange is one changed file with its changed spans. Deleted files have
// no new-side content: Spans is empty and Deleted is set, so callers can
// report the gap instead of silently dropping it.
type FileChange struct {
	Path    string `json:"path"`
	Deleted bool   `json:"deleted,omitempty"`
	Spans   []Span `json:"spans,omitempty"`
}

// Changes runs git in dir and returns the changed spans of the working tree
// against HEAD (everything uncommitted, staged or not), or of the index only
// when staged is set (git diff --cached). Paths are relative to dir, matching
// node file paths when dir is the workspace root.
func Changes(dir string, staged bool) ([]FileChange, error) {
	args := []string{"diff", "-U0", "--no-color", "--no-ext-diff", "--relative"}
	if staged {
		args = append(args, "--cached")
	} else {
		args = append(args, "HEAD")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return Parse(&out), nil
}

var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// Parse reads a unified diff and returns the per-file changed spans.
// Binary files (no hunks) yield a FileChange with no spans; pure renames
// (no content change) produce no entry at all.
func Parse(r io.Reader) []FileChange {
	var out []FileChange
	var cur *FileChange
	var oldPath string
	// inHeader guards the ---/+++ parsing: content lines in a -U0 diff start
	// with '-' or '+' too, so "--- a/x" is only a file header between a
	// "diff --git" line and the hunks that follow.
	inHeader := false

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHeader = true
			cur = nil
			oldPath = ""
		case inHeader && strings.HasPrefix(line, "--- "):
			oldPath = strings.TrimPrefix(strings.TrimPrefix(line, "--- "), "a/")
		case inHeader && strings.HasPrefix(line, "+++ "):
			inHeader = false
			p := strings.TrimPrefix(line, "+++ ")
			out = append(out, FileChange{})
			cur = &out[len(out)-1]
			if p == "/dev/null" {
				cur.Path = oldPath
				cur.Deleted = true
			} else {
				cur.Path = strings.TrimPrefix(p, "b/")
			}
		case strings.HasPrefix(line, "@@ "):
			if cur == nil || cur.Deleted {
				continue
			}
			m := hunkRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			if count == 0 {
				// Pure deletion: git records the new-side line *before* the
				// cut; use it (clamped to 1) so enclosure lookup has an anchor.
				if start < 1 {
					start = 1
				}
				cur.Spans = append(cur.Spans, Span{Start: start, End: start})
			} else {
				cur.Spans = append(cur.Spans, Span{Start: start, End: start + count - 1})
			}
		}
	}
	return out
}
