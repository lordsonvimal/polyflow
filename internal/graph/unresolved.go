package graph

import "fmt"

// UnresolvedInFiles scopes the blind-spot ledger to a traversal: it returns
// the refs whose file appears in the given set, preserving input order. The
// result is never nil — an empty slice encodes as [] so agents can tell
// "no known blind spots here" apart from "section missing".
func UnresolvedInFiles(refs []UnresolvedRef, files map[string]bool) []UnresolvedRef {
	out := make([]UnresolvedRef, 0)
	for _, r := range refs {
		if files[r.File] {
			out = append(out, r)
		}
	}
	return out
}

// UnresolvedNote renders the agent-facing warning attached to query output
// alongside a non-empty unresolved section. Empty when there is nothing to
// verify.
func UnresolvedNote(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "verify this 1 unresolved reference manually — the indexer could not resolve it, so edges may be missing from this answer"
	}
	return fmt.Sprintf("verify these %d unresolved references manually — the indexer could not resolve them, so edges may be missing from this answer", n)
}
