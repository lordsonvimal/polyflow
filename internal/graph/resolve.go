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
	var root *Node
	switch {
	case len(filtered) > 0:
		root = filtered[0]
	case len(exact) > 0:
		root = exact[0]
	default:
		root = nodes[0]
	}

	return root, candidates, nil
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
