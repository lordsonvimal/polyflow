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

// Tier VG.3 deliverable 4 (docs/js-value-graph-pilot-plan.md): every http_client
// node the engine path resolves, and every http_call edge out of it, carries
// SA.1 provenance with Layer "L2" and a non-empty Rule naming the valuegraph
// spec — not the coarse L5/<pass> fallback writeEdges stamps otherwise.
func TestRun_ValuegraphLocalURLProvenance(t *testing.T) {
	t.Setenv("PF_VALUEGRAPH", "1")

	dir := t.TempDir()
	svc := filepath.Join(dir, "orion")
	require.NoError(t, os.MkdirAll(svc, 0o755))

	writeFile(t, svc, "package.json", `{"name":"orion","dependencies":{"express":"^4.0.0"}}`)
	writeFile(t, svc, "routes.js", `const router = require("express").Router();
router.get("/api/things/:id", (req, res) => res.end());
module.exports = router;
`)
	writeFile(t, svc, "client.js", `function loadThing(id) {
  const url = `+"`/api/things/${id}`"+`;
  window.$.ajax({ url, method: "GET" });
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

	var client *graph.Node
	for _, n := range idx.Nodes {
		if n.Type == graph.NodeTypeHTTPClient && n.Meta["url"] == "/api/things/*" {
			client = n
			break
		}
	}
	require.NotNil(t, client, "engine path did not resolve the local-binding URL")
	require.Equal(t, "L2", client.Meta["vg_layer"])
	require.Equal(t, "valuegraph/javascript#local_binding", client.Meta["vg_rule"])

	var httpCalls int
	for _, e := range idx.OutEdges[client.ID] {
		if e.Type != graph.EdgeTypeHTTPCall {
			continue
		}
		httpCalls++
		require.NotEmpty(t, e.Sources, "edge %s has no Sources", e.ID)
		require.Equal(t, "L2", e.Sources[0].Layer, "edge %s layer", e.ID)
		require.Equal(t, "valuegraph/javascript#local_binding", e.Sources[0].Rule, "edge %s rule", e.ID)
	}
	require.Positive(t, httpCalls, "no http_call edge reached the route handler")
}
