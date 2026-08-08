package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/patterns"
	"github.com/stretchr/testify/require"
)

const rbPatternsDir = "../../patterns"

// parseRubyRoutes runs the RubyParser on inline routes.rb source — the
// filename must be routes.rb (or under config/routes/) for the Tier R
// composeRailsRoutePaths gate to fire.
func parseRubyRoutes(t *testing.T, src string) []graph.Node {
	t.Helper()
	dir := t.TempDir()
	file := filepath.Join(dir, "routes.rb")
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))
	reg, err := patterns.DefaultRegistry(rbPatternsDir)
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	p := &RubyParser{}
	nodes, _, _, err := p.Parse(file, "svc", m)
	require.NoError(t, err)
	return nodes
}

func routeNode(nodes []graph.Node, pattern string) []*graph.Node {
	var out []*graph.Node
	for i := range nodes {
		if nodes[i].Type == graph.NodeTypeHTTPHandler && nodes[i].Meta["pattern"] == pattern {
			out = append(out, &nodes[i])
		}
	}
	return out
}

// TestComposeRailsRoutePaths_NestedNamespaceAndMemberCollection is the
// worked example from docs/rails-route-path-composition-plan.md: nested
// namespace/resources composing a prefix, a symbol-based collection action
// (no path capture pre-Tier-R), and a nested resource's member action.
func TestComposeRailsRoutePaths_NestedNamespaceAndMemberCollection(t *testing.T) {
	src := `Rails.application.routes.draw do
  namespace :client_api do
    namespace :v1 do
      resources :folders do
        collection do
          get :details_by_path
        end
        resources :files do
          member do
            post :copy
          end
        end
      end
      get "/studies", to: "studies#index"
      get "/studies/:id", to: "studies#show", on: :member
    end
  end
end
`
	nodes := parseRubyRoutes(t, src)

	collection := routeNode(nodes, "collection_verb_route")
	require.Len(t, collection, 1)
	require.Equal(t, "/client_api/v1/folders/details_by_path", collection[0].Meta["path"])
	require.Equal(t, "GET", collection[0].Meta["method"])

	// resources :files nests directly under resources :folders, so Rails scopes
	// it under the parent's member parameter: /folders/:folder_id/files/:id/copy.
	// Tier K.1 closed what the route-composition plan had listed as a non-goal —
	// without the parent id, nested paths came out a segment short and could not
	// match a caller's URL.
	member := routeNode(nodes, "member_verb_route")
	require.Len(t, member, 1)
	require.Equal(t, "/client_api/v1/folders/:folder_id/files/:id/copy", member[0].Meta["path"])

	verb := routeNode(nodes, "http_verb_route")
	require.Len(t, verb, 2)
	byLine := map[int]*graph.Node{}
	for _, n := range verb {
		byLine[n.Line] = n
	}
	require.Equal(t, "/client_api/v1/studies", byLine[14].Meta["path"])
	require.Equal(t, "/client_api/v1/:id/studies/:id", byLine[15].Meta["path"])
}

// TestComposeRailsRoutePaths_InlineOnMemberCollection covers the bare
// `get :action, on: :member`/`on: :collection` form — measured as the
// *dominant* Rails route idiom in the real orion corpus (80 occurrences vs.
// 56 explicit member/collection blocks), and the exact shape of the
// motivating GetFolderByPath → orion edge from
// docs/sprintf-url-resolution-plan.md (Tier X.11).
func TestComposeRailsRoutePaths_InlineOnMemberCollection(t *testing.T) {
	src := `Rails.application.routes.draw do
  namespace :client_api do
    namespace :v1 do
      resources :folders do
        get :children, on: :member
        get :details_by_path, on: :collection
      end
    end
  end
end
`
	nodes := parseRubyRoutes(t, src)

	member := routeNode(nodes, "member_verb_route_inline")
	require.Len(t, member, 1)
	require.Equal(t, "/client_api/v1/folders/:id/children", member[0].Meta["path"])
	require.Equal(t, "GET", member[0].Meta["method"])

	collection := routeNode(nodes, "collection_verb_route_inline")
	require.Len(t, collection, 1)
	require.Equal(t, "/client_api/v1/folders/details_by_path", collection[0].Meta["path"])
	require.Equal(t, "GET", collection[0].Meta["method"])
}

// parseRubyAt runs the RubyParser on inline source at a caller-chosen path
// relative to a temp dir, so the routes-file gates can be exercised directly.
func parseRubyAt(t *testing.T, rel, src string) []graph.Node {
	t.Helper()
	file := filepath.Join(t.TempDir(), filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))
	reg, err := patterns.DefaultRegistry(rbPatternsDir)
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	p := &RubyParser{}
	nodes, _, _, err := p.Parse(file, "svc", m)
	require.NoError(t, err)
	return nodes
}

// TestComposeRailsRoutePaths_SkipsNonRoutesFile confirms the filename gate:
// the exact same source, parsed under a non-routes.rb filename, composes
// nothing. Since Tier HH.1 the receiverless verb call is not admitted as a
// route node at all, so what remains to assert is that `resources` does not
// synthesize its implicit REST surface outside a routes file.
func TestComposeRailsRoutePaths_SkipsNonRoutesFile(t *testing.T) {
	nodes := parseRubyAt(t, "not_routes.rb", `Rails.application.routes.draw do
  namespace :client_api do
    resources :folders do
      collection do
        get :details_by_path
      end
    end
  end
end
`)
	require.Empty(t, routeNode(nodes, "collection_verb_route"))
	require.Empty(t, routeNode(nodes, "rest_resource_route"))
	for _, n := range nodes {
		if n.Type == graph.NodeTypeHTTPHandler {
			require.Empty(t, n.Meta["path"], "no route path may compose outside a routes file: %+v", n)
		}
	}
}

// TestRouteNodesOnlyInRoutesFiles is the Tier HH.1 gate (worked example:
// orion/app/services/atlas/user_category_rules_client.rb:22). A receiverless
// `get("…")` in a service object calls the private helper named `get` defined
// below it — it is an outbound HTTP call, not a route declaration.
func TestRouteNodesOnlyInRoutesFiles(t *testing.T) {
	src := `module Cam
  class UserCategoryRulesClient
    def show(id)
      get("#{base_url}/client_api/v1/user_category_rules/#{id}")
    end

    def get(url)
      RestClient::Request.execute(method: :get, url: url)
    end
  end
end
`
	nodes := parseRubyAt(t, "app/services/foo_client.rb", src)
	for _, n := range nodes {
		require.NotEqual(t, graph.NodeTypeHTTPHandler, n.Type,
			"a service object must not declare routes: %+v", n)
	}

	// The same text under config/routes.rb is a route declaration and is kept.
	routes := parseRubyAt(t, "config/routes.rb", src)
	require.NotEmpty(t, routeNode(routes, "http_verb_route"))
}

// TestRoutesConcernsFileStillAdmitted guards against over-tightening the gate
// to a literal routes.rb: Rails' routes-concerns convention puts real route
// files under config/routes/.
func TestRoutesConcernsFileStillAdmitted(t *testing.T) {
	nodes := parseRubyAt(t, "config/routes/admin.rb", `namespace :admin do
  get "reports", to: "reports#index"
end
`)
	got := routeNode(nodes, "http_verb_route")
	require.Len(t, got, 1)
	require.Equal(t, "/admin/reports", got[0].Meta["path"])
}

// TestLiteralPathRouteStillDroppedOutsideRoutesFile: the gate is positional,
// not evidence-based. A receiverless verb call with a fully literal path in a
// service object is still a helper call, not a route.
func TestLiteralPathRouteStillDroppedOutsideRoutesFile(t *testing.T) {
	nodes := parseRubyAt(t, "app/services/foo_client.rb", `class FooClient
  def index
    get("/client_api/v1/user_category_rules")
  end
end
`)
	for _, n := range nodes {
		require.NotEqual(t, graph.NodeTypeHTTPHandler, n.Type, "phantom route survived: %+v", n)
	}
}

// TestComposeRailsRoutePaths_Determinism: two runs produce byte-identical
// composed paths.
func TestComposeRailsRoutePaths_Determinism(t *testing.T) {
	src := `Rails.application.routes.draw do
  namespace :client_api do
    resources :folders do
      collection do
        get :details_by_path
      end
    end
  end
end
`
	first := parseRubyRoutes(t, src)
	second := parseRubyRoutes(t, src)

	f := routeNode(first, "collection_verb_route")
	s := routeNode(second, "collection_verb_route")
	require.Len(t, f, 1)
	require.Len(t, s, 1)
	require.Equal(t, f[0].Meta["path"], s[0].Meta["path"])
	require.Equal(t, "/client_api/folders/details_by_path", f[0].Meta["path"])
}

// TestRESTResourceRoutes_ImplicitActions is the Tier K.1 guard for the routes
// Rails declares without naming them.
//
// `resources :users` writes no verb call, so before K.1 there was no node at all
// for `GET /client_api/v1/users` — the endpoint maple-manager's Vega user-sync
// adapter actually calls. Its client-side URL resolved perfectly and still matched
// nothing, because the handler side of the join did not exist.
func TestRESTResourceRoutes_ImplicitActions(t *testing.T) {
	nodes := parseRubyRoutes(t, `
Rails.application.routes.draw do
  namespace :client_api do
    namespace :v1 do
      resources :users do
        get :test_connection, on: :collection
      end
      resources :organizations, only: %i[index show]
      resources :sessions, except: [:destroy]
      resource :profile
      resources :studies do
        resources :user_study_roles
      end
    end
  end
end
`)

	got := map[string]bool{}
	for _, n := range routeNode(nodes, "rest_resource_route") {
		got[n.Label] = true
		require.Equal(t, "ruby", n.Language)
		require.NotEmpty(t, n.Meta["action"])
	}

	// The full default set for a plain `resources`.
	for _, want := range []string{
		"GET /client_api/v1/users",
		"POST /client_api/v1/users",
		"GET /client_api/v1/users/new",
		"GET /client_api/v1/users/:id",
		"GET /client_api/v1/users/:id/edit",
		"PATCH /client_api/v1/users/:id",
		"PUT /client_api/v1/users/:id",
		"DELETE /client_api/v1/users/:id",
	} {
		require.True(t, got[want], "missing implicit route %q", want)
	}

	// only: restricts to exactly the listed actions.
	require.True(t, got["GET /client_api/v1/organizations"])
	require.True(t, got["GET /client_api/v1/organizations/:id"])
	require.False(t, got["POST /client_api/v1/organizations"], "only: must exclude create")
	require.False(t, got["DELETE /client_api/v1/organizations/:id"], "only: must exclude destroy")

	// except: removes just the listed actions.
	require.True(t, got["GET /client_api/v1/sessions"])
	require.False(t, got["DELETE /client_api/v1/sessions/:id"], "except: must exclude destroy")

	// A singular resource has no index and no :id.
	require.True(t, got["GET /client_api/v1/profile"])
	require.False(t, got["GET /client_api/v1/profile/:id"], "singular resource takes no :id")

	// A nested resource scopes under the parent's member parameter.
	require.True(t, got["GET /client_api/v1/studies/:study_id/user_study_roles"],
		"nested resource must carry the parent's :study_id")
}

// TestRESTResourceRoutes_EmptyOnlyGeneratesNothing: `resources :users, only: []`
// declares a nesting scope, not an endpoint.
//
// orion-vega-agent uses exactly this shape (config/routes.rb:44). Reading the
// empty list as "no filter" invented `POST /api/v1/users` there, which then
// collided with the Atlas user API that maple-manager and willow really call —
// three cross-service edges to a service that exposes no such route.
func TestRESTResourceRoutes_EmptyOnlyGeneratesNothing(t *testing.T) {
	nodes := parseRubyRoutes(t, `
Rails.application.routes.draw do
  namespace :api do
    namespace :v1 do
      resources :users, only: [] do
        post :sync, on: :collection
      end
    end
  end
end
`)
	for _, n := range routeNode(nodes, "rest_resource_route") {
		t.Errorf("only: [] must generate no implicit route, got %q", n.Label)
	}

	// The explicitly declared action is unaffected.
	inline := routeNode(nodes, "collection_verb_route_inline")
	require.Len(t, inline, 1)
	require.Equal(t, "/api/v1/users/sync", inline[0].Meta["path"])
}

// TestRESTResourceRoutes_Deterministic: the synthesized set is byte-identical
// across runs (bug-class #2).
func TestRESTResourceRoutes_Deterministic(t *testing.T) {
	src := `
Rails.application.routes.draw do
  namespace :client_api do
    resources :users
    resources :folders do
      resources :files
    end
  end
end
`
	// Labels, not IDs: parseRubyRoutes writes to a fresh t.TempDir() per call, and
	// the node ID embeds the file path.
	labels := func() []string {
		var out []string
		for _, n := range routeNode(parseRubyRoutes(t, src), "rest_resource_route") {
			out = append(out, n.Label)
		}
		return out
	}
	first, second := labels(), labels()
	require.NotEmpty(t, first)
	require.Equal(t, first, second)
}

// TestSingularize covers the inflections used for nested-resource parameters.
func TestSingularize(t *testing.T) {
	for in, want := range map[string]string{
		"folders":  "folder",
		"files":    "file",
		"studies":  "study",
		"boxes":    "box",
		"branches": "branch",
		"status":   "status",
		"user":     "user",
	} {
		require.Equal(t, want, singularize(in), "singularize(%q)", in)
	}
}

// TestBareStringRouteVerbUpcased pins the Tier HH.2 normalization: a
// bare-string route captures @method verbatim, so it used to keep Ruby's
// lowercase `post` while every other handler in the graph carries `POST`.
func TestBareStringRouteVerbUpcased(t *testing.T) {
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  post "login", to: "sessions#create"
end
`)
	got := routeNode(nodes, "http_verb_route")
	require.Len(t, got, 1)
	require.Equal(t, "POST", got[0].Meta["method"])
	require.Equal(t, "/login", got[0].Meta["path"])
}

// TestBareStringRouteLabelRefreshed: the label is minted from the raw capture
// at node-creation time, before composeRailsRoutePaths rewrites the path, so it
// read "patch Users/:id" — neither the composed path nor the graph-wide
// "METHOD /path" shape an agent searches for.
func TestBareStringRouteLabelRefreshed(t *testing.T) {
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  post "login", to: "sessions#create"
  namespace :scim do
    namespace :v2 do
      patch "Users/:id", to: "users#update"
    end
  end
end
`)
	labels := map[string]bool{}
	for _, n := range routeNode(nodes, "http_verb_route") {
		labels[n.Label] = true
	}
	require.True(t, labels["POST /login"], "got labels %v", labels)
	require.True(t, labels["PATCH /scim/v2/Users/:id"], "got labels %v", labels)
}

// TestComposeRoutePathsIdempotent guards the failure mode the Go route-group
// twin shipped once: re-composing an already-composed node treats its composed
// path as a fresh literal and prepends the prefix again
// ("/api/v1/api/v1/users").
func TestComposeRoutePathsIdempotent(t *testing.T) {
	src := `Rails.application.routes.draw do
  namespace :api do
    namespace :v1 do
      get "users/:id", to: "users#show"
      post "users", to: "users#create"
    end
  end
end
`
	dir := t.TempDir()
	file := filepath.Join(dir, "routes.rb")
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))
	nodes := parseRubyRoutes(t, src)

	snapshot := func() []string {
		var out []string
		for _, n := range routeNode(nodes, "http_verb_route") {
			out = append(out, n.Label+"|"+n.Meta["method"]+"|"+n.Meta["path"])
		}
		return out
	}
	before := snapshot()
	require.ElementsMatch(t,
		[]string{"GET /api/v1/users/:id|GET|/api/v1/users/:id", "POST /api/v1/users|POST|/api/v1/users"},
		before)

	composeRailsRoutePaths(file, "svc", []byte(src), nodes)
	require.Equal(t, before, snapshot())
}

// ─── Tier HH.3: route scaffolding is not an endpoint ────────────────────────

// typedNodes returns every node of a given type, whatever its pattern.
// routeNode cannot be reused here: it hard-filters on NodeTypeHTTPHandler,
// which is precisely the property these tests are about.
func typedNodes(nodes []graph.Node, typ graph.NodeType) []*graph.Node {
	var out []*graph.Node
	for i := range nodes {
		if nodes[i].Type == typ {
			out = append(out, &nodes[i])
		}
	}
	return out
}

// TestRakeNamespaceIsNotARoute pins the largest single population of fake
// handlers at HEAD: 85 of the fleet's 100 namespace_route nodes came from
// `namespace :db do` in a .rake file. That is Rake's *task* namespace — a
// different DSL that happens to share a method name with Rails' route scope —
// and it has nothing to do with HTTP. HH.1's gate did not cover it, because
// namespace_route's query matches a call regardless of receiver.
func TestRakeNamespaceIsNotARoute(t *testing.T) {
	nodes := parseRubyAt(t, "lib/tasks/db_hardening.rake", `namespace :db do
  namespace :hardening do
    task :backfill => :environment do
      puts "backfilling"
    end
  end
end
`)
	require.Empty(t, typedNodes(nodes, graph.NodeTypeHTTPHandler),
		"a Rake task namespace is not an endpoint")
	require.Empty(t, typedNodes(nodes, graph.NodeTypeRouteGroup),
		"nor is it route scaffolding — it must not be admitted at all")
}

// TestResourcesRouteIsRouteGroupNotHandler is the core HH.3 retyping: the
// `resources :users` declaration itself is a group, while the seven routes it
// implicitly declares are real, callable endpoints and stay http_handler.
func TestResourcesRouteIsRouteGroupNotHandler(t *testing.T) {
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  resources :users
end
`)

	groups := typedNodes(nodes, graph.NodeTypeRouteGroup)
	require.Len(t, groups, 1, "got %v", groups)
	require.Equal(t, "resources_route", groups[0].Meta["pattern"])

	// Every remaining handler is a synthesized REST route, and each one has the
	// method+path a client can actually be matched against — the property that
	// makes it an endpoint and the group not one.
	// Eight, not seven: `resources` declares seven *actions*, but `update` is
	// reachable by both PATCH and PUT, so it contributes two routes.
	handlers := typedNodes(nodes, graph.NodeTypeHTTPHandler)
	require.Len(t, handlers, 8, "the default REST surface must survive")
	for _, h := range handlers {
		require.Equal(t, "rest_resource_route", h.Meta["pattern"])
		require.NotEmpty(t, h.Meta["method"], "endpoint without a method: %+v", h)
		require.NotEmpty(t, h.Meta["path"], "endpoint without a path: %+v", h)
	}
}

// TestRouteGroupStillComposesPrefix is the regression that matters. The
// scaffolding nodes are load-bearing for path composition, so retyping them
// must change their Type and nothing else: `namespace :api` / `namespace :v1`
// / `resources :users` still has to compose to /api/v1/users.
func TestRouteGroupStillComposesPrefix(t *testing.T) {
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  namespace :api do
    namespace :v1 do
      resources :users
      get "health", to: "health#show"
    end
  end
end
`)

	got := map[string]bool{}
	for _, h := range typedNodes(nodes, graph.NodeTypeHTTPHandler) {
		got[h.Label] = true
	}
	for _, want := range []string{
		"GET /api/v1/users",
		"POST /api/v1/users",
		"GET /api/v1/users/:id",
		"DELETE /api/v1/users/:id",
		"GET /api/v1/health",
	} {
		require.True(t, got[want], "prefix composition broke; missing %q, got %v", want, got)
	}

	// Both namespaces plus the resources declaration are groups, and no group
	// carries a composed path — composition writes to the routes, not to the
	// scaffolding that scoped them.
	groups := typedNodes(nodes, graph.NodeTypeRouteGroup)
	require.Len(t, groups, 3)
	for _, g := range groups {
		require.Empty(t, g.Meta["path"], "a group is not an endpoint: %+v", g)
	}
}
