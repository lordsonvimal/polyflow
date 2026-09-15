package indexer

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// TestRun_RouteReachesInheritedAction is Tier RA end to end.
//
// A route whose controller inherits its action was indistinguishable from one
// whose action does not exist: both produced a `rails_route_action_unresolved`
// row and an http_handler with no outbound edge. The distinction is the whole
// tier, so the fixture holds both shapes at once — a base class, an included
// concern, and a bare `resources` against a controller that implements one of
// its declared actions. Closing the first two while leaving the third alone is
// the only passing outcome; closing all three would mean the pass had started
// matching routes that have no implementation anywhere.
//
// This runs through the real indexer rather than the linker alone because the
// `contains` and `inherits` edges RA walks are produced by two different
// stages, and their availability at this pass's position in the pipeline is
// exactly what a unit test cannot check.
func TestRun_RouteReachesInheritedAction(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "orion")
	ctrls := filepath.Join(svc, "app", "controllers")
	concerns := filepath.Join(ctrls, "concerns")
	api := filepath.Join(ctrls, "api", "v1")
	require.NoError(t, os.MkdirAll(filepath.Join(svc, "config"), 0o755))
	require.NoError(t, os.MkdirAll(concerns, 0o755))
	require.NoError(t, os.MkdirAll(api, 0o755))

	// rails_route_actions is Tier FX FX.8.24 — a package-gated framework pass
	// (railties, matching rails_filters/rails_model_tables), so the fixture
	// has to resolve it the way a real Rails app does.
	writeFile(t, svc, "Gemfile", "source 'https://rubygems.org'\ngem 'rails'\n")
	writeFile(t, svc, "Gemfile.lock", `GEM
  remote: https://rubygems.org/
  specs:
    railties (7.1.3)
    rails (7.1.3)

PLATFORMS
  ruby

DEPENDENCIES
  rails
`)

	writeFile(t, filepath.Join(svc, "config"), "routes.rb", `Rails.application.routes.draw do
  resource :home, only: [:show]
  namespace :api do
    scope module: :v1 do
      resources :mappings, only: [:index, :show]
      resources :async_operations
    end
  end
end
`)

	// The concern case: HomesController declares no `show` of its own.
	writeFile(t, concerns, "home_common_actions.rb", `module HomeCommonActions
  def show
    render :show
  end
end
`)
	writeFile(t, ctrls, "homes_controller.rb", `class HomesController < ApplicationController
  include HomeCommonActions

  def pusher_script; end
end
`)

	// The base-class case: a generic index/show two controllers away from any
	// route, plus a private helper that must not become routable.
	writeFile(t, api, "api_base_controller.rb", `module Api
  module V1
    class ApiBaseController < ActionController::Base
      def index
        render json: find_all
      end

      def show
        render json: find
      end

      private

      def find_all; end

      def find; end
    end
  end
end
`)
	writeFile(t, api, "mappings_controller.rb", `module Api
  module V1
    class MappingsController < ApiBaseController
      private

      def find_all
        Mapping.all
      end
    end
  end
end
`)

	// The anti-case. `resources :async_operations` declares all seven REST
	// actions; the controller implements one, and the base class implements two
	// more. The remaining four are dead and must stay in the ledger.
	writeFile(t, api, "async_operations_controller.rb", `module Api
  module V1
    class AsyncOperationsController < ApiBaseController
      def poll
        head :ok
      end
    end
  end
end
`)

	cfg := &workspace.WorkspaceConfig{
		Name: "test", Version: "1",
		Services: []workspace.Service{{Name: "orion", Path: svc, Language: "ruby"}},
	}
	dbDir := filepath.Join(dir, ".polyflow")
	runIndexer(t, cfg, dbDir, false)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()
	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)

	served := map[string][]string{}
	for _, n := range idx.Nodes {
		if n.Type != graph.NodeTypeHTTPHandler {
			continue
		}
		for _, e := range idx.OutEdges[n.ID] {
			if e.Type != graph.EdgeTypeCalls {
				continue
			}
			to := idx.Nodes[e.To]
			require.NotNil(t, to, "route %s calls a node that is not in the graph", n.Label)
			served[n.Label] = append(served[n.Label], filepath.Base(to.File)+"#"+to.Label)
		}
	}

	assert.Equal(t, []string{"home_common_actions.rb#show"}, served["GET /home"],
		"an action reached through `include` is still the action the route serves")
	assert.Equal(t, []string{"api_base_controller.rb#index"}, served["GET /api/mappings"])
	assert.Equal(t, []string{"api_base_controller.rb#show"}, served["GET /api/mappings/:id"])
	assert.Equal(t, []string{"api_base_controller.rb#index"}, served["GET /api/async_operations"])

	// The fan-out gate: a route serves exactly one action, and RA adds a second
	// place to look for it. Two edges out of one handler would mean the ancestor
	// walk emitted alongside the direct hit rather than instead of it.
	for label, targets := range served {
		assert.Len(t, targets, 1, "route %s serves more than one action: %v", label, targets)
	}

	unresolved, err := store.ListUnresolvedRefs(context.Background())
	require.NoError(t, err)
	var routeRefs []string
	for _, u := range unresolved {
		if u.Kind == "rails_route_action_unresolved" {
			routeRefs = append(routeRefs, u.Name)
		}
	}
	sort.Strings(routeRefs)
	assert.Equal(t, []string{
		"async_operations#create",
		"async_operations#destroy",
		"async_operations#edit",
		"async_operations#new",
		"async_operations#update",
	}, routeRefs,
		"REST actions no ancestor implements are dead routes and must stay unresolved")

	// `find_all` is private in both the base class and the subclass. Nothing
	// routes to it, and neither the override nor the inherited copy may appear
	// as anyone's action.
	for label, targets := range served {
		for _, target := range targets {
			assert.NotContains(t, target, "find_all", "route %s reached a private helper", label)
		}
	}
}
