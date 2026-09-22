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

// foldRailsRouteNames is foldRailsRoutes' route_helper-carrying twin, for the
// route-name parity tests below — mirrors ruby_route_names_test.go's namesOf.
func foldRailsRouteNames(t *testing.T, src string) map[string]string {
	t.Helper()
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, []byte(src))
	if err != nil || tree == nil {
		t.Fatalf("parse: %v", err)
	}
	matches := buildRailsMatches(tree.RootNode(), "config/routes.rb", []byte(src))
	facts := scopefold.Fold(tree.RootNode(), matches, railsRouteGrammar())
	out := map[string]string{}
	for _, f := range facts {
		if f.Pred != "rails_route" {
			continue
		}
		out[f.Args[1].Value()+" "+f.Args[0].Value()] = f.Args[2].Value()
	}
	return out
}

// TestScopeFoldRails_RouteNames parity-tests railsRouteGrammar's route_helper
// composition against ruby_route_names_test.go's fixtures (nameScope's own
// ground truth) — nested resources singularization, member-vs-collection
// naming, as:/on: overrides, the collection_name `_index` disambiguation, and
// the literal-path lexical-member-block fallback.
func TestScopeFoldRails_RouteNames(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want map[string]string
	}{
		{"ScopeContributesNoName", `Rails.application.routes.draw do
  scope "app" do
    resources :folders
  end
end`, map[string]string{
			"GET /app/folders": "folders", "GET /app/folders/:id": "folder",
			"GET /app/folders/new": "new_folder", "GET /app/folders/:id/edit": "edit_folder",
			"POST /app/folders": "folders", "DELETE /app/folders/:id": "folder",
		}},
		{"NamespaceContributesToBoth", `Rails.application.routes.draw do
  namespace :client_api do
    namespace :v1 do
      resources :users
    end
  end
end`, map[string]string{
			"GET /client_api/v1/users":     "client_api_v1_users",
			"GET /client_api/v1/users/:id": "client_api_v1_user",
			"GET /client_api/v1/users/new": "new_client_api_v1_user",
		}},
		{"NestedParentIsSingularized", `Rails.application.routes.draw do
  scope "app" do
    resources :studies do
      resources :deliverables
    end
  end
end`, map[string]string{
			"GET /app/studies/:study_id/deliverables":          "study_deliverables",
			"GET /app/studies/:study_id/deliverables/:id":      "study_deliverable",
			"GET /app/studies/:study_id/deliverables/:id/edit": "edit_study_deliverable",
		}},
		{"MemberIsSingularCollectionIsPlural", `Rails.application.routes.draw do
  scope "app" do
    resources :users do
      member do
        get :sync
      end
      collection do
        get :recent
      end
    end
  end
end`, map[string]string{
			"GET /app/users/:id/sync": "sync_user", "GET /app/users/recent": "recent_users",
		}},
		{"InlineOnMemberAndCollection", `Rails.application.routes.draw do
  resources :change_logs, only: %i[index] do
    get :export, on: :collection
    get :details, on: :member
  end
end`, map[string]string{
			"GET /change_logs/export":      "export_change_logs",
			"GET /change_logs/:id/details": "details_change_log",
		}},
		{"LiteralVerbRouteUsesThePathNotTheScope", `Rails.application.routes.draw do
  scope "app" do
    get "audit_logs", to: "change_logs#audit_logs"
  end
end`, map[string]string{"GET /app/audit_logs": "audit_logs"}},
		{"AsOverridesTheName", `Rails.application.routes.draw do
  resources :studies do
    resources :task_reports, only: [] do
      post "/", on: :collection, to: "task_reports#index", as: "collection"
    end
  end
end`, map[string]string{"POST /studies/:study_id/task_reports": "collection_study_task_reports"}},
		{"AsOnResourcesRenamesWithoutMovingTheURL", `Rails.application.routes.draw do
  resources :studies, as: :containers
end`, map[string]string{"GET /studies": "containers", "GET /studies/:id": "container"}},
		{"SiblingScopesDoNotBleed", `Rails.application.routes.draw do
  namespace :admin do
    resources :users
  end
  namespace :api do
    resources :users
  end
  resources :users
end`, map[string]string{
			"GET /admin/users": "admin_users", "GET /api/users": "api_users", "GET /users": "users",
		}},
		{"UncountableResourceGetsIndexSuffix", `Rails.application.routes.draw do
  namespace :organization_admin do
    resources :sso
  end
  resources :help, only: [:index, :show]
end`, map[string]string{
			"GET /organization_admin/sso":        "organization_admin_sso_index",
			"POST /organization_admin/sso":       "organization_admin_sso_index",
			"GET /organization_admin/sso/:id":    "organization_admin_sso",
			"DELETE /organization_admin/sso/:id": "organization_admin_sso",
			"GET /organization_admin/sso/new":    "new_organization_admin_sso",
			"GET /help":                          "help_index",
			"GET /help/:id":                      "help",
		}},
		{"CountableResourceUnaffected", `Rails.application.routes.draw do
  resources :folders
  resources :studies
end`, map[string]string{
			"GET /folders": "folders", "GET /folders/:id": "folder",
			"GET /studies": "studies", "GET /studies/:id": "study",
		}},
		{"SingletonKeepsSingularName", `Rails.application.routes.draw do
  resource :session, only: [:new, :create, :destroy]
  resource :sso, only: [:show]
end`, map[string]string{
			"POST /session": "session", "DELETE /session": "session",
			"GET /session/new": "new_session", "GET /sso": "sso",
		}},
		{"UncountableCollectionBlock", `Rails.application.routes.draw do
  resources :sso do
    collection do
      get :bulk
    end
    member do
      get :audit
    end
  end
end`, map[string]string{
			"GET /sso/bulk": "bulk_sso_index", "GET /sso/:id/audit": "audit_sso",
		}},
		{"StringActionInMemberBlockIsPrefixed", `Rails.application.routes.draw do
  resources :standards do
    resources :standard_export_templates, only: [:index] do
      member { get "download" }
    end
    collection { get "recent" }
  end
end`, map[string]string{
			"GET /standards/:standard_id/standard_export_templates/:id/download": "download_standard_standard_export_template",
			"GET /standards/recent": "recent_standards",
		}},
		{"LiteralRouteOutsideAnyBlockUnchanged", `Rails.application.routes.draw do
  scope "app" do
    get "audit_logs", to: "audits#index"
  end
  resources :folders do
    get "sibling", to: "folders#sibling"
  end
end`, map[string]string{
			"GET /app/audit_logs": "audit_logs", "GET /folders/sibling": "folder_sibling",
		}},
		{"DynamicLiteralPathIsUnnamed", `Rails.application.routes.draw do
  get "files/:id/raw", to: "files#raw"
end`, map[string]string{"GET /files/:id/raw": ""}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := foldRailsRouteNames(t, tc.src)
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("%s: route_helper[%q] = %q, want %q (all: %v)", tc.name, k, got[k], want, got)
				}
			}
		})
	}
}

// TestScopeFoldRails_ResourceScopedVerbName parity-tests a bare verb inside a
// resources block (no member/collection wrapper, no on:) — resourceScopedHelperName's
// SUFFIX order (base_action), the one shape reusing member_verb_route_inline's
// Emit would have gotten backwards.
func TestScopeFoldRails_ResourceScopedVerbName(t *testing.T) {
	got := foldRailsRouteNames(t, `Rails.application.routes.draw do
  resources :lros do
    post :add_details
  end
end`)
	if h := got["POST /lros/:id/add_details"]; h != "lro_add_details" {
		t.Errorf("route_helper = %q, want %q (all: %v)", h, "lro_add_details", got)
	}
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

// TestScopeFoldRails_InlineOnMemberCollection parity-tests the on: :member/
// on: :collection routes TestComposeRailsRoutePaths_InlineOnMemberCollection
// covers (that older, narrower test predates the resources ExpandTable, which
// now also generates this fixture's implicit REST CRUD alongside them).
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
