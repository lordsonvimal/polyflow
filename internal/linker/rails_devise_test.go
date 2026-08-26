package linker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeDeviseFixture lays out a minimal Rails app (config/routes.rb +
// app/models/user.rb) under a temp dir and returns the two file paths.
func writeDeviseFixture(t *testing.T, routes, model string) (routesFile, modelFile string) {
	t.Helper()
	dir := t.TempDir()
	routesFile = filepath.Join(dir, "config", "routes.rb")
	modelFile = filepath.Join(dir, "app", "models", "user.rb")
	require.NoError(t, os.MkdirAll(filepath.Dir(routesFile), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(modelFile), 0o755))
	require.NoError(t, os.WriteFile(routesFile, []byte(routes), 0o644))
	require.NoError(t, os.WriteFile(modelFile, []byte(model), 0o644))
	return routesFile, modelFile
}

func deviseRouteLabels(nodes []graph.Node) map[string]*graph.Node {
	out := map[string]*graph.Node{}
	for i := range nodes {
		if nodes[i].Meta["pattern"] == "devise_default_route" {
			out[nodes[i].Label] = &nodes[i]
		}
	}
	return out
}

// TestLinkDeviseDefaultRoutes_EnabledScopesSynthesize is orion-atlas's real
// shape (Phase DV.2's worked example): `devise_for :users` with no
// `controllers:`/`skip:` at all, and a model declaring every core module
// except :confirmable.
func TestLinkDeviseDefaultRoutes_EnabledScopesSynthesize(t *testing.T) {
	t.Parallel()
	routesFile, modelFile := writeDeviseFixture(t, `
Rails.application.routes.draw do
  devise_for :users
end
`, `
class User < ApplicationRecord
  devise :database_authenticatable, :registerable, :recoverable,
         :rememberable, :validatable, :lockable, :jwt_authenticatable
end
`)

	nodes := LinkDeviseDefaultRoutes(map[string][]string{
		"orion-atlas": {routesFile, modelFile},
	})
	got := deviseRouteLabels(nodes)

	for _, want := range []string{
		"POST /users/sign_in",
		"DELETE /users/sign_out",
		"POST /users",
		"PATCH /users",
		"GET /users/password/new",
		"POST /users/unlock",
	} {
		assert.Contains(t, got, want, "enabled module must synthesize its scope's routes")
	}

	// :confirmable was never declared — zero confirmations nodes.
	for label := range got {
		assert.NotEqual(t, "confirmations", got[label].Meta["resource"], "no :confirmable in model, got %q", label)
	}
	for _, n := range got {
		assert.Empty(t, n.Meta["controller_module"])
		assert.Equal(t, "ruby", n.Language)
	}
}

// TestLinkDeviseDefaultRoutes_ControllersOverrideNotDuplicated: a scope named
// in `controllers:` is DV.1's territory — DV.2 must not also synthesize a
// default node for it, or the two phases double up on one route.
func TestLinkDeviseDefaultRoutes_ControllersOverrideNotDuplicated(t *testing.T) {
	t.Parallel()
	routesFile, modelFile := writeDeviseFixture(t, `
Rails.application.routes.draw do
  devise_for :users, controllers: { sessions: "sessions" }
end
`, `
class User < ApplicationRecord
  devise :database_authenticatable, :registerable
end
`)

	nodes := LinkDeviseDefaultRoutes(map[string][]string{
		"orion": {routesFile, modelFile},
	})
	got := deviseRouteLabels(nodes)

	assert.NotContains(t, got, "POST /users/sign_in", "sessions is DV.1's territory via controllers:")
	assert.Contains(t, got, "POST /users", "registrations was not overridden, so DV.2 must still synthesize it")
}

// TestLinkDeviseDefaultRoutes_SkipDropsScope: `skip:` removes a scope from
// DV.2's default set exactly as it does DV.1's override set.
func TestLinkDeviseDefaultRoutes_SkipDropsScope(t *testing.T) {
	t.Parallel()
	routesFile, modelFile := writeDeviseFixture(t, `
Rails.application.routes.draw do
  devise_for :users, skip: [:registrations]
end
`, `
class User < ApplicationRecord
  devise :database_authenticatable, :registerable
end
`)

	nodes := LinkDeviseDefaultRoutes(map[string][]string{
		"orion": {routesFile, modelFile},
	})
	got := deviseRouteLabels(nodes)

	assert.NotContains(t, got, "POST /users", "skip: must drop registrations even though the model declares :registerable")
	assert.Contains(t, got, "POST /users/sign_in", "sessions was not skipped, so it must still synthesize")
}

// TestLinkDeviseDefaultRoutes_DisabledModuleProducesNothing: a module the
// model does not declare produces zero nodes for its scope, not an
// under-specified guess.
func TestLinkDeviseDefaultRoutes_DisabledModuleProducesNothing(t *testing.T) {
	t.Parallel()
	routesFile, modelFile := writeDeviseFixture(t, `
Rails.application.routes.draw do
  devise_for :users
end
`, `
class User < ApplicationRecord
  devise :database_authenticatable
end
`)

	nodes := LinkDeviseDefaultRoutes(map[string][]string{
		"orion": {routesFile, modelFile},
	})
	got := deviseRouteLabels(nodes)

	assert.Contains(t, got, "POST /users/sign_in")
	assert.NotContains(t, got, "POST /users", "no :registerable declared, no registrations routes")
	assert.NotContains(t, got, "GET /users/password/new", "no :recoverable declared, no passwords routes")
}

// TestLinkDeviseDefaultRoutes_NoDanglingEdges is this phase's bug-class #10
// check (the plan's own framing, matching TestLinkRailsFilters_NoDanglingEdges's
// equivalent): every synthesized node has no in-repo controller behind it, so
// LinkRailsRouteActions must ledger every single one as
// UnresolvedRailsRouteAction and produce zero `calls` edges — never a
// fabricated link to a controller that doesn't exist.
func TestLinkDeviseDefaultRoutes_NoDanglingEdges(t *testing.T) {
	t.Parallel()
	routesFile, modelFile := writeDeviseFixture(t, `
Rails.application.routes.draw do
  devise_for :users
end
`, `
class User < ApplicationRecord
  devise :database_authenticatable, :registerable, :recoverable, :confirmable, :lockable
end
`)

	nodes := LinkDeviseDefaultRoutes(map[string][]string{
		"orion": {routesFile, modelFile},
	})
	require.NotEmpty(t, nodes)

	edges, unresolved := LinkRailsRouteActions(nodes)
	assert.Empty(t, edges, "no controller exists for any default-scope node; must never fabricate a calls edge")
	assert.Len(t, unresolved, len(nodes), "every synthesized node must be ledgered")
	for _, u := range unresolved {
		assert.Equal(t, UnresolvedRailsRouteAction, u.Kind)
	}
}

// TestLinkDeviseDefaultRoutes_NoRoutesFileIsNoOp: a service with no
// config/routes.rb (not a Rails app, or Devise unused) synthesizes nothing.
func TestLinkDeviseDefaultRoutes_NoRoutesFileIsNoOp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	modelFile := filepath.Join(dir, "app", "models", "user.rb")
	require.NoError(t, os.MkdirAll(filepath.Dir(modelFile), 0o755))
	require.NoError(t, os.WriteFile(modelFile, []byte(`
class User < ApplicationRecord
  devise :database_authenticatable
end
`), 0o644))

	nodes := LinkDeviseDefaultRoutes(map[string][]string{"svc": {modelFile}})
	assert.Empty(t, nodes)
}
