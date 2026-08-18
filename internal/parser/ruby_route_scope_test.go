package parser

import (
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// C.1b. `scope` was the missing half of Rails' route-grouping vocabulary.
// nextGen wraps roughly 400 routes — every URL its frontend calls — in one
// `scope "app" do` at config/routes.rb:83, so every one of those handlers was
// recorded a segment short of the path any caller actually requests. No amount
// of caller-side URL resolution could bridge that.
//
// The reason this needs its own stack rather than reusing the namespace one is
// that `scope` and `namespace` differ exactly where it matters: `namespace`
// prefixes both the URL and the controller module, `scope "app"` prefixes only
// the URL, and `scope module:` only the module.

func pathsOf(nodes []graph.Node) map[string]string {
	out := map[string]string{}
	for i := range nodes {
		n := &nodes[i]
		if n.Type != graph.NodeTypeHTTPHandler {
			continue
		}
		if p := n.Meta["path"]; p != "" {
			out[n.Meta["method"]+" "+p] = n.Meta["controller_module"]
		}
	}
	return out
}

// TestScopeString_PrefixesThePath is nextGen's shape verbatim.
func TestScopeString_PrefixesThePath(t *testing.T) {
	t.Parallel()
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  scope "app" do
    resources :studies do
      member do
        get :summary
      end
    end
  end
end`)
	paths := pathsOf(nodes)

	assert.Contains(t, paths, "GET /app/studies")
	assert.Contains(t, paths, "GET /app/studies/:id")
	assert.Contains(t, paths, "GET /app/studies/:id/summary")
	assert.NotContains(t, paths, "GET /studies", "the unprefixed path must be gone, not duplicated")
}

// TestScopeString_ContributesNoControllerModule is the correctness constraint
// that made a separate stack necessary. Rails resolves these to
// StudiesController, not App::StudiesController — so a module derived from the
// URL would send phase A's route→action edges to controllers that do not exist.
func TestScopeString_ContributesNoControllerModule(t *testing.T) {
	t.Parallel()
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  scope "app" do
    resources :studies
  end
end`)

	for _, mod := range pathsOf(nodes) {
		assert.Equal(t, "", mod, "scope \"app\" must not name a controller module")
	}
}

// TestNamespace_ContributesBoth — the contrast case.
func TestNamespace_ContributesBoth(t *testing.T) {
	t.Parallel()
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  namespace :client_api do
    namespace :v1 do
      resources :lros
    end
  end
end`)
	paths := pathsOf(nodes)

	require.Contains(t, paths, "GET /client_api/v1/lros")
	assert.Equal(t, "client_api/v1", paths["GET /client_api/v1/lros"])
}

// TestScopeModuleOnly_NoPathSegment — `scope module: "admin"` relocates the
// controller without touching the URL.
func TestScopeModuleOnly_NoPathSegment(t *testing.T) {
	t.Parallel()
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  scope module: "admin" do
    resources :users
  end
end`)
	paths := pathsOf(nodes)

	require.Contains(t, paths, "GET /users")
	assert.Equal(t, "admin", paths["GET /users"])
}

// TestScopePathOption_WinsOverPositional — Rails' own precedence.
func TestScopePathOption_WinsOverPositional(t *testing.T) {
	t.Parallel()
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  scope :api, path: "v2", module: :api do
    resources :jobs
  end
end`)
	paths := pathsOf(nodes)

	require.Contains(t, paths, "GET /v2/jobs")
	assert.Equal(t, "api", paths["GET /v2/jobs"])
	assert.NotContains(t, paths, "GET /api/jobs")
}

// TestScopeWithoutPathOrModule_IsTransparent — `scope format: false` and
// `scope constraints: {…}` group routes without moving them. Inventing a
// segment from an unreadable option would shift every route beneath it.
func TestScopeWithoutPathOrModule_IsTransparent(t *testing.T) {
	t.Parallel()
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  scope format: false do
    resources :files
  end
end`)
	paths := pathsOf(nodes)

	require.Contains(t, paths, "GET /files")
	assert.Equal(t, "", paths["GET /files"])
}

// TestScopeSiblingsDoNotBleed guards the shared-backing-array bug: two sibling
// scopes appended onto the same parent slice, and the second overwrote the
// first's segment in place.
func TestScopeSiblingsDoNotBleed(t *testing.T) {
	t.Parallel()
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  scope "app" do
    scope "a" do
      resources :alphas
    end
    scope "b" do
      resources :betas
    end
  end
end`)
	paths := pathsOf(nodes)

	assert.Contains(t, paths, "GET /app/a/alphas")
	assert.Contains(t, paths, "GET /app/b/betas")
}

// TestScopeAroundNamespaceComposesInOrder — the mixed nesting nextGen actually
// has: a URL-only outer scope wrapping a module-bearing namespace.
func TestScopeAroundNamespaceComposesInOrder(t *testing.T) {
	t.Parallel()
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  scope "app" do
    namespace :admin do
      resources :audits
    end
  end
end`)
	paths := pathsOf(nodes)

	require.Contains(t, paths, "GET /app/admin/audits")
	assert.Equal(t, "admin", paths["GET /app/admin/audits"],
		"the scope segment must not reach the module stack")
}

// TestScopeAppliesToVerbRoutes — the composeAndStamp path, not just the
// synthesized REST one.
func TestScopeAppliesToVerbRoutes(t *testing.T) {
	t.Parallel()
	nodes := parseRubyRoutes(t, `Rails.application.routes.draw do
  scope "app" do
    get "/zstd_level"
  end
end`)
	paths := pathsOf(nodes)

	assert.Contains(t, paths, "GET /app/zstd_level")
}
