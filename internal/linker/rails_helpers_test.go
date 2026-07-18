package linker

import (
	"sort"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// makeRouteNode is a test helper that builds a ruby route node.
func makeRouteNode(id, svc, file string, line int, pattern string, meta map[string]string) graph.Node {
	if meta == nil {
		meta = map[string]string{}
	}
	meta["pattern"] = pattern
	return graph.Node{
		ID:       id,
		Type:     graph.NodeTypeHTTPHandler,
		Service:  svc,
		File:     file,
		Line:     line,
		Language: "ruby",
		Meta:     meta,
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

// TestBuildRailsHelperMap_Resources verifies RESTful helper generation.
func TestBuildRailsHelperMap_Resources(t *testing.T) {
	nodes := []graph.Node{
		makeRouteNode("n1", "svc", "config/routes.rb", 2, "resources_route",
			map[string]string{"resource": ":reports"}),
	}
	m := BuildRailsHelperMap(nodes)
	svc := m["svc"]

	// Index (GET /reports)
	assertHelperGet(t, svc, "reports_path", "GET", "/reports")
	// Create (POST /reports)
	assertHelperGet(t, svc, "reports_path", "POST", "/reports")
	// Show (GET /reports/:id)
	assertHelperGet(t, svc, "report_path", "GET", "/reports/:id")
	// New
	assertHelperGet(t, svc, "new_report_path", "GET", "/reports/new")
	// Edit
	assertHelperGet(t, svc, "edit_report_path", "GET", "/reports/:id/edit")
	// _url aliases
	if len(svc["reports_url"]) == 0 {
		t.Error("missing reports_url alias")
	}
}

// TestBuildRailsHelperMap_SingularResource verifies resource (singular) helper generation.
func TestBuildRailsHelperMap_SingularResource(t *testing.T) {
	nodes := []graph.Node{
		makeRouteNode("n1", "svc", "config/routes.rb", 2, "resource_route",
			map[string]string{"resource": ":profile"}),
	}
	m := BuildRailsHelperMap(nodes)
	svc := m["svc"]

	assertHelperGet(t, svc, "profile_path", "GET", "/profile")
	assertHelperGet(t, svc, "new_profile_path", "GET", "/profile/new")
	assertHelperGet(t, svc, "edit_profile_path", "GET", "/profile/edit")

	// Singular resource has NO :id in paths.
	for _, r := range svc["profile_path"] {
		if r.Path == "/profiles/:id" || r.Path == "/profile/:id" {
			t.Errorf("singular resource must not have :id path; got %s %s", r.Method, r.Path)
		}
	}
}

// TestBuildRailsHelperMap_MemberCollection verifies member/collection route helpers.
func TestBuildRailsHelperMap_MemberCollection(t *testing.T) {
	nodes := []graph.Node{
		makeRouteNode("n1", "svc", "config/routes.rb", 2, "resources_route",
			map[string]string{"resource": ":reports"}),
		// member do; get :archive; end  (line 3 = inside the resources block)
		makeRouteNode("n2", "svc", "config/routes.rb", 3, "member_verb_route",
			map[string]string{"verb": "get", "action": ":archive"}),
		// collection do; get :recent; end
		makeRouteNode("n3", "svc", "config/routes.rb", 6, "collection_verb_route",
			map[string]string{"verb": "get", "action": ":recent"}),
	}
	m := BuildRailsHelperMap(nodes)
	svc := m["svc"]

	// member: archive_report_path → GET /reports/:id/archive
	assertHelperGet(t, svc, "archive_report_path", "GET", "/reports/:id/archive")
	// collection: recent_reports_path → GET /reports/recent
	assertHelperGet(t, svc, "recent_reports_path", "GET", "/reports/recent")
}

// TestBuildRailsHelperMap_FanOutCollision verifies that two resources with the
// same singular name (collision across namespaces) each get a helper entry (rule 1).
func TestBuildRailsHelperMap_FanOutCollision(t *testing.T) {
	nodes := []graph.Node{
		makeRouteNode("n1", "svc", "routes.rb", 1, "resources_route",
			map[string]string{"resource": ":users"}),
		makeRouteNode("n2", "svc", "routes.rb", 10, "resources_route",
			map[string]string{"resource": ":users"}), // same name, different block
	}
	m := BuildRailsHelperMap(nodes)
	svc := m["svc"]

	// Both entries must produce routes — fan-out means ≥2 entries for users_path.
	routes := svc["users_path"]
	if len(routes) < 2 {
		t.Errorf("fan-out: expected ≥2 routes for users_path, got %d", len(routes))
	}
}

// TestBuildRailsHelperMap_Determinism verifies that running twice produces identical output (rule 2).
func TestBuildRailsHelperMap_Determinism(t *testing.T) {
	nodes := []graph.Node{
		makeRouteNode("n1", "svc", "routes.rb", 1, "resources_route",
			map[string]string{"resource": ":reports"}),
		makeRouteNode("n2", "svc", "routes.rb", 2, "member_verb_route",
			map[string]string{"verb": "get", "action": ":archive"}),
		makeRouteNode("n3", "svc", "routes.rb", 5, "resources_route",
			map[string]string{"resource": ":users"}),
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
	nodes := []graph.Node{
		makeRouteNode("r1", "svc", "routes.rb", 1, "resources_route",
			map[string]string{"resource": ":reports"}),
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
		if n.ID == "c1" && n.Meta["path"] == "/reports" && n.Meta["method"] == "GET" {
			found = true
		}
	}
	if !found {
		t.Errorf("updated node for reports_path not found or has wrong meta; got: %+v", updated)
	}
}

// TestResolveRailsNavHelpers_Unresolved verifies that unknown helpers go to the ledger.
func TestResolveRailsNavHelpers_Unresolved(t *testing.T) {
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
	nodes := []graph.Node{
		makeRouteNode("r1", "svc", "routes.rb", 1, "resources_route",
			map[string]string{"resource": ":reports"}),
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
	nodes := []graph.Node{
		makeRouteNode("r1", "svc", "routes.rb", 1, "resources_route",
			map[string]string{"resource": ":reports"}),
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

// TestSingularize covers the most common English plural forms.
func TestSingularize(t *testing.T) {
	cases := []struct{ plural, want string }{
		{"reports", "report"},
		{"users", "user"},
		{"categories", "category"},
		{"buses", "bus"},
		{"branches", "branch"},
		{"people", "person"},
		{"media", "medium"},
	}
	for _, c := range cases {
		got := singularize(c.plural)
		if got != c.want {
			t.Errorf("singularize(%q) = %q, want %q", c.plural, got, c.want)
		}
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
