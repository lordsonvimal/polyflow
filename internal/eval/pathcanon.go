package eval

import (
	"path/filepath"
	"strings"
)

// pathCanon maps file paths onto a single coordinate system so that ground
// truth and graph output can be compared.
//
// The corpus and the graph disagree about how to spell a file, in two ways
// that scoring must not mistake for a miss:
//
//   - Ground truth is written repo-relative ("cmd/writefreely/main.go") in some
//     manifests and absolute in others, while impact output is always absolute.
//   - A corpus repo is reached through the eval/.cache symlink, so the graph
//     records ".../polyflow/eval/.cache/synergy/apps/x.tsx" for the file a
//     manifest calls "/Users/…/Projects/synergy/apps/x.tsx".
//
// Both are the same file, and an exact string compare scored them as silent
// misses — the single failure mode the corpus exists to detect. That silently
// zeroed 74 of 163 cases while the gate still reported success.
//
// Canonical form is repo-relative when the path lies inside the repo, and the
// symlink-resolved absolute path otherwise (out-of-tree services legitimately
// live elsewhere). Both sides of every comparison pass through key, so paths
// that already agreed still agree.
type pathCanon struct {
	root string // symlink-resolved absolute repo root; "" when unresolvable
}

func newPathCanon(root string) *pathCanon {
	abs, err := filepath.Abs(root)
	if err != nil {
		return &pathCanon{}
	}
	if resolved, rErr := filepath.EvalSymlinks(abs); rErr == nil {
		abs = resolved
	}
	return &pathCanon{root: abs}
}

// key returns the canonical form of one path. A relative path is already in
// canonical form. EvalSymlinks failing (a path that no longer exists) is not
// an error here — the cleaned path is still the best key available, and
// scoring it as a miss is the honest outcome.
func (pc *pathCanon) key(p string) string {
	if p == "" {
		return p
	}
	if !filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	q := filepath.Clean(p)
	if resolved, err := filepath.EvalSymlinks(q); err == nil {
		q = resolved
	}
	if pc.root != "" {
		if rel, err := filepath.Rel(pc.root, q); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return rel
		}
	}
	return q
}

// keys canonicalises a slice, preserving order and dropping nothing.
func (pc *pathCanon) keys(ps []string) []string {
	if ps == nil {
		return nil
	}
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, pc.key(p))
	}
	return out
}

// keySet canonicalises the keys of a set.
func (pc *pathCanon) keySet(m map[string]bool) map[string]bool {
	if m == nil {
		return nil
	}
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[pc.key(k)] = v
	}
	return out
}
