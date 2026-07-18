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

// resourcesEntry holds a resources/resource route with its line for parent lookup.
type resourcesEntry struct {
	service  string
	file     string
	line     int
	plural   string // e.g. "reports" (stripped of ":")
	singular string // derived by singularize()
	isSingle bool   // true for "resource" (singular resource route)
}

// memberEntry holds a member/collection verb route with its line for association.
type memberEntry struct {
	service  string
	file     string
	line     int
	verb     string // HTTP method, uppercased
	action   string // e.g. "archive" (stripped of ":")
	isMember bool   // false = collection
}

// BuildRailsHelperMap builds per-service helper→routes from route nodes.
// Returns: svc → helper_name → []railsRoute, iteration order sorted for rule 2.
func BuildRailsHelperMap(nodes []graph.Node) map[string]map[string][]railsRoute {
	result := make(map[string]map[string][]railsRoute)

	ensure := func(svc string) {
		if result[svc] == nil {
			result[svc] = make(map[string][]railsRoute)
		}
	}
	add := func(svc, helper string, r railsRoute) {
		ensure(svc)
		result[svc][helper] = append(result[svc][helper], r)
	}

	// Collect resources and member/collection entries.
	var resList []resourcesEntry
	var memberList []memberEntry
	var verbList []struct {
		service string
		file    string
		line    int
		method  string
		path    string
	}

	for _, n := range nodes {
		if n.Language != "ruby" {
			continue
		}
		pat := n.Meta["pattern"]
		switch pat {
		case "resources_route":
			resource := strings.TrimPrefix(n.Meta["resource"], ":")
			if resource == "" {
				continue
			}
			resList = append(resList, resourcesEntry{
				service:  n.Service,
				file:     n.File,
				line:     n.Line,
				plural:   resource,
				singular: singularize(resource),
				isSingle: false,
			})
		case "resource_route":
			resource := strings.TrimPrefix(n.Meta["resource"], ":")
			if resource == "" {
				continue
			}
			resList = append(resList, resourcesEntry{
				service:  n.Service,
				file:     n.File,
				line:     n.Line,
				plural:   resource + "s", // plural for path consistency
				singular: resource,
				isSingle: true,
			})
		case "member_verb_route":
			action := strings.TrimPrefix(n.Meta["action"], ":")
			verb := strings.ToUpper(n.Meta["verb"])
			if action == "" || verb == "" {
				continue
			}
			memberList = append(memberList, memberEntry{
				service:  n.Service,
				file:     n.File,
				line:     n.Line,
				verb:     verb,
				action:   action,
				isMember: true,
			})
		case "collection_verb_route":
			action := strings.TrimPrefix(n.Meta["action"], ":")
			verb := strings.ToUpper(n.Meta["verb"])
			if action == "" || verb == "" {
				continue
			}
			memberList = append(memberList, memberEntry{
				service:  n.Service,
				file:     n.File,
				line:     n.Line,
				verb:     verb,
				action:   action,
				isMember: false,
			})
		case "http_verb_route":
			method := strings.ToUpper(n.Meta["method"])
			path := n.Meta["path"]
			if method != "" && path != "" {
				verbList = append(verbList, struct {
					service string
					file    string
					line    int
					method  string
					path    string
				}{n.Service, n.File, n.Line, method, path})
			}
		}
	}

	// Sort for determinism (rule 2) — stable ordering matters for fan-out output.
	sort.Slice(resList, func(i, j int) bool {
		a, b := resList[i], resList[j]
		if a.service != b.service {
			return a.service < b.service
		}
		if a.file != b.file {
			return a.file < b.file
		}
		return a.line < b.line
	})
	sort.Slice(memberList, func(i, j int) bool {
		a, b := memberList[i], memberList[j]
		if a.service != b.service {
			return a.service < b.service
		}
		if a.file != b.file {
			return a.file < b.file
		}
		return a.line < b.line
	})

	// Add RESTful helpers for each resources/resource entry.
	for _, r := range resList {
		basePath := "/" + r.plural
		if r.isSingle {
			basePath = "/" + r.singular
		}
		p := r.plural
		s := r.singular

		if r.isSingle {
			// Singular resource: no :id in paths.
			add(r.service, s+"_path", railsRoute{"GET", basePath})
			add(r.service, s+"_path", railsRoute{"PATCH", basePath})
			add(r.service, s+"_path", railsRoute{"PUT", basePath})
			add(r.service, s+"_path", railsRoute{"DELETE", basePath})
			add(r.service, "new_"+s+"_path", railsRoute{"GET", basePath + "/new"})
			add(r.service, "edit_"+s+"_path", railsRoute{"GET", basePath + "/edit"})
		} else {
			// Plural resource: standard 7 RESTful actions.
			add(r.service, p+"_path", railsRoute{"GET", basePath})           // index
			add(r.service, p+"_path", railsRoute{"POST", basePath})           // create
			add(r.service, "new_"+s+"_path", railsRoute{"GET", basePath + "/new"})
			add(r.service, "edit_"+s+"_path", railsRoute{"GET", basePath + "/:id/edit"})
			add(r.service, s+"_path", railsRoute{"GET", basePath + "/:id"})  // show
			add(r.service, s+"_path", railsRoute{"PATCH", basePath + "/:id"}) // update
			add(r.service, s+"_path", railsRoute{"PUT", basePath + "/:id"})   // update
			add(r.service, s+"_path", railsRoute{"DELETE", basePath + "/:id"}) // destroy
		}

		// _url aliases map to the same routes.
		for _, helper := range []string{p + "_path", "new_" + s + "_path", "edit_" + s + "_path", s + "_path"} {
			urlHelper := strings.TrimSuffix(helper, "_path") + "_url"
			if routes, ok := result[r.service][helper]; ok {
				result[r.service][urlHelper] = append(result[r.service][urlHelper], routes...)
			}
		}
	}

	// Add member/collection helpers by finding the enclosing resources entry
	// (nearest preceding resources entry in the same file, by line).
	for _, m := range memberList {
		parent := nearestResource(resList, m.service, m.file, m.line)
		if parent == nil {
			continue // no enclosing resources found; ledger is handled in ResolveRailsNavHelpers
		}
		basePath := "/" + parent.plural
		if parent.isSingle {
			basePath = "/" + parent.singular
		}
		var helper, path string
		if m.isMember {
			// member action: {action}_{singular}_path → VERB /resource/:id/{action}
			helper = m.action + "_" + parent.singular + "_path"
			path = basePath + "/:id/" + m.action
		} else {
			// collection action: {action}_{plural}_path → VERB /resources/{action}
			helper = m.action + "_" + parent.plural + "_path"
			path = basePath + "/" + m.action
		}
		add(m.service, helper, railsRoute{m.verb, path})
		// _url alias
		urlHelper := strings.TrimSuffix(helper, "_path") + "_url"
		add(m.service, urlHelper, railsRoute{m.verb, path})
	}

	// Add explicit verb routes with helper names derived from the path.
	// e.g. GET /reports → reports_path (heuristic; used only when no resources entry covers it)
	for _, v := range verbList {
		helper := pathToHelper(v.path)
		if helper != "" {
			add(v.service, helper, railsRoute{v.method, v.path})
		}
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

// nearestResource returns the resources/resource entry in the same service+file
// with the highest line number that is still below targetLine.
func nearestResource(entries []resourcesEntry, service, file string, targetLine int) *resourcesEntry {
	var best *resourcesEntry
	for i := range entries {
		e := &entries[i]
		if e.service != service || e.file != file {
			continue
		}
		if e.line >= targetLine {
			continue
		}
		if best == nil || e.line > best.line {
			best = e
		}
	}
	return best
}

// singularize converts a common English plural resource name to singular.
// Covers the most common Rails resource naming conventions.
func singularize(plural string) string {
	// Irregular cases first.
	irregulars := map[string]string{
		"people":   "person",
		"men":      "man",
		"women":    "woman",
		"children": "child",
		"mice":     "mouse",
		"oxen":     "ox",
		"teeth":    "tooth",
		"feet":     "foot",
		"geese":    "goose",
		"data":     "datum",
		"criteria": "criterion",
		"media":    "medium",
	}
	if s, ok := irregulars[plural]; ok {
		return s
	}

	switch {
	case strings.HasSuffix(plural, "ies"):
		return plural[:len(plural)-3] + "y" // categories → category
	case strings.HasSuffix(plural, "ses") || strings.HasSuffix(plural, "xes") ||
		strings.HasSuffix(plural, "ches") || strings.HasSuffix(plural, "shes"):
		return plural[:len(plural)-2] // buses→bus, boxes→box, branches→branch
	case strings.HasSuffix(plural, "ves"):
		return plural[:len(plural)-3] + "f" // leaves→leaf (approximate)
	case strings.HasSuffix(plural, "s") && !strings.HasSuffix(plural, "ss"):
		return plural[:len(plural)-1] // reports → report
	}
	return plural
}

// pathToHelper derives a Rails-style helper name from a literal path.
// e.g. "/reports" → "reports_path", "/admin/users" → "admin_users_path".
// Returns "" for dynamic paths (containing :param segments).
func pathToHelper(path string) string {
	if strings.Contains(path, ":") {
		return "" // dynamic parameter segments → cannot derive stable helper
	}
	path = strings.Trim(path, "/")
	if path == "" {
		return "root_path"
	}
	parts := strings.Split(path, "/")
	return strings.Join(parts, "_") + "_path"
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
