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

// TestRun_PropClientParamReceiver is Tier CW end to end. The transport is defined
// in one file (the HOC) and consumed in another as a plain function parameter, so
// the consuming file contains no `this.props` and nothing that names the HOC —
// the shape SPA.4's receiver gate could not admit. The point of running it
// through the full indexer rather than the linker alone is the last leg: the
// synthetic client has to reach the Rails route on the other side, which only
// happens if the minted node carries a path the http_call matcher recognises.
func TestRun_PropClientParamReceiver(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "orion")
	react := filepath.Join(svc, "react")
	require.NoError(t, os.MkdirAll(filepath.Join(svc, "config"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(react, "common"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(react, "utils"), 0o755))

	writeFile(t, filepath.Join(svc, "config"), "routes.rb", `Rails.application.routes.draw do
  get "api/prefs/assistant_config", to: "prefs#assistant_config"
end
`)
	writeFile(t, filepath.Join(react, "common"), "ComponentWithAjaxStatus.jsx", `import React from "react";
const ComponentWithAjaxStatus = function (WrappedComponent) {
  class component extends React.Component {
    get = (msg, url) => {
      const request = { method: "GET", url };
      return this.ajax(msg, request);
    };
    ajax = (msg, request) => {
      return window.$.ajax(request);
    };
    render() {
      return (
        <div>
          <WrappedComponent ajaxStatus={this} {...this.props} />
        </div>
      );
    }
  }
  return component;
};
export default ComponentWithAjaxStatus;
`)
	writeFile(t, filepath.Join(react, "utils"), "config.jsx", `function fetchConfig(ajaxStatus) {
  if (!ajaxStatus || !ajaxStatus.get) return;
  ajaxStatus.get("Loading...", "/api/prefs/assistant_config");
}
export default fetchConfig;
`)

	cfg := &workspace.WorkspaceConfig{
		Name: "test", Version: "1",
		Services: []workspace.Service{{Name: "orion", Path: svc}},
	}
	dbDir := filepath.Join(dir, ".polyflow")
	runIndexer(t, cfg, dbDir, false)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()
	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)

	var client *graph.Node
	for _, n := range idx.Nodes {
		if n.Type == graph.NodeTypeHTTPClient && n.Meta["spa"] == "prop_client" {
			client = n
		}
	}
	require.NotNil(t, client, "the parameter-receiver call minted no http_client node")
	// Suffix, not equality: a fixture under t.TempDir() is outside cwd, so file
	// paths stay absolute here.
	assert.Contains(t, client.File, "react/utils/config.jsx")
	assert.Equal(t, "/api/prefs/assistant_config", client.Meta["url"])
	assert.Equal(t, "GET", client.Meta["method"])

	var reached []string
	for _, e := range idx.OutEdges[client.ID] {
		if e.Type != graph.EdgeTypeHTTPCall {
			continue
		}
		to := idx.Nodes[e.To]
		require.NotNil(t, to, "http_call points at a node not in the graph")
		reached = append(reached, to.Label)
	}
	assert.Equal(t, []string{"GET /api/prefs/assistant_config"}, reached,
		"the synthetic client must reach the route, and exactly one of them")
}
