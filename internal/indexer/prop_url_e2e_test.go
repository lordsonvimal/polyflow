package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// Tier UB.2 end to end. The endpoint is computed in two parent components and
// passed to a shared child as the `dataURL` prop; the child issues the request
// through a prop-injected transport. js_prop_clients ledgers the child call
// `prop_client_dynamic_url`; js_prop_urls has to index the two producers, join
// on (component, prop) and mint one client per distinct URL so the contract
// engine reaches each express route. A linker unit test with hand-built nodes
// cannot check the cross-file producer index.
func TestRun_PropURLCrossesJSXProp(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "orion")
	require.NoError(t, os.MkdirAll(filepath.Join(svc, "components"), 0o755))

	writeFile(t, svc, "package.json", `{"name":"orion","dependencies":{"express":"^4.0.0"}}`)

	writeFile(t, svc, "routes.js", `const router = require("express").Router();
router.get("/api/widgets/:id/usage", (req, res) => res.end());
router.get("/api/widget_groups/:id/usage", (req, res) => res.end());
router.get("/api/gadgets/:id/usage", (req, res) => res.end());
module.exports = router;
`)

	writeFile(t, filepath.Join(svc, "components"), "ComponentWithAjaxStatus.jsx", `import React from "react";
const ComponentWithAjaxStatus = function (WrappedComponent) {
  class component extends React.Component {
    get = (msg, url) => {
      const request = { method: "GET", url };
      return this.ajax(msg, request);
    };
    ajax = (msg, request) => window.$.ajax(request);
    render() {
      return <WrappedComponent ajaxStatus={this} {...this.props} />;
    }
  }
  return component;
};
export default ComponentWithAjaxStatus;
`)

	writeFile(t, filepath.Join(svc, "components"), "WhereUsed.jsx", `import React from "react";
import ComponentWithAjaxStatus from "./ComponentWithAjaxStatus";
class WhereUsed extends React.Component {
  load = () => {
    const { ajaxStatus, dataURL } = this.props;
    ajaxStatus.get("Loading...", dataURL);
  };
  render() { return <div onClick={this.load} />; }
}
export default ComponentWithAjaxStatus(WhereUsed);
`)

	writeFile(t, filepath.Join(svc, "components"), "WidgetPane.jsx", `import React from "react";
import WhereUsed from "./WhereUsed";
export function WidgetPane({ id, kind }) {
  let url = `+"`/api/widgets/${id}/usage`"+`;
  if (kind === "group") {
    url = `+"`/api/widget_groups/${id}/usage`"+`;
  }
  return <WhereUsed dataURL={url} />;
}
`)

	writeFile(t, filepath.Join(svc, "components"), "GadgetPane.jsx", `import React from "react";
import WhereUsed from "./WhereUsed";
export const GadgetPane = props => (
  <WhereUsed dataURL={`+"`/api/gadgets/${props.id}/usage`"+`} />
);
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

	urls := map[string]string{} // resolved url -> handler label
	handlerHits := map[string]int{}
	for _, n := range idx.Nodes {
		if n.Meta["spa"] != "prop_url" {
			continue
		}
		require.Equal(t, "dataURL", n.Meta["prop"])
		require.NotEmpty(t, n.Meta["producer"])
		var handlers []string
		for _, e := range idx.OutEdges[n.ID] {
			if e.Type != graph.EdgeTypeHTTPCall {
				continue
			}
			if to := idx.Nodes[e.To]; to != nil {
				handlers = append(handlers, to.Label)
				handlerHits[to.ID]++
			}
		}
		require.Lenf(t, handlers, 1, "client %s reached %v", n.Meta["url"], handlers)
		urls[n.Meta["url"]] = handlers[0]
	}

	require.Len(t, urls, 3, "one client per distinct producer URL")
	require.Contains(t, urls, "/api/widgets/*/usage")
	require.Contains(t, urls, "/api/widget_groups/*/usage")
	require.Contains(t, urls, "/api/gadgets/*/usage")
	for id, hits := range handlerHits {
		require.LessOrEqualf(t, hits, 1, "handler %s reached %d times (fan-out)", id, hits)
	}
}
