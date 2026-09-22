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

// railsRouteMeta is the rails_route Fact's controller/action tail (Args[3:8]),
// parallel to graph.Node.Meta's controller_module/resource/action/
// resource_style/controller_explicit fields.
type railsRouteMeta struct {
	controllerModule   string
	resource           string
	action             string
	resourceStyle      string
	controllerExplicit string
}

// foldRailsRouteMeta is foldRailsRoutes'/foldRailsRouteNames' controller/
// action-carrying twin, for the controller:/to: override parity tests below.
func foldRailsRouteMeta(t *testing.T, src string) map[string]railsRouteMeta {
	t.Helper()
	p := sitter.NewParser()
	p.SetLanguage(rubysitter.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, []byte(src))
	if err != nil || tree == nil {
		t.Fatalf("parse: %v", err)
	}
	matches := buildRailsMatches(tree.RootNode(), "config/routes.rb", []byte(src))
	facts := scopefold.Fold(tree.RootNode(), matches, railsRouteGrammar())
	out := map[string]railsRouteMeta{}
	for _, f := range facts {
		if f.Pred != "rails_route" {
			continue
		}
		out[f.Args[1].Value()+" "+f.Args[0].Value()] = railsRouteMeta{
			controllerModule:   f.Args[3].Value(),
			resource:           f.Args[4].Value(),
			action:             f.Args[5].Value(),
			resourceStyle:      f.Args[6].Value(),
			controllerExplicit: f.Args[7].Value(),
		}
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

// TestScopeFoldRails_ExplicitToTarget parity-tests
// TestHTTPVerbRouteExplicitToTarget (ruby_route_paths_test.go): an explicit
// `to: "controller#action"` target decouples the resource/action from the
// URL, and a namespaced controller ("admin/db_status") contributes extra
// module nesting on top of whatever namespace/scope already pushed.
func TestScopeFoldRails_ExplicitToTarget(t *testing.T) {
	meta := foldRailsRouteMeta(t, `Rails.application.routes.draw do
  post "queue_compute_dependencies", to: "lyra_job_items#queue_compute_dependencies"
  get "/x", to: "admin/db_status#index"
  namespace :api do
    get "/y", to: "admin/db_status#index"
  end
end
`)
	cases := []struct {
		key    string
		action string
		res    string
		mod    string
	}{
		{"POST /queue_compute_dependencies", "queue_compute_dependencies", "lyra_job_items", ""},
		{"GET /x", "index", "db_status", "admin"},
		{"GET /api/y", "index", "db_status", "api/admin"},
	}
	for _, tc := range cases {
		got, ok := meta[tc.key]
		if !ok {
			t.Errorf("%s: missing (all: %v)", tc.key, meta)
			continue
		}
		if got.action != tc.action || got.resource != tc.res || got.controllerModule != tc.mod {
			t.Errorf("%s: action=%q resource=%q controller_module=%q, want action=%q resource=%q controller_module=%q",
				tc.key, got.action, got.resource, got.controllerModule, tc.action, tc.res, tc.mod)
		}
	}
}

// TestScopeFoldRails_ResourceStyleRecorded parity-tests
// TestRESTResourceRoutes_ResourceStyleRecorded: a singleton's implicit
// actions are stamped "singular", a plural resource's "plural", and neither
// carries controller_explicit absent a controller: option.
func TestScopeFoldRails_ResourceStyleRecorded(t *testing.T) {
	meta := foldRailsRouteMeta(t, `Rails.application.routes.draw do
  resource :session, only: [:create, :destroy]
  resources :widgets, only: [:index]
end
`)
	if got := meta["POST /session"].resourceStyle; got != "singular" {
		t.Errorf("POST /session resource_style = %q, want singular", got)
	}
	if got := meta["GET /widgets"].resourceStyle; got != "plural" {
		t.Errorf("GET /widgets resource_style = %q, want plural", got)
	}
	if got := meta["POST /session"].controllerExplicit; got != "" {
		t.Errorf("POST /session controller_explicit = %q, want empty", got)
	}
}

// TestScopeFoldRails_ExplicitControllerOption parity-tests
// TestRESTResourceRoutes_ExplicitControllerOption: `controller:` renames the
// controller outright without touching the URL.
func TestScopeFoldRails_ExplicitControllerOption(t *testing.T) {
	meta := foldRailsRouteMeta(t, `Rails.application.routes.draw do
  resources :studies, controller: "containers", only: [:index]
end
`)
	got := meta["GET /studies"]
	if got.resource != "containers" {
		t.Errorf("resource = %q, want containers", got.resource)
	}
	if got.controllerExplicit != "true" {
		t.Errorf("controller_explicit = %q, want true", got.controllerExplicit)
	}
}

// TestScopeFoldRails_NamespacedControllerOption parity-tests
// TestRESTResourceRoutes_NamespacedControllerOption: a namespaced controller:
// value ("admin/dashboards") splits into extra module nesting the same way
// an explicit to: target does.
func TestScopeFoldRails_NamespacedControllerOption(t *testing.T) {
	meta := foldRailsRouteMeta(t, `Rails.application.routes.draw do
  resource :dashboard, controller: "admin/dashboards", only: [:show]
end
`)
	got := meta["GET /dashboard"]
	if got.resource != "dashboards" {
		t.Errorf("resource = %q, want dashboards", got.resource)
	}
	if got.controllerModule != "admin" {
		t.Errorf("controller_module = %q, want admin", got.controllerModule)
	}
}

// TestScopeFoldRails_VerbRouteInsideSingularResource parity-tests
// TestRESTResourceRoutes_VerbRouteInsideSingularResource: a member/collection
// verb route nested in a singular `resource` block inherits "singular", the
// same style its enclosing resource's own implicit actions carry — but a
// bare top-level verb route stays unmarked, since it has no enclosing
// resource to claim a style from. resource_scoped_verb's own resource/
// controller_module (a bare verb inside `resources`) never honours
// controller:, unlike the block's own implicit CRUD.
func TestScopeFoldRails_VerbRouteInsideSingularResource(t *testing.T) {
	meta := foldRailsRouteMeta(t, `Rails.application.routes.draw do
  resource :home, only: [] do
    collection do
      get :pusher_script
    end
  end
  resources :widgets, only: [] do
    collection do
      get :bulk_edit
    end
  end
  get "app_info", to: "homes#app_info"
end
`)
	if got := meta["GET /home/pusher_script"].resourceStyle; got != "singular" {
		t.Errorf("GET /home/pusher_script resource_style = %q, want singular", got)
	}
	if got := meta["GET /widgets/bulk_edit"].resourceStyle; got != "" {
		t.Errorf("GET /widgets/bulk_edit resource_style = %q, want empty", got)
	}
	if got := meta["GET /app_info"].resourceStyle; got != "" {
		t.Errorf("GET /app_info resource_style = %q, want empty", got)
	}
}

// TestScopeFoldRails_ResourceScopedVerbIgnoresControllerOption parity-tests
// TestComposeRailsRoutePaths_BareVerbInResourcesBlock's controller_module/
// resource assertions: a bare verb directly in a `resources` block reads its
// resource off res_plural, not off any controller: override on the block.
func TestScopeFoldRails_ResourceScopedVerbIgnoresControllerOption(t *testing.T) {
	meta := foldRailsRouteMeta(t, `Rails.application.routes.draw do
  namespace :client_api do
    namespace :v1 do
      resources :lros do
        post :add_details
      end
    end
  end
end
`)
	got := meta["POST /client_api/v1/lros/:id/add_details"]
	if got.controllerModule != "client_api/v1" {
		t.Errorf("controller_module = %q, want client_api/v1", got.controllerModule)
	}
	if got.resource != "lros" {
		t.Errorf("resource = %q, want lros", got.resource)
	}
}

// TestScopeFoldRails_DeviseForControllersOverride parity-tests
// TestDeviseForControllersOverride (ruby_route_paths_test.go): devise_for's
// controllers: hash, resolved entirely through scopefold.HashExpandSpec
// against railsinflect.DeviseScopeActions — including two scopes
// (invitations, password_expired) that are not core Devise but are named
// directly in the override hash.
func TestScopeFoldRails_DeviseForControllersOverride(t *testing.T) {
	meta := foldRailsRouteMeta(t, `Rails.application.routes.draw do
  devise_for :users, controllers: {
    invitations: "invitations", passwords: "passwords",
    registrations: "registrations", password_expired: "password_expired",
    sessions: "sessions"
  }
end
`)

	sessionsCreate, ok := meta["POST /users/sign_in"]
	if !ok {
		t.Fatalf("missing sessions create route, got %v", meta)
	}
	if sessionsCreate.resource != "sessions" || sessionsCreate.action != "create" || sessionsCreate.controllerModule != "" {
		t.Errorf("sessions create = %+v, want resource=sessions action=create controller_module=\"\"", sessionsCreate)
	}

	if got := meta["DELETE /users/sign_out"].action; got != "destroy" {
		t.Errorf("sessions destroy action = %q, want destroy", got)
	}

	regUpdate, ok := meta["PATCH /users"]
	if !ok {
		t.Fatalf("missing registrations update route, got %v", meta)
	}
	if regUpdate.resource != "registrations" || regUpdate.action != "update" {
		t.Errorf("registrations update = %+v, want resource=registrations action=update", regUpdate)
	}

	if got := meta["GET /users/password/new"].resource; got != "passwords" {
		t.Errorf("passwords new resource = %q, want passwords", got)
	}

	if _, ok := meta["GET /users/invitation/new"]; !ok {
		t.Error("invitations override must still route despite not being core Devise")
	}
	if _, ok := meta["GET /users/password_expired/edit"]; !ok {
		t.Error("password_expired override must still route despite not being core Devise")
	}
}

// TestScopeFoldRails_DeviseForNoControllersHashSynthesizesNothing parity-tests
// TestDeviseForNoControllersHashSynthesizesNothing: skip: with no
// controllers: hash at all synthesizes zero routes.
func TestScopeFoldRails_DeviseForNoControllersHashSynthesizesNothing(t *testing.T) {
	routes := foldRailsRoutes(t, `Rails.application.routes.draw do
  devise_for :users, skip: [:sessions], path: ""
end
`)
	if len(routes) != 0 {
		t.Errorf("expected zero routes, got %v", routes)
	}
}

// TestScopeFoldRails_DeviseForNamespacedControllerBasename parity-tests
// TestDeviseForNamespacedControllerBasename: a controllers: value embedding
// its own namespace ("users/sessions") splits into extra module nesting the
// same way an explicit to: target does.
func TestScopeFoldRails_DeviseForNamespacedControllerBasename(t *testing.T) {
	meta := foldRailsRouteMeta(t, `Rails.application.routes.draw do
  devise_for :users, controllers: { sessions: "users/sessions" }
end
`)
	got, ok := meta["POST /users/sign_in"]
	if !ok {
		t.Fatalf("missing sessions create route, got %v", meta)
	}
	if got.resource != "sessions" || got.controllerModule != "users" {
		t.Errorf("got %+v, want resource=sessions controller_module=users", got)
	}
}

// TestScopeFoldRails_DeviseForSkipDropsOverriddenScope parity-tests
// TestDeviseForSkipDropsOverriddenScope: skip: removes a scope even when
// it's also named in controllers:.
func TestScopeFoldRails_DeviseForSkipDropsOverriddenScope(t *testing.T) {
	routes := foldRailsRoutes(t, `Rails.application.routes.draw do
  devise_for :users, skip: [:sessions], controllers: { sessions: "sessions", passwords: "passwords" }
end
`)
	if containsRoute(routes, "POST /users/sign_in") {
		t.Error("skip: must drop the sessions scope even though it's also overridden")
	}
	if !containsRoute(routes, "GET /users/password/new") {
		t.Error("the non-skipped override must still synthesize")
	}
}

// TestScopeFoldRails_RootString parity-tests root's own semantics against
// Rails' documented Mapper#root/match_root_route behavior directly — no
// ground truth exists in ruby_route_paths_test.go, since
// composeRailsRoutePaths never implemented `root` at all (see this file's
// grammar-file doc comment).
func TestScopeFoldRails_RootString(t *testing.T) {
	routes := foldRailsRoutes(t, `Rails.application.routes.draw do
  root "pages#home"
end
`)
	if !containsRoute(routes, "GET /") {
		t.Fatalf("root string form must synthesize GET /, got %v", routes)
	}
	names := foldRailsRouteNames(t, `Rails.application.routes.draw do
  root "pages#home"
end
`)
	if got := names["GET /"]; got != "root" {
		t.Errorf("route_helper = %q, want %q", got, "root")
	}
	meta := foldRailsRouteMeta(t, `Rails.application.routes.draw do
  root "pages#home"
end
`)["GET /"]
	if meta.controllerModule != "" || meta.resource != "pages" || meta.action != "home" {
		t.Errorf("meta = %+v, want controller_module=\"\" resource=pages action=home", meta)
	}
}

func TestScopeFoldRails_RootToKeyword(t *testing.T) {
	meta := foldRailsRouteMeta(t, `Rails.application.routes.draw do
  root to: "welcome#index"
end
`)["GET /"]
	if meta.controllerModule != "" || meta.resource != "welcome" || meta.action != "index" {
		t.Errorf("meta = %+v, want controller_module=\"\" resource=welcome action=index", meta)
	}
}

func TestScopeFoldRails_RootControllerActionKeywords(t *testing.T) {
	meta := foldRailsRouteMeta(t, `Rails.application.routes.draw do
  root controller: "welcome", action: "index"
end
`)["GET /"]
	if meta.controllerModule != "" || meta.resource != "welcome" || meta.action != "index" {
		t.Errorf("meta = %+v, want controller_module=\"\" resource=welcome action=index", meta)
	}
}

// TestScopeFoldRails_RootAsOverride confirms an explicit as: replaces the
// default "root" trailing segment while the enclosing scope's own name
// prefix (empty here) is still honored — mirrors every other leaf's
// asOrLiteralName precedence.
func TestScopeFoldRails_RootAsOverride(t *testing.T) {
	names := foldRailsRouteNames(t, `Rails.application.routes.draw do
  root "pages#home", as: :home
end
`)
	if got := names["GET /"]; got != "home" {
		t.Errorf("route_helper = %q, want %q", got, "home")
	}
}

// TestScopeFoldRails_RootNestedInNamespace confirms root scoped inside a
// namespace both prefixes the path AND the helper name — the same "/api"
// path and "api_root" helper any other route in that namespace would get.
func TestScopeFoldRails_RootNestedInNamespace(t *testing.T) {
	routes := foldRailsRoutes(t, `Rails.application.routes.draw do
  namespace :api do
    root "dashboard#show"
  end
end
`)
	if !containsRoute(routes, "GET /api") {
		t.Fatalf("root inside namespace :api must synthesize GET /api, got %v", routes)
	}
	names := foldRailsRouteNames(t, `Rails.application.routes.draw do
  namespace :api do
    root "dashboard#show"
  end
end
`)
	if got := names["GET /api"]; got != "api_root" {
		t.Errorf("route_helper = %q, want %q", got, "api_root")
	}
	meta := foldRailsRouteMeta(t, `Rails.application.routes.draw do
  namespace :api do
    root "dashboard#show"
  end
end
`)["GET /api"]
	if meta.controllerModule != "api" || meta.resource != "dashboard" || meta.action != "show" {
		t.Errorf("meta = %+v, want controller_module=api resource=dashboard action=show", meta)
	}
}

// TestScopeFoldRails_RootInsideSingletonResource confirms a root nested
// inside a `resource` block still inherits resource_style="singular" off
// the shared stack, same as any other verb leaf.
func TestScopeFoldRails_RootInsideSingletonResource(t *testing.T) {
	meta := foldRailsRouteMeta(t, `Rails.application.routes.draw do
  resource :session do
    root "sessions#new"
  end
end
`)["GET /session"]
	if meta.resourceStyle != "singular" {
		t.Errorf("resource_style = %q, want %q", meta.resourceStyle, "singular")
	}
}

// TestScopeFoldRails_RootWithNoTargetEmitsNothing confirms a malformed bare
// `root` (no to:/positional/controller+action — Rails itself raises
// ArgumentError on this) synthesizes no route rather than a garbage one.
func TestScopeFoldRails_RootWithNoTargetEmitsNothing(t *testing.T) {
	routes := foldRailsRoutes(t, `Rails.application.routes.draw do
  root
end
`)
	if containsRoute(routes, "GET /") {
		t.Errorf("a targetless root must not synthesize a route, got %v", routes)
	}
}
