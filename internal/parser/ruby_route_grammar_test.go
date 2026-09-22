package parser

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	rubysitter "github.com/smacker/go-tree-sitter/ruby"

	"github.com/lordsonvimal/polyflow/internal/patterns/scopefold"
)

// This file parity-tests railsRouteGrammar + buildRailsMatches (Tier SF
// Phase 2's path/method/action composition slice) against the exact fixtures
// TestComposeRailsRoutePaths_NestedNamespaceAndMemberCollection and
// TestComposeRailsRoutePaths_InlineOnMemberCollection (ruby_route_paths_test.go)
// already prove composeRailsRoutePaths against — the byte-parity gate the
// plan doc's §8 requires before any migration, scoped for now to what this
// increment actually covers (route-name/controller-override/devise/root/
// resource-scoped-verb synthesis are not yet in railsRouteGrammar; see its
// doc comment).
func foldRailsRoutes(t *testing.T, src string) []string {
	t.Helper()
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, []byte(src))
	if err != nil || tree == nil {
		t.Fatalf("parse: %v", err)
	}
	matches := buildRailsMatches(tree.RootNode(), "config/routes.rb", []byte(src))
	facts := scopefold.Fold(tree.RootNode(), matches, railsRouteGrammar())
	var out []string
	for _, f := range facts {
		if f.Pred != "rails_route" {
			continue
		}
		out = append(out, f.Args[1].Value()+" "+f.Args[0].Value())
	}
	return out
}

func containsRoute(routes []string, want string) bool {
	for _, r := range routes {
		if r == want {
			return true
		}
	}
	return false
}

// TestScopeFoldRails_NestedNamespaceAndMemberCollection parity-tests the
// collection/member composition half of
// TestComposeRailsRoutePaths_NestedNamespaceAndMemberCollection (the
// on: :member literal-path insertion for line 15's http_verb_route is a
// known gap — not yet in railsRouteGrammar, see its doc comment).
func TestScopeFoldRails_NestedNamespaceAndMemberCollection(t *testing.T) {
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
    end
  end
end
`
	routes := foldRailsRoutes(t, src)

	want := []string{
		"GET /client_api/v1/folders/details_by_path",
		"POST /client_api/v1/folders/:folder_id/files/:id/copy",
		"GET /client_api/v1/studies",
	}
	for _, w := range want {
		if !containsRoute(routes, w) {
			t.Errorf("missing route %q, got %v", w, routes)
		}
	}
}

// TestScopeFoldRails_InlineOnMemberCollection parity-tests
// TestComposeRailsRoutePaths_InlineOnMemberCollection exactly.
func TestScopeFoldRails_InlineOnMemberCollection(t *testing.T) {
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
	routes := foldRailsRoutes(t, src)

	want := []string{
		"GET /client_api/v1/folders/:id/children",
		"GET /client_api/v1/folders/details_by_path",
	}
	if len(routes) != len(want) {
		t.Fatalf("route count = %d, want %d: %v", len(routes), len(want), routes)
	}
	for _, w := range want {
		if !containsRoute(routes, w) {
			t.Errorf("missing route %q, got %v", w, routes)
		}
	}
}

// TestScopeFoldRails_ScopePrefix parity-tests the `scope "app" do ... end`
// shape scopeSegments' own doc comment calls out as orion's dominant idiom
// (~400 routes) — path: from the positional argument, no module
// contribution.
func TestScopeFoldRails_ScopePrefix(t *testing.T) {
	src := `Rails.application.routes.draw do
  scope "app" do
    get "/audit_logs", to: "audit_logs#index"
  end
end
`
	routes := foldRailsRoutes(t, src)
	if !containsRoute(routes, "GET /app/audit_logs") {
		t.Errorf("missing route %q, got %v", "GET /app/audit_logs", routes)
	}
}
