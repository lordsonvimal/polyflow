package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/linker"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// TestRun_SingularResourceReachesPluralController is Tier CR end to end. Rails
// routes the singular `resource :session` to the *plural* SessionsController;
// before CR the route walker's name was resolved verbatim, looked for
// session_controller.rb, and ledgered `session#create` while the controller sat
// on disk one "s" away — 19 such declarations on the audit corpus.
//
// The fixture holds all four cases together because they interact: the two
// that must resolve, the one that must resolve to its *stated* controller
// rather than its inflected one, and the dead route that must stay in the
// ledger. Passing the first three while quietly closing the fourth is the
// failure mode this tier's acceptance table has a lower bound for.
func TestRun_SingularResourceReachesPluralController(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "orion")
	ctrls := filepath.Join(svc, "app", "controllers")
	require.NoError(t, os.MkdirAll(filepath.Join(svc, "config"), 0o755))
	require.NoError(t, os.MkdirAll(ctrls, 0o755))

	writeFile(t, filepath.Join(svc, "config"), "routes.rb", `Rails.application.routes.draw do
  resource :session, only: [:create]
  resource :current_organization, only: [:update]
  resource :report, controller: "containers", only: [:show]
  resources :widgets, only: [:index, :new]
end
`)
	writeFile(t, ctrls, "sessions_controller.rb", `class SessionsController < ApplicationController
  def create
    head :ok
  end
end
`)
	writeFile(t, ctrls, "current_organizations_controller.rb", `class CurrentOrganizationsController < ApplicationController
  def update
    head :ok
  end
end
`)
	writeFile(t, ctrls, "containers_controller.rb", `class ContainersController < ApplicationController
  def show
    head :ok
  end
end
`)
	// The decoy for the explicit-controller case: pluralizing the declaration
	// would land here, and `controller:` says otherwise.
	writeFile(t, ctrls, "reports_controller.rb", `class ReportsController < ApplicationController
  def show
    head :ok
  end
end
`)
	// A bare `resources` whose controller implements only one of the two
	// actions it declares. The `new` route is genuinely dead and must stay in
	// the ledger — roughly half of a real monolith's unresolved route entries
	// are this shape, and CR must not close any of them.
	writeFile(t, ctrls, "widgets_controller.rb", `class WidgetsController < ApplicationController
  def index
    head :ok
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

	// route label -> the controller file its `calls` edge lands in.
	served := map[string][]string{}
	for _, n := range idx.Nodes {
		if n.Type != graph.NodeTypeHTTPHandler || n.Meta["pattern"] != "rest_resource_route" {
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

	assert.Equal(t, []string{"sessions_controller.rb#create"}, served["POST /session"])
	assert.Equal(t, []string{"current_organizations_controller.rb#update"},
		served["PATCH /current_organization"])
	assert.Equal(t, []string{"containers_controller.rb#show"}, served["GET /report"],
		"an explicit controller: option is a stated fact; pluralizing the name would guess ReportsController")
	assert.Equal(t, []string{"widgets_controller.rb#index"}, served["GET /widgets"])

	for label, targets := range served {
		assert.Len(t, targets, 1, "route %s serves more than one action: %v", label, targets)
	}

	unresolved, err := store.ListUnresolvedRefs(context.Background())
	require.NoError(t, err)
	var routeRefs []string
	for _, u := range unresolved {
		if u.Kind == linker.UnresolvedRailsRouteAction {
			routeRefs = append(routeRefs, u.Name)
		}
	}
	assert.Equal(t, []string{"widgets#new"}, routeRefs,
		"a REST route the controller never implements is correctly unresolved and must stay that way")

	// PUT and PATCH are both minted for update; the PUT twin resolving too is
	// the only reason this count is not simply len(served).
	require.NotEmpty(t, served["PUT /current_organization"],
		"both update verbs must reach the same action")
	assert.True(t, strings.HasSuffix(served["PUT /current_organization"][0],
		"current_organizations_controller.rb#update"))
}
