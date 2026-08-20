package e2e_test

// Tier NV.6 regression fixture, encoding the live 2026-08-20 fleet-orion
// session that motivated docs/trace-context-noise-visibility-plan.md: an
// UppyUploader-shaped JSX component whose DOM-click-handler edges buried a
// real multipart-upload call chain, and a FastUploadsController-shaped Rails
// action whose before_action fan-out buried its own real callee. Both shapes
// go through the full indexer.Run pipeline (not synthetic graph structs, per
// NV.2/NV.3/NV.4's unit fixtures) so a regression here means real parsing +
// classification broke, not just the classification function in isolation.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ctxpkg "github.com/lordsonvimal/polyflow/internal/context"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/indexer"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/trace"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// indexNoiseVisibility runs the full production pipeline over the
// noise_visibility workspace (one JSX service, one Rails service) and
// returns the adjacency index.
func indexNoiseVisibility(t *testing.T) *graph.AdjacencyIndex {
	t.Helper()

	cfg := &workspace.WorkspaceConfig{
		Name:    "noise-visibility",
		Version: "1",
		Services: []workspace.Service{
			{Name: "web", Path: "testdata/noise_visibility/web", Language: "javascript"},
			{Name: "rails", Path: "testdata/noise_visibility/rails", Language: "ruby"},
		},
	}

	dbDir := t.TempDir()
	_, err := indexer.Run(context.Background(), indexer.Options{
		Config:      cfg,
		DBDir:       dbDir,
		PatternsDir: "../../patterns",
	})
	require.NoError(t, err)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)
	return idx
}

func findNoiseNode(idx *graph.AdjacencyIndex, service, label string) *graph.Node {
	for _, n := range idx.Nodes {
		if n.Service == service && n.Label == label {
			return n
		}
	}
	return nil
}

// TestNoiseVisibility_JSXTrace_DefaultHidesRenderTreeShowsBusinessCall
// reproduces failure mode 1 from the session: a component's JSX render-tree
// edges (className-selector `element` nodes) must not crowd out the real
// onClick -> handler -> ... business chain in default trace output.
func TestNoiseVisibility_JSXTrace_DefaultHidesRenderTreeShowsBusinessCall(t *testing.T) {
	t.Parallel()
	idx := indexNoiseVisibility(t)

	root := findNoiseNode(idx, "web", "UploadWidget")
	require.NotNil(t, root, "UploadWidget component/function node must exist")

	r := trace.Run(idx, root.ID, "forward", 0, false, 0, nil, 0)
	require.NotNil(t, r)

	require.Len(t, r.Chains, 1, "default trace should surface exactly the business chain, got:\n%s", allChains(r))
	text := r.Chains[0].Text
	assert.Contains(t, text, "handleUpload")
	assert.Contains(t, text, "startMultipartUpload")
	assert.Contains(t, text, "completeMultipartUpload")

	for _, c := range r.Chains {
		assert.NotContains(t, c.Text, ".btn", "render-tree element edges must not appear in default output")
		assert.NotContains(t, c.Text, ".upload-widget", "render-tree element edges must not appear in default output")
	}

	assert.Equal(t, 2, r.HiddenByClass[graph.NoiseRenderTree],
		"the two className-selector element edges must be tallied, not silently dropped")
}

// TestNoiseVisibility_RailsTrace_DefaultHidesFilterChainShowsBusinessCall
// reproduces failure mode 2: a controller action's before_action fan-out
// must not crowd out the action's real callee.
func TestNoiseVisibility_RailsTrace_DefaultHidesFilterChainShowsBusinessCall(t *testing.T) {
	t.Parallel()
	idx := indexNoiseVisibility(t)

	root := findNoiseNode(idx, "rails", "complete_multipart")
	require.NotNil(t, root, "complete_multipart controller action node must exist")

	r := trace.Run(idx, root.ID, "forward", 0, false, 0, nil, 0)
	require.NotNil(t, r)

	require.Len(t, r.Chains, 1, "default trace should surface exactly the business chain, got:\n%s", allChains(r))
	assert.Contains(t, r.Chains[0].Text, "finalize_upload")

	for _, c := range r.Chains {
		assert.NotContains(t, c.Text, "authenticate_user!", "before_action filter edges must not appear in default output")
		assert.NotContains(t, c.Text, "verify_organization!", "before_action filter edges must not appear in default output")
	}

	assert.Equal(t, 2, r.HiddenByClass[graph.NoiseFilterChain],
		"the two before_action edges must be tallied, not silently dropped")
}

// TestNoiseVisibility_Context_DefaultHidesFilterChainShowsBusinessCall proves
// the same shape holds for `context` (NV.3), not just `trace` (NV.2) —
// consuming the same ClassifyEdgeNoise substrate through a structurally
// different (flat-list, not chain) traversal.
func TestNoiseVisibility_Context_DefaultHidesFilterChainShowsBusinessCall(t *testing.T) {
	t.Parallel()
	idx := indexNoiseVisibility(t)

	root := findNoiseNode(idx, "rails", "complete_multipart")
	require.NotNil(t, root, "complete_multipart controller action node must exist")

	result := ctxpkg.Build(idx, root.ID, "debug", 5, false, 0, nil)
	require.NotNil(t, result)

	var downstreamLabels []string
	for _, n := range result.Downstream {
		downstreamLabels = append(downstreamLabels, n.Label)
	}
	joined := strings.Join(downstreamLabels, ",")
	assert.Contains(t, joined, "finalize_upload")
	assert.NotContains(t, joined, "authenticate_user!")
	assert.NotContains(t, joined, "verify_organization!")

	assert.Equal(t, 2, result.HiddenByClass[graph.NoiseFilterChain],
		"the two before_action edges must be tallied, not silently dropped")
}
