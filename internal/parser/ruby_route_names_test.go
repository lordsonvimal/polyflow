package parser

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// C.2. A Rails view calls a route by *name* — `study_deliverable_path(s, d)` —
// and the name is composed from a third subset of the routing DSL that agrees
// with neither the URL nor the controller module. Recording it at declaration
// time is the only way to get it right; deriving it from the composed path
// afterwards cannot work, because `scope "app"` is in the path and not in the
// name while a nested parent resource is in both but singularized.

// namesOf indexes the route names the walker stamped by their method+path, so
// a test can assert the pairing rather than either half alone.
func namesOf(nodes []graph.Node) map[string]string {
	out := map[string]string{}
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPHandler {
			continue
		}
		if p := n.Meta["path"]; p != "" {
			out[n.Meta["method"]+" "+p] = n.Meta["route_helper"]
		}
	}
	return out
}

// TestRouteNames_ScopeContributesNoName is the defect C.2 fixes, stated
// minimally: the URL gains "app", the name does not. The old helper map derived
// one from the other in both directions and so had to be wrong in one of them.
func TestRouteNames_ScopeContributesNoName(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
  scope "app" do
    resources :folders
  end
end`))

	assert.Equal(t, "folders", names["GET /app/folders"])
	assert.Equal(t, "folder", names["GET /app/folders/:id"])
	assert.Equal(t, "new_folder", names["GET /app/folders/new"])
	assert.Equal(t, "edit_folder", names["GET /app/folders/:id/edit"])
	// create/update/destroy reuse the index/show names, as `rails routes` prints them.
	assert.Equal(t, "folders", names["POST /app/folders"])
	assert.Equal(t, "folder", names["DELETE /app/folders/:id"])
}

// TestRouteNames_NamespaceContributesToBoth is the contrast that makes the
// third stack necessary rather than merely tidy.
func TestRouteNames_NamespaceContributesToBoth(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
  namespace :client_api do
    namespace :v1 do
      resources :users
    end
  end
end`))

	assert.Equal(t, "client_api_v1_users", names["GET /client_api/v1/users"])
	assert.Equal(t, "client_api_v1_user", names["GET /client_api/v1/users/:id"])
	assert.Equal(t, "new_client_api_v1_user", names["GET /client_api/v1/users/new"])
}

// TestRouteNames_NestedParentIsSingularized is orion's dominant view shape.
// The parent contributes `studies/:study_id` to the URL and `study` to the
// name — the single clearest reason a name cannot be read back off a path.
func TestRouteNames_NestedParentIsSingularized(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
  scope "app" do
    resources :studies do
      resources :deliverables
    end
  end
end`))

	assert.Equal(t, "study_deliverables", names["GET /app/studies/:study_id/deliverables"])
	assert.Equal(t, "study_deliverable", names["GET /app/studies/:study_id/deliverables/:id"])
	assert.Equal(t, "edit_study_deliverable", names["GET /app/studies/:study_id/deliverables/:id/edit"])
}

// TestRouteNames_MemberIsSingularCollectionIsPlural pins the asymmetry that
// nameScope keeps singular and plural apart for: two routes in the same block,
// named off opposite inflections of the same resource.
func TestRouteNames_MemberIsSingularCollectionIsPlural(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
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
end`))

	assert.Equal(t, "sync_user", names["GET /app/users/:id/sync"])
	assert.Equal(t, "recent_users", names["GET /app/users/recent"])
}

// TestRouteNames_InlineOnMemberAndCollection covers the `on:` spelling, which
// reaches composeAndStamp by a different pattern and so a different code path.
func TestRouteNames_InlineOnMemberAndCollection(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
  resources :change_logs, only: %i[index] do
    get :export, on: :collection
    get :details, on: :member
  end
end`))

	assert.Equal(t, "export_change_logs", names["GET /change_logs/export"])
	assert.Equal(t, "details_change_log", names["GET /change_logs/:id/details"])
}

// TestRouteNames_LiteralVerbRouteUsesThePathNotTheScope pins Rails' auto-naming
// of a string route: the literal contributes, the enclosing `scope "app"` does
// not, so the name is audit_logs and not app_audit_logs.
func TestRouteNames_LiteralVerbRouteUsesThePathNotTheScope(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
  scope "app" do
    get "audit_logs", to: "change_logs#audit_logs"
  end
end`))

	assert.Equal(t, "audit_logs", names["GET /app/audit_logs"])
}

// TestRouteNames_AsOverridesTheName covers `as:`, the one construct that names
// a route without touching its URL. orion uses it at routes.rb:160.
func TestRouteNames_AsOverridesTheName(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
  resources :studies do
    resources :task_reports, only: [] do
      post "/", on: :collection, to: "task_reports#index", as: "collection"
    end
  end
end`))

	assert.Equal(t, "collection_study_task_reports",
		names["POST /studies/:study_id/task_reports"])
}

// TestRouteNames_AsOnResourcesRenamesWithoutMovingTheURL is the other half of
// `as:`: the path stack and the name stack diverge in opposite directions.
func TestRouteNames_AsOnResourcesRenamesWithoutMovingTheURL(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
  resources :studies, as: :containers
end`))

	assert.Equal(t, "containers", names["GET /studies"])
	assert.Equal(t, "container", names["GET /studies/:id"])
}

// TestRouteNames_DynamicLiteralPathIsUnnamed. Rails generates no helper for a
// route whose path has a dynamic segment, and neither should this. An invented
// name would shadow a real helper of the same spelling and send its views to
// the wrong endpoint — the failure mode that is strictly worse than no link.
func TestRouteNames_DynamicLiteralPathIsUnnamed(t *testing.T) {
	t.Parallel()
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  get "files/:id/raw", to: "files#raw"
end`)
	names := namesOf(nodes)

	require.Contains(t, names, "GET /files/:id/raw")
	assert.Equal(t, "", names["GET /files/:id/raw"],
		"a route Rails would not name must carry no route_helper")
}

// TestRouteNames_SiblingScopesDoNotBleed guards the copy-on-append discipline
// on the third stack, the same shared-backing-array bug appendSeg exists for.
func TestRouteNames_SiblingScopesDoNotBleed(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
  namespace :admin do
    resources :users
  end
  namespace :api do
    resources :users
  end
  resources :users
end`))

	assert.Equal(t, "admin_users", names["GET /admin/users"])
	assert.Equal(t, "api_users", names["GET /api/users"])
	assert.Equal(t, "users", names["GET /users"])
}

// TestRouteNames_UncountableResourceGetsIndexSuffix is Rails'
// `collection_name` rule (CN): when a resource's singular and plural are the
// same word, one name cannot mean both the collection and a member, so the
// collection takes an `_index` suffix. Six of cedar's declarations are shaped
// this way, and until this landed every view writing sso_index_path resolved to
// nothing while the route sat in the graph under the member's name.
func TestRouteNames_UncountableResourceGetsIndexSuffix(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
  namespace :organization_admin do
    resources :sso
  end
  resources :help, only: [:index, :show]
end`))

	assert.Equal(t, "organization_admin_sso_index", names["GET /organization_admin/sso"])
	assert.Equal(t, "organization_admin_sso_index", names["POST /organization_admin/sso"])
	// The member keeps the bare name — that is the whole point of the suffix.
	assert.Equal(t, "organization_admin_sso", names["GET /organization_admin/sso/:id"])
	assert.Equal(t, "organization_admin_sso", names["DELETE /organization_admin/sso/:id"])
	assert.Equal(t, "new_organization_admin_sso", names["GET /organization_admin/sso/new"])

	assert.Equal(t, "help_index", names["GET /help"])
	assert.Equal(t, "help", names["GET /help/:id"])
}

// TestRouteNames_CountableResourceUnaffected is the regression guard: the
// suffix must appear only where singular and plural genuinely collide. A rule
// that fired one word too widely would rename every collection helper in the
// graph at once.
func TestRouteNames_CountableResourceUnaffected(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
  resources :folders
  resources :studies
end`))

	assert.Equal(t, "folders", names["GET /folders"])
	assert.Equal(t, "folder", names["GET /folders/:id"])
	assert.Equal(t, "studies", names["GET /studies"])
	assert.Equal(t, "study", names["GET /studies/:id"])
}

// TestRouteNames_SingletonKeepsSingularName. `resource :session` has no
// collection at all, and ActionDispatch's SingletonResource overrides
// collection_name back to the singular — so the `_index` rule, whose trigger
// (singular == plural) a singleton always satisfies, must not reach it.
func TestRouteNames_SingletonKeepsSingularName(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
  resource :session, only: [:new, :create, :destroy]
  resource :sso, only: [:show]
end`))

	assert.Equal(t, "session", names["POST /session"])
	assert.Equal(t, "session", names["DELETE /session"])
	assert.Equal(t, "new_session", names["GET /session/new"])
	assert.Equal(t, "sso", names["GET /sso"])
}

// TestRouteNames_UncountableCollectionBlock. A collection verb route is named
// off the same collection_name, so the suffix has to reach it too:
// `collection do get :bulk end` inside `resources :sso` is bulk_sso_index_path.
func TestRouteNames_UncountableCollectionBlock(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
  resources :sso do
    collection do
      get :bulk
    end
    member do
      get :audit
    end
  end
end`))

	assert.Equal(t, "bulk_sso_index", names["GET /sso/bulk"])
	// A member route is named off the singular, which the suffix never touches.
	assert.Equal(t, "audit_sso", names["GET /sso/:id/audit"])
}

// TestRouteNames_StringActionInMemberBlockIsPrefixed. Rails accepts a verb
// action as either a symbol (`get :download`) or a string (`get "download"`),
// and names both identically. The string form is matched by http_verb_route
// rather than member_verb_route, and http_verb_route could previously only see
// the `on:` keyword — so the lexical `member do ... end` form fell through to
// literal-path naming and put the qualifier behind the action instead of in
// front of it. A name that is right except for word order resolves nothing.
func TestRouteNames_StringActionInMemberBlockIsPrefixed(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
  resources :standards do
    resources :standard_export_templates, only: [:index] do
      member { get "download" }
    end
    collection { get "recent" }
  end
end`))

	assert.Equal(t, "download_standard_standard_export_template",
		names["GET /standards/:standard_id/standard_export_templates/:id/download"])
	assert.Equal(t, "recent_standards", names["GET /standards/recent"])
}

// TestRouteNames_LiteralRouteOutsideAnyBlockUnchanged is the counterweight: the
// lexical fallback must not fire where there is no member/collection block, or
// every scoped literal route in the graph would be renamed after a resource it
// merely sits near.
func TestRouteNames_LiteralRouteOutsideAnyBlockUnchanged(t *testing.T) {
	t.Parallel()
	names := namesOf(parseRubyRoutes(t, `Rails.application.routes.draw do
  scope "app" do
    get "audit_logs", to: "audits#index"
  end
  resources :folders do
    get "sibling", to: "folders#sibling"
  end
end`))

	assert.Equal(t, "audit_logs", names["GET /app/audit_logs"])
	// Directly inside a resource block but in neither member nor collection:
	// Rails names it off the path, under the resource's name prefix.
	assert.Equal(t, "folder_sibling", names["GET /folders/sibling"])
}

// TestRouteNames_UncountableResourceKeepsControllerName is the regression the
// `_index` rule caused on its first pass. A bare verb inside a `resources`
// block records Meta["resource"] for the route→controller resolver, and it read
// the same nameScope field the collection *name* lives in — so every one of
// `resources :saml_org`'s four member verbs started claiming to be served by
// SamlOrgIndexController, a class no app has ever had.
func TestRouteNames_UncountableResourceKeepsControllerName(t *testing.T) {
	t.Parallel()
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  resources :saml_org, path: "saml", only: :index do
    get :sso
    post :acs
  end
end`)

	resources := map[string]string{}
	names := map[string]string{}
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPHandler || n.Meta["path"] == "" {
			continue
		}
		key := n.Meta["method"] + " " + n.Meta["path"]
		resources[key] = n.Meta["resource"]
		names[key] = n.Meta["route_helper"]
	}

	// The controller is named after the resource as declared, never after the
	// `_index`-suffixed route name.
	assert.Equal(t, "saml_org", resources["GET /saml_org/:id/sso"])
	assert.Equal(t, "saml_org", resources["POST /saml_org/:id/acs"])
	assert.Equal(t, "saml_org", resources["GET /saml_org"])
	// The route names still take the suffix where Rails does.
	assert.Equal(t, "saml_org_index", names["GET /saml_org"])
	assert.Equal(t, "saml_org_sso", names["GET /saml_org/:id/sso"])
}
