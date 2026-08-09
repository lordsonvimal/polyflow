package linker

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// railsRoute is a resolved (method, path) pair for a Rails route helper.
type railsRoute struct {
	Method string
	Path   string
}

// BuildRailsHelperMap builds per-service helper→routes from the route names the
// walker stamped on each http_handler.
// Returns: svc → helper_name → []railsRoute, iteration order sorted for rule 2.
//
// This used to *reconstruct* a path from the resource name — `resources
// :folders` became `/folders/:id` for `folder_path` — and every reconstruction
// was wrong for the one app that has real views. Rails' three grouping
// constructs contribute to the URL, the controller module and the route name in
// three different combinations (see parser.nameScope), so a path derived from a
// name and a name derived from a path are both lossy. orion puts ~400 routes
// under a single `scope "app"`, which contributes to the URL and to nothing
// else: every helper in every view resolved to a path that was missing its
// prefix, and 228 nav links with a fully "resolved" path matched no handler.
//
// The walker already composes the exact path and now records the exact name
// alongside it, so this is a lookup rather than a derivation. A route the
// walker could not name (a dynamic literal path, a member route with no
// enclosing resource) contributes nothing here and its helper lands in the
// ledger — an absent entry is recoverable, a confidently wrong one is not.
func BuildRailsHelperMap(nodes []graph.Node) map[string]map[string][]railsRoute {
	result := make(map[string]map[string][]railsRoute)

	seen := map[string]bool{}
	add := func(svc, helper string, r railsRoute) {
		key := svc + "\x00" + helper + "\x00" + r.Method + "\x00" + r.Path
		if seen[key] {
			return
		}
		seen[key] = true
		if result[svc] == nil {
			result[svc] = make(map[string][]railsRoute)
		}
		result[svc][helper] = append(result[svc][helper], r)
	}

	for _, n := range nodes {
		if n.Language != "ruby" || n.Type != graph.NodeTypeHTTPHandler {
			continue
		}
		name := n.Meta["route_helper"]
		method := strings.ToUpper(n.Meta["method"])
		path := n.Meta["path"]
		if name == "" || method == "" || path == "" {
			continue
		}
		// `_path` and `_url` name the same route; Rails generates both and views
		// use them interchangeably (a mailer reaches for `_url`).
		add(n.Service, name+"_path", railsRoute{method, path})
		add(n.Service, name+"_url", railsRoute{method, path})
	}

	// Sort each route list for determinism (rule 2).
	for svc := range result {
		for helper := range result[svc] {
			sort.Slice(result[svc][helper], func(i, j int) bool {
				a, b := result[svc][helper][i], result[svc][helper][j]
				if a.Method != b.Method {
					return a.Method < b.Method
				}
				return a.Path < b.Path
			})
		}
	}

	return result
}

// ResolveRailsNavHelpers updates http_client nodes that carry a `helper` meta key
// (emitted by nav_link_rails_helper patterns) with the resolved `method` and `path`
// from the per-service helper map. Returns updated node copies and unresolved refs.
//
// Fan-out rule (rule 1): a helper that maps to multiple routes in different namespaces
// emits candidate copies for each, plus a rails_helper_collision ledger entry.
// Sort order: updated nodes are sorted by ID for determinism (rule 2).
func ResolveRailsNavHelpers(nodes []graph.Node) (updatedNodes []graph.Node, unresolved []graph.UnresolvedRef) {
	helperMap := BuildRailsHelperMap(nodes)

	seen := make(map[string]bool)

	// Collect nav-helper nodes (sorted by ID for determinism, rule 2).
	var navHelperNodes []graph.Node
	for _, n := range nodes {
		if n.Type != graph.NodeTypeHTTPClient {
			continue
		}
		if n.Meta["helper"] == "" {
			continue
		}
		navHelperNodes = append(navHelperNodes, n)
	}
	sort.Slice(navHelperNodes, func(i, j int) bool {
		return navHelperNodes[i].ID < navHelperNodes[j].ID
	})

	for _, n := range navHelperNodes {
		helperName := n.Meta["helper"]
		svcMap := helperMap[n.Service]

		routes, ok := svcMap[helperName]
		if !ok || len(routes) == 0 {
			// Unresolvable helper.
			if !seen["unresolved:"+n.Service+":"+helperName] {
				seen["unresolved:"+n.Service+":"+helperName] = true
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: n.Service,
					File:    n.File,
					Line:    n.Line,
					Name:    helperName,
					Kind:    "rails_helper_unresolved",
				})
			}
			continue
		}

		// Pick the route matching the node's method (default GET for nav links).
		nodeMethod := strings.ToUpper(n.Meta["method"])
		if nodeMethod == "" {
			nodeMethod = "GET"
		}

		// Find all routes matching the node's method.
		var matching []railsRoute
		for _, r := range routes {
			if r.Method == nodeMethod {
				matching = append(matching, r)
			}
		}
		if len(matching) == 0 {
			// No method match: use all routes (method mismatch, let contract engine decide).
			matching = routes
		}

		// Deduplicate paths in matching.
		seen2 := make(map[string]bool)
		var deduped []railsRoute
		for _, r := range matching {
			key := r.Method + " " + r.Path
			if !seen2[key] {
				seen2[key] = true
				deduped = append(deduped, r)
			}
		}
		matching = deduped

		if len(matching) > 1 {
			// Multiple distinct paths → fan-out (rule 1) + collision ledger.
			collKey := "collision:" + n.Service + ":" + helperName
			if !seen[collKey] {
				seen[collKey] = true
				unresolved = append(unresolved, graph.UnresolvedRef{
					Service: n.Service,
					File:    n.File,
					Line:    n.Line,
					Name:    helperName,
					Kind:    "rails_helper_collision",
				})
			}
		}

		for _, r := range matching {
			updated := copyNode(n)
			updated.Meta["path"] = r.Path
			updated.Meta["method"] = r.Method
			delete(updated.Meta, "helper")
			// The key was dynamic because the helper call was unreadable; it has
			// just been read. Leaving the marker behind sends the node to the
			// honest-gap ledger instead of the matcher, which is why 148 of the
			// fleet's 175 path-resolved nav links emitted no navigates_to edge
			// even once their path was correct. Same shape as the Phase B ledger
			// fix: a resolved reference is not a blind spot.
			delete(updated.Meta, "key_dynamic")
			delete(updated.Meta, "key_dynamic_raw")
			label := r.Method + " " + r.Path
			updated.Label = label

			if len(matching) > 1 {
				// Fan-out: emit candidate copies per distinct path.
				updated.ID = fmt.Sprintf("%s:candidate:%s", n.ID, r.Path)
				updated.Meta["via"] = "rails_helper_candidate"
			}
			updatedNodes = append(updatedNodes, updated)
		}
	}

	// Sort output for determinism (rule 2).
	sort.Slice(updatedNodes, func(i, j int) bool {
		return updatedNodes[i].ID < updatedNodes[j].ID
	})
	sort.Slice(unresolved, func(i, j int) bool {
		if unresolved[i].Service != unresolved[j].Service {
			return unresolved[i].Service < unresolved[j].Service
		}
		if unresolved[i].Name != unresolved[j].Name {
			return unresolved[i].Name < unresolved[j].Name
		}
		return unresolved[i].Kind < unresolved[j].Kind
	})

	return updatedNodes, unresolved
}

// copyNode returns a shallow copy of n with a new meta map.
func copyNode(n graph.Node) graph.Node {
	meta := make(map[string]string, len(n.Meta))
	for k, v := range n.Meta {
		meta[k] = v
	}
	n.Meta = meta
	return n
}
