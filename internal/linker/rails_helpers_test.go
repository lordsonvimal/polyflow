package linker

import (
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// makeRouteNode builds an http_handler node the way the Rails route walker
// emits one: an already-composed path plus the route name it was declared
// under. Both are facts only the walker can know — the earlier fixtures passed
// a bare resource name and expected the map to reconstruct a path from it,
// which is precisely the model this pass replaced.
func makeRouteNode(id, svc, file string, line int, helper, method, path string) graph.Node {
	return graph.Node{
		ID:       id,
		Type:     graph.NodeTypeHTTPHandler,
		Service:  svc,
		File:     file,
		Line:     line,
		Language: "ruby",
		Meta: map[string]string{
			"pattern":      "rest_resource_route",
			"route_helper": helper,
			"method":       method,
			"path":         path,
		},
	}
}

func makeNavHelperNode(id, svc, file string, line int, helper string) graph.Node {
	return graph.Node{
		ID:       id,
		Type:     graph.NodeTypeHTTPClient,
		Service:  svc,
		File:     file,
		Line:     line,
		Language: "ruby",
		Label:    "nav_link_rails_helper",
		Meta: map[string]string{
			"pattern":  "nav_link_rails_helper",
			"helper":   helper,
			"nav_link": "true",
			"method":   "GET",
		},
	}
}

// TestBuildRailsHelperMap_ReadsComposedRoutes verifies that a helper resolves
// to the path the walker composed, prefix and all — not to one rebuilt from the
// resource name. `scope "app" { resources :folders }` is the shape that broke:
// folder_path is /app/folders/:id, and no amount of inflection on "folders"
// produces the "app" segment.
func TestBuildRailsHelperMap_ReadsComposedRoutes(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeRouteNode("n1", "svc", "config/routes.rb", 2, "folders", "GET", "/app/folders"),
		makeRouteNode("n2", "svc", "config/routes.rb", 2, "folders", "POST", "/app/folders"),
		makeRouteNode("n3", "svc", "config/routes.rb", 2, "folder", "GET", "/app/folders/:id"),
		makeRouteNode("n4", "svc", "config/routes.rb", 2, "new_folder", "GET", "/app/folders/new"),
		makeRouteNode("n5", "svc", "config/routes.rb", 2, "edit_folder", "GET", "/app/folders/:id/edit"),
	}
	m := BuildRailsHelperMap(nodes)
	svc := m["svc"]

	assertHelperGet(t, svc, "folders_path", "GET", "/app/folders")
	assertHelperGet(t, svc, "folders_path", "POST", "/app/folders")
	assertHelperGet(t, svc, "folder_path", "GET", "/app/folders/:id")
	assertHelperGet(t, svc, "new_folder_path", "GET", "/app/folders/new")
	assertHelperGet(t, svc, "edit_folder_path", "GET", "/app/folders/:id/edit")

	// _url is the same route under the other name Rails generates.
	assertHelperGet(t, svc, "folder_url", "GET", "/app/folders/:id")
}

// TestBuildRailsHelperMap_UnnamedRouteIsSkipped verifies that a route the walker
// could not name contributes nothing. A nameless route is a gap the ledger
// records; inventing a name for it would shadow a real helper.
func TestBuildRailsHelperMap_UnnamedRouteIsSkipped(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeRouteNode("n1", "svc", "config/routes.rb", 2, "", "GET", "/app/*glob"),
	}
	if len(BuildRailsHelperMap(nodes)) != 0 {
		t.Error("a route with no route_helper must contribute no entry")
	}
}

// TestBuildRailsHelperMap_ScopedNamesDoNotCollide verifies that two resources
// with the same name in different namespaces stay distinct helpers rather than
// fanning out. Their *paths* differ and so do their Rails names, which is the
// whole reason the name is recorded at declaration time.
func TestBuildRailsHelperMap_ScopedNamesDoNotCollide(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeRouteNode("n1", "svc", "routes.rb", 1, "users", "GET", "/app/users"),
		makeRouteNode("n2", "svc", "routes.rb", 10, "client_api_v1_users", "GET", "/client_api/v1/users"),
	}
	svc := BuildRailsHelperMap(nodes)["svc"]

	if got := len(svc["users_path"]); got != 1 {
		t.Errorf("users_path: expected 1 route, got %d: %v", got, svc["users_path"])
	}
	assertHelperGet(t, svc, "users_path", "GET", "/app/users")
	assertHelperGet(t, svc, "client_api_v1_users_path", "GET", "/client_api/v1/users")
}

// TestBuildRailsHelperMap_FanOutOnGenuineDuplicate verifies that when one name
// really does map to two paths, both survive for the fan-out rule (rule 1).
func TestBuildRailsHelperMap_FanOutOnGenuineDuplicate(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeRouteNode("n1", "svc", "routes.rb", 1, "users", "GET", "/app/users"),
		makeRouteNode("n2", "svc", "config/routes/admin.rb", 3, "users", "GET", "/admin/users"),
	}
	svc := BuildRailsHelperMap(nodes)["svc"]
	if got := len(svc["users_path"]); got != 2 {
		t.Errorf("fan-out: expected 2 routes for users_path, got %d", got)
	}
}

// TestBuildRailsHelperMap_DedupesIdenticalRoutes verifies that the same
// (name, method, path) recorded twice yields one entry, so a duplicate node
// cannot masquerade as a collision and trigger candidate fan-out.
func TestBuildRailsHelperMap_DedupesIdenticalRoutes(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeRouteNode("n1", "svc", "routes.rb", 1, "users", "GET", "/app/users"),
		makeRouteNode("n2", "svc", "routes.rb", 1, "users", "GET", "/app/users"),
	}
	svc := BuildRailsHelperMap(nodes)["svc"]
	if got := len(svc["users_path"]); got != 1 {
		t.Errorf("expected identical routes deduped to 1, got %d", got)
	}
}

// TestBuildRailsHelperMap_Determinism verifies that running twice produces identical output (rule 2).
func TestBuildRailsHelperMap_Determinism(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeRouteNode("n1", "svc", "routes.rb", 1, "reports", "GET", "/app/reports"),
		makeRouteNode("n2", "svc", "routes.rb", 2, "archive_report", "GET", "/app/reports/:id/archive"),
		makeRouteNode("n3", "svc", "routes.rb", 5, "users", "GET", "/app/users"),
		makeRouteNode("n4", "svc", "routes.rb", 6, "users", "GET", "/admin/users"),
	}

	run := func() string {
		m := BuildRailsHelperMap(nodes)
		svc := m["svc"]
		helpers := make([]string, 0, len(svc))
		for h := range svc {
			helpers = append(helpers, h)
		}
		sort.Strings(helpers)
		var out []string
		for _, h := range helpers {
			for _, r := range svc[h] {
				out = append(out, h+":"+r.Method+":"+r.Path)
			}
		}
		return join(out)
	}

	a, b := run(), run()
	if a != b {
		t.Errorf("determinism: run 1 != run 2\nrun1: %s\nrun2: %s", a, b)
	}
}

// TestResolveRailsNavHelpers_Basic verifies path resolution for a simple link_to.
func TestResolveRailsNavHelpers_Basic(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeRouteNode("r1", "svc", "routes.rb", 1, "reports", "GET", "/app/reports"),
		makeNavHelperNode("c1", "svc", "views/index.erb", 5, "reports_path"),
	}

	updated, unresolved := ResolveRailsNavHelpers(nodes)
	if len(unresolved) != 0 {
		t.Errorf("expected 0 unresolved, got %d: %v", len(unresolved), unresolved)
	}
	if len(updated) == 0 {
		t.Fatal("expected updated node for reports_path")
	}
	found := false
	for _, n := range updated {
		if n.ID == "c1" && n.Meta["path"] == "/app/reports" && n.Meta["method"] == "GET" {
			found = true
		}
	}
	if !found {
		t.Errorf("updated node for reports_path not found or has wrong meta; got: %+v", updated)
	}
}

// TestResolveRailsNavHelpers_Unresolved verifies that unknown helpers go to the ledger.
func TestResolveRailsNavHelpers_Unresolved(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeNavHelperNode("c1", "svc", "views/index.erb", 5, "unknown_helper_path"),
	}

	updated, unresolved := ResolveRailsNavHelpers(nodes)
	if len(updated) != 0 {
		t.Errorf("expected 0 updated nodes for unknown helper, got %d", len(updated))
	}
	if len(unresolved) == 0 {
		t.Fatal("expected rails_helper_unresolved ledger entry")
	}
	if unresolved[0].Kind != "rails_helper_unresolved" {
		t.Errorf("expected kind rails_helper_unresolved, got %q", unresolved[0].Kind)
	}
	if unresolved[0].Name != "unknown_helper_path" {
		t.Errorf("expected name unknown_helper_path, got %q", unresolved[0].Name)
	}
}

// TestResolveRailsNavHelpers_HelperRemoved verifies that the 'helper' meta key
// is removed from the updated node (the helper is now resolved into path/method).
func TestResolveRailsNavHelpers_HelperRemoved(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeRouteNode("r1", "svc", "routes.rb", 1, "reports", "GET", "/app/reports"),
		makeNavHelperNode("c1", "svc", "views/index.erb", 5, "reports_path"),
	}

	updated, _ := ResolveRailsNavHelpers(nodes)
	for _, n := range updated {
		if n.ID == "c1" {
			if _, ok := n.Meta["helper"]; ok {
				t.Error("updated node still has 'helper' meta key; should have been removed")
			}
		}
	}
}

// TestResolveRailsNavHelpers_Determinism verifies two-run byte-identical output (rule 2).
func TestResolveRailsNavHelpers_Determinism(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeRouteNode("r1", "svc", "routes.rb", 1, "reports", "GET", "/app/reports"),
		makeRouteNode("r2", "svc", "routes.rb", 1, "report", "GET", "/app/reports/:id"),
		makeNavHelperNode("c1", "svc", "views/a.erb", 5, "reports_path"),
		makeNavHelperNode("c2", "svc", "views/b.erb", 3, "report_path"),
		makeNavHelperNode("c3", "svc", "views/b.erb", 7, "unknown_path"),
	}

	run := func() string {
		updated, unresolved := ResolveRailsNavHelpers(nodes)
		var out []string
		for _, n := range updated {
			out = append(out, n.ID+":"+n.Meta["method"]+":"+n.Meta["path"])
		}
		for _, u := range unresolved {
			out = append(out, "u:"+u.Name+":"+u.Kind)
		}
		return join(out)
	}

	a, b := run(), run()
	if a != b {
		t.Errorf("determinism failed\nrun1: %s\nrun2: %s", a, b)
	}
}

// assertHelperGet checks that the given helper has a route with the given method+path.
func assertHelperGet(t *testing.T, svc map[string][]railsRoute, helper, method, path string) {
	t.Helper()
	routes, ok := svc[helper]
	if !ok {
		t.Errorf("helper %q not in map; keys: %v", helper, mapKeys(svc))
		return
	}
	for _, r := range routes {
		if r.Method == method && r.Path == path {
			return
		}
	}
	t.Errorf("helper %q: expected route %s %s; got: %v", helper, method, path, routes)
}

func mapKeys(m map[string][]railsRoute) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func join(ss []string) string {
	sort.Strings(ss)
	out := ""
	for _, s := range ss {
		out += s + "\n"
	}
	return out
}

// TestResolveRailsNavHelpers_FanOutReplacesTheOriginal is the node-hygiene half
// of Tier CN. A helper naming more than one route emits one candidate per route,
// and every candidate used to get a freshly minted ID. The caller updates in
// place when it recognises the ID and appends when it does not
// (internal/indexer/link_passes.go's rails_nav_helpers pass), so the original
// unresolved node survived alongside its own candidates — still labelled with
// its pattern name, still carrying `helper`, and dangling permanently because
// nothing downstream matches a node with no path.
//
// The first candidate therefore keeps the original's ID. The assertion is on
// the ID set rather than the count so a future change cannot satisfy it by
// dropping a candidate instead of reusing the ID.
func TestResolveRailsNavHelpers_FanOutReplacesTheOriginal(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeRouteNode("r1", "svc", "routes.rb", 1, "session", "POST", "/session"),
		makeRouteNode("r2", "svc", "routes.rb", 2, "session", "DELETE", "/session"),
		makeNavHelperNode("c1", "svc", "views/new.erb", 11, "session_path"),
	}

	updated, unresolved := ResolveRailsNavHelpers(nodes)

	ids := map[string]graph.Node{}
	for _, n := range updated {
		if _, dup := ids[n.ID]; dup {
			t.Errorf("duplicate candidate ID %q: two candidates collapse to one node in the store", n.ID)
		}
		ids[n.ID] = n
	}
	if _, ok := ids["c1"]; !ok {
		t.Errorf("original node c1 was not replaced by a candidate; got IDs %v", sortedIDs(ids))
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 candidates for the two /session routes, got %d: %v", len(ids), sortedIDs(ids))
	}

	// Both routes share a path and differ only by method, which is exactly the
	// pair a path-only candidate ID could not tell apart.
	methods := map[string]bool{}
	for _, n := range ids {
		methods[n.Meta["method"]] = true
		if n.Meta["path"] != "/session" {
			t.Errorf("candidate %s has path %q, want /session", n.ID, n.Meta["path"])
		}
		if n.Meta["helper"] != "" {
			t.Errorf("candidate %s still carries the unresolved helper marker", n.ID)
		}
		if n.Meta["via"] != "rails_helper_candidate" {
			t.Errorf("candidate %s not marked as a fan-out candidate", n.ID)
		}
	}
	if !methods["POST"] || !methods["DELETE"] {
		t.Errorf("expected one candidate per method, got %v", methods)
	}

	var collisions int
	for _, u := range unresolved {
		if u.Kind == "rails_helper_collision" {
			collisions++
		}
	}
	if collisions != 1 {
		t.Errorf("expected exactly 1 rails_helper_collision ledger entry, got %d", collisions)
	}
}

// TestResolveRailsNavHelpers_SingleMatchKeepsID guards the ordinary path: a
// helper naming one route must still resolve in place, with no candidate suffix
// anywhere in sight.
func TestResolveRailsNavHelpers_SingleMatchKeepsID(t *testing.T) {
	t.Parallel()
	nodes := []graph.Node{
		makeRouteNode("r1", "svc", "routes.rb", 1, "reports", "GET", "/app/reports"),
		makeNavHelperNode("c1", "svc", "views/index.erb", 5, "reports_path"),
	}

	updated, _ := ResolveRailsNavHelpers(nodes)
	if len(updated) != 1 || updated[0].ID != "c1" {
		t.Fatalf("expected exactly one in-place update of c1, got %v", updated)
	}
	if updated[0].Meta["via"] == "rails_helper_candidate" {
		t.Error("a single match must not be marked as a fan-out candidate")
	}
	if updated[0].Label != "GET /app/reports" {
		t.Errorf("label = %q, want the resolved route", updated[0].Label)
	}
}

func sortedIDs(m map[string]graph.Node) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
