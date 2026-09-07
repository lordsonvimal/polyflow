package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// TestRun_RailsNavHelperResolution is Tier CN end to end: a `redirect_to
// <helper>` in a controller and a `link_to`/`form_with` in a view are
// page-to-page transitions, and each has to come out the other side either as a
// navigates_to edge to the route it names or as a labelled, ledgered node — never
// as a node called "nav_link_rails_redirect_helper", which is a pattern name
// masquerading as a destination.
//
// The fixture puts the resolvable and the unresolvable side by side because the
// failure mode is closing the second by loosening the first.
func TestRun_RailsNavHelperResolution(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "orion")
	ctrls := filepath.Join(svc, "app", "controllers")
	views := filepath.Join(svc, "app", "views", "sessions")
	require.NoError(t, os.MkdirAll(filepath.Join(svc, "config"), 0o755))
	require.NoError(t, os.MkdirAll(ctrls, 0o755))
	require.NoError(t, os.MkdirAll(views, 0o755))

	writeFile(t, filepath.Join(svc, "config"), "routes.rb", `Rails.application.routes.draw do
  get "login", to: "sessions#new", as: :login
  resources :folders
  namespace :organization_admin do
    resources :sso
  end
end
`)
	writeFile(t, ctrls, "sessions_controller.rb", `class SessionsController < ApplicationController
  def create
    redirect_to folders_path
  end

  def destroy
    redirect_to login_path
  end

  def failed
    # Not a route helper Rails ever generated: a local method in this app.
    redirect_to fallback_path
  end
end
`)
	writeFile(t, views, "new.html.erb", `<%= link_to "Admin SSO", organization_admin_sso_index_path %>
<%= link_to "Edit", edit_folder_path(@folder) %>
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

	// Gate 1: no nav producer is labelled with its own pattern name — resolved
	// or not. (Scoped to nav producers: `resources_route` and friends are
	// bookkeeping nodes in the routes file with nothing else to be called, a
	// separate question from a destination that has a name and hides it.)
	for _, n := range idx.Nodes {
		if n.Meta["nav_link"] != "true" {
			continue
		}
		assert.NotEqual(t, n.Meta["pattern"], n.Label,
			"nav node at %s:%d is labelled with its pattern name", n.File, n.Line)
	}

	// Gate 2: each nav producer reaches the route it named.
	dest := map[string][]string{}
	for _, n := range idx.Nodes {
		if n.Type != graph.NodeTypeHTTPClient || n.Meta["nav_link"] != "true" {
			continue
		}
		for _, e := range idx.OutEdges[n.ID] {
			if e.Type != graph.EdgeTypeNavigatesTo {
				continue
			}
			to := idx.Nodes[e.To]
			require.NotNil(t, to, "nav node %s points at a node not in the graph", n.ID)
			dest[n.Label] = append(dest[n.Label], to.Label)
		}
	}

	assert.Equal(t, []string{"GET /folders"}, dest["GET /folders"])
	assert.Equal(t, []string{"GET /login"}, dest["GET /login"],
		"an `as:` route names its own helper; login_path must reach it")
	assert.Equal(t, []string{"GET /organization_admin/sso"}, dest["GET /organization_admin/sso"],
		"Rails names an uncountable resource's collection <name>_index; the view says so too")
	assert.Equal(t, []string{"GET /folders/:id/edit"}, dest["GET /folders/:id/edit"])

	// Gate 3: the one target that is not a route stays honest — labelled with
	// what the source says, carrying no invented path, and in the ledger.
	var fallback *graph.Node
	for _, n := range idx.Nodes {
		if n.Type == graph.NodeTypeHTTPClient && n.Meta["helper"] == "fallback_path" {
			fallback = n
		}
	}
	require.NotNil(t, fallback, "the unresolvable redirect produced no node at all")
	assert.Equal(t, "fallback_path", fallback.Label)
	assert.Empty(t, fallback.Meta["path"], "an unresolved helper must not acquire a path")
	assert.Empty(t, idx.OutEdges[fallback.ID], "an unresolved helper must not acquire an edge")

	unresolved, err := store.ListUnresolvedRefs(context.Background())
	require.NoError(t, err)
	var ledgered []string
	for _, u := range unresolved {
		if u.Kind == "rails_helper_unresolved" {
			ledgered = append(ledgered, u.Name)
		}
	}
	assert.Equal(t, []string{"fallback_path"}, ledgered)
}
