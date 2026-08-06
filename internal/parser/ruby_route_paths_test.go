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

	// resources :files nests directly under resources :folders (no explicit
	// member/collection wrapping of folders' own files action), so only the
	// files resource's own member block contributes an :id — Rails' implicit
	// parent-resource :id insertion for bare nested `resources` is a
	// documented non-goal (docs/rails-route-path-composition-plan.md).
	member := routeNode(nodes, "member_verb_route")
	require.Len(t, member, 1)
	require.Equal(t, "/client_api/v1/folders/files/:id/copy", member[0].Meta["path"])

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
