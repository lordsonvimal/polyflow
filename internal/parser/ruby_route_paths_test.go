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
// *dominant* Rails route idiom in the real nextGen corpus (80 occurrences vs.
// 56 explicit member/collection blocks), and the exact shape of the
// motivating GetFolderByPath → nextGen edge from
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

// TestComposeRailsRoutePaths_SkipsNonRoutesFile confirms the filename gate:
// the exact same source, parsed under a non-routes.rb filename, must not
// have its Meta["path"] rewritten.
func TestComposeRailsRoutePaths_SkipsNonRoutesFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not_routes.rb")
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
	require.NoError(t, os.WriteFile(file, []byte(src), 0o644))
	reg, err := patterns.DefaultRegistry(rbPatternsDir)
	require.NoError(t, err)
	m := patterns.NewTreeSitterMatcher(reg)
	p := &RubyParser{}
	nodes, _, _, err := p.Parse(file, "svc", m)
	require.NoError(t, err)

	collection := routeNode(nodes, "collection_verb_route")
	require.Len(t, collection, 1)
	require.Empty(t, collection[0].Meta["path"])
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
// for `GET /client_api/v1/users` — the endpoint dsw-manager's CDR user-sync
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
// nextGen-CDR-Agent uses exactly this shape (config/routes.rb:44). Reading the
// empty list as "no filter" invented `POST /api/v1/users` there, which then
// collided with the CAM user API that dsw-manager and mysycamore really call —
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
