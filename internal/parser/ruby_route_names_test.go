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
