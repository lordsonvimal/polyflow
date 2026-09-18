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

// Tier UB.3 end to end. The parent owns the transport function
// (`postToServer`, whose third parameter is the URL) and hands it to a shared
// child as the `postToServer` prop; the child calls it with a concrete path.
// js_prop_clients ledgers the parent's `ajaxStatus.ajax` call
// `prop_client_dynamic_url` (the URL is a bare parameter); js_prop_transport
// has to index the prop pass, read the argument at parameter index 2 at each
// child call site, and mint one client per distinct URL at the parent call so
// the contract engine reaches each express route.
func TestRun_PropTransportCrossesJSXProp(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "orion")
	require.NoError(t, os.MkdirAll(filepath.Join(svc, "components"), 0o755))

	writeFile(t, svc, "package.json", `{"name":"orion","dependencies":{"express":"^4.0.0"}}`)

	writeFile(t, svc, "routes.js", `const router = require("express").Router();
router.put("/api/schedules/:id", (req, res) => res.end());
router.put("/api/schedules/:id/publish", (req, res) => res.end());
module.exports = router;
`)

	writeFile(t, filepath.Join(svc, "components"), "ComponentWithAjaxStatus.jsx", `import React from "react";
const ComponentWithAjaxStatus = function (WrappedComponent) {
  class component extends React.Component {
    ajax = (msg, request) => window.$.ajax(request);
    render() {
      return <WrappedComponent ajaxStatus={this} {...this.props} />;
    }
  }
  return component;
};
export default ComponentWithAjaxStatus;
`)

	writeFile(t, filepath.Join(svc, "components"), "ScheduleTopLevel.jsx", `import React from "react";
import ScheduleGrid from "./ScheduleGrid";
export default class ScheduleTopLevel extends React.Component {
  postToServer = (ajaxStatus, data, updateURL) => {
    return ajaxStatus.ajax("Saving...", { url: updateURL, data, method: "PUT" });
  };
  render() {
    return <ScheduleGrid postToServer={this.postToServer} />;
  }
}
`)

	writeFile(t, filepath.Join(svc, "components"), "ScheduleGrid.jsx", `import React from "react";
export default class ScheduleGrid extends React.Component {
  save = () => {
    this.props.postToServer(this.props.ajaxStatus, this.state.data, `+"`/api/schedules/${this.props.id}`"+`);
  };
  publish = () => {
    this.props.postToServer(this.props.ajaxStatus, {}, `+"`/api/schedules/${this.props.id}/publish`"+`);
  };
}
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

	urls := map[string]string{}
	handlerHits := map[string]int{}
	for _, n := range idx.Nodes {
		if n.Meta["spa"] != "prop_transport" {
			continue
		}
		require.Equal(t, "PUT", n.Meta["method"])
		require.Equal(t, "postToServer", n.Meta["via"])
		require.NotEmpty(t, n.Meta["caller"])
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

	require.Len(t, urls, 2, "one client per distinct argument URL")
	require.Contains(t, urls, "/api/schedules/*")
	require.Contains(t, urls, "/api/schedules/*/publish")
	for id, hits := range handlerHits {
		require.LessOrEqualf(t, hits, 1, "handler %s reached %d times (fan-out)", id, hits)
	}
}

// Tier RC.6 (docs/js-declarative-composition-cluster-plan.md): the same
// shape as above, but the parent does not hand postToServer straight to the
// component that calls it — it hands it to a pass-through wrapper
// (RightPane's real-world shape), which destructures it off its own props
// and re-forwards it, unqualified, to the actual caller. The reverse
// crossing has to follow that second hop; before RC.6 it silently minted
// nothing here (the value resolved, but its nearest Src named the wrong
// crossing kind, so the caller's kind filter rejected it).
func TestRun_PropTransportCrossesJSXPropThroughWrapper(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "orion")
	require.NoError(t, os.MkdirAll(filepath.Join(svc, "components"), 0o755))

	writeFile(t, svc, "package.json", `{"name":"orion","dependencies":{"express":"^4.0.0"}}`)

	writeFile(t, svc, "routes.js", `const router = require("express").Router();
router.put("/api/schedules/:id", (req, res) => res.end());
module.exports = router;
`)

	writeFile(t, filepath.Join(svc, "components"), "ComponentWithAjaxStatus.jsx", `import React from "react";
const ComponentWithAjaxStatus = function (WrappedComponent) {
  class component extends React.Component {
    ajax = (msg, request) => window.$.ajax(request);
    render() {
      return <WrappedComponent ajaxStatus={this} {...this.props} />;
    }
  }
  return component;
};
export default ComponentWithAjaxStatus;
`)

	writeFile(t, filepath.Join(svc, "components"), "ScheduleTopLevel.jsx", `import React from "react";
import RightPane from "./RightPane";
export default class ScheduleTopLevel extends React.Component {
  postToServer = (ajaxStatus, data, updateURL) => {
    return ajaxStatus.ajax("Saving...", { url: updateURL, data, method: "PUT" });
  };
  render() {
    return <RightPane postToServer={this.postToServer} />;
  }
}
`)

	writeFile(t, filepath.Join(svc, "components"), "RightPane.jsx", `import React from "react";
import ScheduleGrid from "./ScheduleGrid";
export default class RightPane extends React.Component {
  render() {
    const { postToServer } = this.props;
    return <ScheduleGrid postToServer={postToServer} />;
  }
}
`)

	writeFile(t, filepath.Join(svc, "components"), "ScheduleGrid.jsx", `import React from "react";
export default class ScheduleGrid extends React.Component {
  save = () => {
    this.props.postToServer(this.props.ajaxStatus, this.state.data, `+"`/api/schedules/${this.props.id}`"+`);
  };
}
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

	urls := map[string]string{}
	for _, n := range idx.Nodes {
		if n.Meta["spa"] != "prop_transport" {
			continue
		}
		require.Equal(t, "PUT", n.Meta["method"])
		var handlers []string
		for _, e := range idx.OutEdges[n.ID] {
			if e.Type != graph.EdgeTypeHTTPCall {
				continue
			}
			if to := idx.Nodes[e.To]; to != nil {
				handlers = append(handlers, to.Label)
			}
		}
		require.Lenf(t, handlers, 1, "client %s reached %v", n.Meta["url"], handlers)
		urls[n.Meta["url"]] = handlers[0]
	}

	require.Len(t, urls, 1, "the call through the pass-through wrapper still mints")
	require.Contains(t, urls, "/api/schedules/*")
}
