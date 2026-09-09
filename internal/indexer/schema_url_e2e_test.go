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

// Tier MS.1/MS.2 end to end. The asset lives in a different file from every call
// site, so a linker unit test with hand-built nodes cannot check the join: the
// schema_url_tables pass has to discover the YAML by route corroboration and
// learn the accessor family from urls.js, and the JS mint sites have to pin the
// entity and resolve the key (direct read and accessor call) so the contract
// engine reaches the express route.
func TestRun_SchemaDrivenURLResolution(t *testing.T) {
	dir := t.TempDir()
	svc := filepath.Join(dir, "orion")
	require.NoError(t, os.MkdirAll(filepath.Join(svc, "config"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(svc, "components"), 0o755))

	writeFile(t, svc, "package.json", `{"name":"orion","dependencies":{"express":"^4.0.0"}}`)

	writeFile(t, filepath.Join(svc, "config"), "orion-resources.yml", `resources:
  widget:
    get: /api/widgets/<id>
    update: /api/widgets/<id>
  gadget:
    endpoint: /api/gadgets
    reorder: /api/gadgets/:id/reorder
  sprocket:
    list: /api/sprockets
  cog:
    list: /api/cogs
  bolt:
    list: /api/bolts
`)

	writeFile(t, svc, "routes.js", `const router = require("express").Router();
router.put("/api/widgets/:id", (req, res) => res.end());
router.post("/api/gadgets/:id/reorder", (req, res) => res.end());
router.get("/api/gadgets/:id", (req, res) => res.end());
router.get("/api/sprockets", (req, res) => res.end());
router.get("/api/cogs", (req, res) => res.end());
router.get("/api/bolts", (req, res) => res.end());
router.get("/api/gadgets", (req, res) => res.end());
module.exports = router;
`)

	// A non-cedar accessor family, learnt from these bodies alone (MS.2a).
	writeFile(t, filepath.Join(svc, "components"), "urls.js", `export function listURL(res) { return res.list; }
export function createURL(res) { return res.endpoint || listURL(res); }
`)

	writeFile(t, filepath.Join(svc, "components"), "WidgetPane.js", `import resources from "../config/orion-resources.yml";
import { createURL } from "./urls";

export function save(values) {
  const r = resources.widget;
  fetch(r.update.replace("<id>", values.id), { method: "PUT" });
}

export function reorder(id) {
  fetch(resources["gadget"].reorder.replace(":id", id), { method: "POST" });
}

export function add() {
  const g = resources.gadget;
  fetch(createURL(g), { method: "GET" });
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

	// Every schema-resolved client reaches exactly one handler.
	got := map[string]string{} // schema_key -> handler label
	minted := 0
	for _, n := range idx.Nodes {
		if n.Meta["url_origin"] != "schema_asset" {
			continue
		}
		minted++
		require.Equal(t, "orion-resources.yml", filepath.Base(n.Meta["schema_file"]))
		var handlers []string
		for _, e := range idx.OutEdges[n.ID] {
			if e.Type != graph.EdgeTypeHTTPCall {
				continue
			}
			if to := idx.Nodes[e.To]; to != nil {
				handlers = append(handlers, to.Label)
			}
		}
		require.Lenf(t, handlers, 1, "client %s (%s) reached %v", n.Label, n.Meta["schema_key"], handlers)
		got[n.Meta["schema_entity"]+"."+n.Meta["schema_key"]] = handlers[0]
	}

	require.Equal(t, 3, minted, "two direct key reads + one through a learnt accessor")
	require.Contains(t, got["widget.update"], "/api/widgets/:id")
	require.Contains(t, got["gadget.reorder"], "/api/gadgets/:id/reorder")
	require.Contains(t, got["gadget.endpoint"], "/api/gadgets", "resolved through the learnt createURL accessor")
}
