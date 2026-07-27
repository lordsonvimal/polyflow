package graph

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// TargetCandidate is one exact-label match surfaced when ResolveTarget finds
// >1 exact match. Agents should re-query with target_service/--target-service
// when target_candidates is non-empty to pin the intended node.
type TargetCandidate struct {
	ID      string `json:"id"`
	Service string `json:"service"`
	File    string `json:"file"`
	Type    string `json:"type"`
}

// NodeSearcher is the subset of Store required by ResolveTarget.
type NodeSearcher interface {
	SearchNodes(ctx context.Context, query string, limit int) ([]*Node, error)
}

// ResolveTarget finds the best node for a search query with optional pre-filters
// applied BEFORE ranking so service-level ambiguity is resolved intentionally.
//
//   - targetService: when non-empty, restrict exact-label candidates to this service.
//   - targetType:    when non-empty, restrict exact-label candidates to this NodeType.
//   - Both empty ⇒ root = SearchNodes[0], byte-identical to the pre-B.3 behaviour.
//
// Returns:
//   - root: the chosen node (nil on error or not-found).
//   - candidates: every exact-label match sorted by (service, file); always non-nil;
//     [] when unambiguous (≤1 exact-label match in the result set). When >1 match
//     exists the list includes the chosen root so agents see the full picture.
//   - err: non-nil only on store error or when no node is found at all.
func ResolveTarget(ctx context.Context, store NodeSearcher, query, targetService, targetType string) (*Node, []TargetCandidate, error) {
	// Fetch more than the usual 5 to catch all exact-label matches.
	nodes, err := store.SearchNodes(ctx, query, 20)
	if err != nil {
		return nil, nil, err
	}
	if len(nodes) == 0 {
		return nil, nil, fmt.Errorf("node not found for query: %s", query)
	}

	// Partition into exact-label matches (case-insensitive) and prefix-only.
	var exact []*Node
	for _, n := range nodes {
		if strings.EqualFold(n.Label, query) {
			exact = append(exact, n)
		}
	}

	// Build the candidates slice: non-nil, sorted by (service, file).
	// Empty when ≤1 exact-label match (unambiguous); populated with every
	// exact match (including the chosen root) when >1 exists.
	candidates := []TargetCandidate{} // []‑never-absent
	if len(exact) > 1 {
		for _, n := range exact {
			candidates = append(candidates, TargetCandidate{
				ID:      n.ID,
				Service: n.Service,
				File:    n.File,
				Type:    string(n.Type),
			})
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].Service != candidates[j].Service {
				return candidates[i].Service < candidates[j].Service
			}
			return candidates[i].File < candidates[j].File
		})
	}

	// Apply filters to the exact-label set first.
	filtered := filterNodes(exact, targetService, targetType)

	// Choose root: first filtered exact match → first unfiltered exact match
	// → SearchNodes[0] (recall-over-precision fallback for non-exact queries).
	// Within a tied set, prefer a non-test-file declaration: test files
	// commonly redeclare a same-named local helper/mock (e.g. inside
	// vi.mock(...)) that would otherwise win by search-rank luck and silently
	// redirect a blast-radius query at the mock instead of the real
	// production symbol. target_candidates still lists every match either
	// way, so this only changes which one is picked by default — it never
	// hides the ambiguity.
	var root *Node
	switch {
	case len(filtered) > 0:
		root = preferNonTestFile(filtered)
	case len(exact) > 0:
		root = preferNonTestFile(exact)
	default:
		root = nodes[0]
	}

	return root, candidates, nil
}

// preferNonTestFile returns the first node in nodes whose file does not look
// like a test file, or nodes[0] if every match is in a test file (or nodes
// has no File set at all, e.g. service-level nodes).
func preferNonTestFile(nodes []*Node) *Node {
	for _, n := range nodes {
		if !isTestFilePath(n.File) {
			return n
		}
	}
	return nodes[0]
}

// isTestFilePath reports whether file looks like a test/spec file by common
// cross-language naming conventions (JS/TS .test./.spec., Go _test.go, Ruby
// _spec.rb/_test.rb, Python test_*.py, and __tests__/spec/test directories).
func isTestFilePath(file string) bool {
	if file == "" {
		return false
	}
	lower := strings.ToLower(file)
	base := lower
	if idx := strings.LastIndexByte(lower, '/'); idx >= 0 {
		base = lower[idx+1:]
	}
	switch {
	case strings.Contains(base, ".test."), strings.Contains(base, ".spec."),
		strings.HasSuffix(base, "_test.go"), strings.HasSuffix(base, "_test.rb"),
		strings.HasSuffix(base, "_spec.rb"), strings.HasPrefix(base, "test_"):
		return true
	case strings.Contains(lower, "/__tests__/"), strings.Contains(lower, "/spec/"),
		strings.HasPrefix(lower, "spec/"):
		return true
	}
	return false
}

// filterNodes returns the subset of nodes matching targetService and targetType
// (both optional; empty string = no filter on that dimension).
func filterNodes(nodes []*Node, targetService, targetType string) []*Node {
	if targetService == "" && targetType == "" {
		return nodes
	}
	var out []*Node
	for _, n := range nodes {
		if targetService != "" && n.Service != targetService {
			continue
		}
		if targetType != "" && string(n.Type) != targetType {
			continue
		}
		out = append(out, n)
	}
	return out
}
