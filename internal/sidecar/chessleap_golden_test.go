package sidecar_test

// V.2 migration guard: the templ parse migrating behind the sidecar router
// must not change the chessleap graph by one byte.
//
// Two layers (pinned hazard, docs/versioning-matrix-plan.md):
//  1. Live pre/post comparison in this commit: the full node+edge set from a
//     sidecar-routed index run equals the in-process (pre-migration path)
//     run — the fallback path IS the pre-migration parser, so this compares
//     the exact code paths the migration swapped.
//  2. A committed snapshot of the templ-derived subgraph
//     (testdata/golden/chessleap_templ_graph.json) guards future commits.
//     Regenerate after an intentional change:
//       go test ./internal/sidecar/ -run TestChessleapSidecar -update-golden
//
// Chessleap is a private local repo (symlinked to eval/.cache/chessleap);
// the tests skip when it is absent (SkippedCorpus.LocalOnly).

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/indexer"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/sidecar"
	"github.com/lordsonvimal/polyflow/internal/toolchain"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

var updateGolden = flag.Bool("update-golden", false, "regenerate testdata/golden snapshots")

const templGoldenFile = "testdata/golden/chessleap_templ_graph.json"

func chessleapPath(t *testing.T) string {
	t.Helper()
	p := filepath.Join(repoRoot(), "eval", ".cache", "chessleap")
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}
	if _, err := os.Stat(p); os.IsNotExist(err) {
		t.Skip("chessleap eval repo not available; clone to eval/.cache/chessleap (SkippedCorpus.LocalOnly)")
	}
	return p
}

// chessleapGraph indexes chessleap with the sidecar dir pinned to dir and
// returns the full node/edge sets (sorted by ID) plus the stored toolchain
// coverage and profile meta.
func chessleapGraph(t *testing.T, dir string) (nodes []*graph.Node, edges []*graph.Edge, coverage, profiles string) {
	t.Helper()
	t.Setenv(sidecar.SidecarDirEnv, dir)

	chessleap := chessleapPath(t)
	cfg, err := workspace.Load(filepath.Join(chessleap, "workspace.yaml"))
	require.NoError(t, err)
	for i := range cfg.Services {
		cfg.Services[i].Path = filepath.Join(chessleap, cfg.Services[i].Path)
	}

	dbDir := t.TempDir()
	stats, err := indexer.Run(context.Background(), indexer.Options{
		Config: cfg, DBDir: dbDir, PatternsDir: filepath.Join(repoRoot(), "patterns"), Full: true,
		// Only nodes/edges are compared; embeddings (~80% of index time) are
		// not under test, so skip the embedding pass.
		NoEmbed: true,
	})
	require.NoError(t, err)
	t.Logf("chessleap (sidecar dir %q): files=%d nodes=%d edges=%d crosslinks=%d",
		dir, stats.TotalFiles, stats.Nodes, stats.Edges, stats.CrossLinks)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	defer store.Close()
	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)

	for _, n := range idx.Nodes {
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	seen := map[string]bool{}
	for _, out := range idx.OutEdges {
		for _, e := range out {
			if !seen[e.ID] {
				seen[e.ID] = true
				edges = append(edges, e)
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })

	coverage, err = store.GetMeta(context.Background(), "toolchain_coverage")
	require.NoError(t, err)
	profiles, err = store.GetMeta(context.Background(), "toolchain_profiles")
	require.NoError(t, err)
	return nodes, edges, coverage, profiles
}

// templSubgraph projects the templ-derived subset: every templ-language node
// plus every edge touching one.
type templGolden struct {
	Nodes []*graph.Node `json:"nodes"`
	Edges []*graph.Edge `json:"edges"`
}

func templSubgraph(nodes []*graph.Node, edges []*graph.Edge) templGolden {
	templIDs := map[string]bool{}
	var g templGolden
	for _, n := range nodes {
		if n.Language == "templ" {
			templIDs[n.ID] = true
			g.Nodes = append(g.Nodes, n)
		}
	}
	for _, e := range edges {
		if templIDs[e.From] || templIDs[e.To] {
			g.Edges = append(g.Edges, e)
		}
	}
	return g
}

// TestChessleapSidecarByteIdentical is the migration guard: index chessleap
// through the sidecar and through the pre-migration in-process path, and
// require the marshaled node+edge sets to be byte-identical. Also asserts
// the sidecar was genuinely used (no in-process fallback note) and the
// profile stamps landed.
func TestChessleapSidecarByteIdentical(t *testing.T) {
	if testing.Short() {
		t.Skip("two full chessleap index runs; skipped in -short mode")
	}
	binDir := builtSidecarDir(t)

	sideNodes, sideEdges, sideCov, sideProf := chessleapGraph(t, binDir)
	inNodes, inEdges, inCov, _ := chessleapGraph(t, t.TempDir()) // empty dir → in-process path

	require.NotEmpty(t, sideNodes)
	sideNodesJSON, err := json.Marshal(sideNodes)
	require.NoError(t, err)
	inNodesJSON, err := json.Marshal(inNodes)
	require.NoError(t, err)
	sideEdgesJSON, err := json.Marshal(sideEdges)
	require.NoError(t, err)
	inEdgesJSON, err := json.Marshal(inEdges)
	require.NoError(t, err)
	assert.Equal(t, string(inNodesJSON), string(sideNodesJSON), "node set must be byte-identical across the migration")
	assert.Equal(t, string(inEdgesJSON), string(sideEdgesJSON), "edge set must be byte-identical across the migration")

	// The sidecar run must actually have used the sidecar…
	var notes []toolchain.CoverageNote
	require.NoError(t, json.Unmarshal([]byte(sideCov), &notes))
	for _, n := range notes {
		assert.NotEqual(t, "in-process", n.UsedProfile,
			"sidecar run fell back in-process — the guard would compare nothing: %s", sideCov)
	}
	// …and the in-process run must have ledgered its fallback.
	require.NoError(t, json.Unmarshal([]byte(inCov), &notes))
	fallback := false
	for _, n := range notes {
		if n.Tool == toolchain.ToolTempl && n.UsedProfile == "in-process" {
			fallback = true
		}
	}
	assert.True(t, fallback, "missing-sidecar run must record the in-process fallback: %s", inCov)

	// Labeling: profile_used / backend_version stamped in graph meta.
	assert.Contains(t, sideProf, `"templ-v0.3"`)
	assert.Contains(t, sideProf, `"datastar-v1"`)

	// Committed regression snapshot of the templ-derived subgraph.
	g := templSubgraph(sideNodes, sideEdges)
	require.NotEmpty(t, g.Nodes, "chessleap must yield templ nodes")
	data, err := json.MarshalIndent(g, "", "  ")
	require.NoError(t, err)
	data = append(data, '\n')

	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(templGoldenFile), 0o755))
		require.NoError(t, os.WriteFile(templGoldenFile, data, 0o644))
		t.Logf("golden snapshot updated: %d nodes, %d edges → %s", len(g.Nodes), len(g.Edges), templGoldenFile)
		return
	}
	golden, err := os.ReadFile(templGoldenFile)
	if os.IsNotExist(err) {
		t.Fatalf("golden snapshot missing; generate with:\n  go test ./internal/sidecar/ -run TestChessleapSidecar -update-golden")
	}
	require.NoError(t, err)
	assert.Equal(t, string(golden), string(data),
		"templ subgraph diverged from golden snapshot; if intentional, regenerate with -update-golden")
}
