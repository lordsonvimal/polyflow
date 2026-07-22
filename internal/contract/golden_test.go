package contract_test

// Golden harness for chessleap cross-service edges.
//
// The original G.1 plan was to snapshot the bespoke-linker edge set and assert
// the contract engine reproduces it. The bespoke linkers were deleted before
// that snapshot was ever generated (chessleap was unavailable at G.1 time), so
// the harness is now a REGRESSION snapshot: the committed golden file captures
// the contract engine's edge set on chessleap, and this test fails when any
// later change silently alters it. See docs/contract-matching-plan.md
// (Verification) for the amendment note.
//
// The chessleap repo is private; clone (or symlink) it to eval/.cache/chessleap
// before running. The test skips automatically when the repo is not present.
//
// Regenerate the snapshot after an intentional edge-set change:
//
//	go test ./internal/contract/ -run TestGoldenChessleapParity -update-golden
import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	contractdata "github.com/lordsonvimal/polyflow/contracts"
	"github.com/lordsonvimal/polyflow/internal/contract"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/indexer"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

var updateGolden = flag.Bool("update-golden", false, "regenerate testdata/golden snapshots")

const goldenFile = "testdata/golden/chessleap_contract_edges.json"

// goldenEdge is the stable, machine-portable projection of a contract edge.
type goldenEdge struct {
	ID         string `json:"id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Type       string `json:"type"`
	Label      string `json:"label,omitempty"`
	Confidence string `json:"confidence"`
}

// chessleapCachePath returns the path to the local chessleap clone used by
// the eval harness, or "" if it cannot be determined.
func chessleapCachePath() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	// internal/contract/golden_test.go → repo root is 2 levels up
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	p := filepath.Join(root, "eval", ".cache", "chessleap")
	// The cache entry may be a symlink to a local clone; the indexer's file
	// walk does not traverse symlinked roots, so resolve it here.
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// contractEdgeTypes returns the set of edge types any embedded rule can emit.
func contractEdgeTypes(t *testing.T) map[graph.EdgeType]bool {
	t.Helper()
	rules, err := contract.Load(contractdata.FS, "")
	require.NoError(t, err)
	types := make(map[graph.EdgeType]bool, len(rules))
	for _, r := range rules {
		types[r.Edge.Type] = true
	}
	return types
}

// indexChessleapContractEdges indexes the chessleap clone into a temp DB and
// returns every edge whose type is emitted by a contract rule, sorted by ID.
func indexChessleapContractEdges(t *testing.T, chessleapDir string) []goldenEdge {
	t.Helper()

	cfg, err := workspace.Load(filepath.Join(chessleapDir, "workspace.yaml"))
	require.NoError(t, err)
	for i := range cfg.Services {
		cfg.Services[i].Path = filepath.Join(chessleapDir, cfg.Services[i].Path)
	}

	dbDir := t.TempDir()
	stats, err := indexer.Run(context.Background(), indexer.Options{
		Config:      cfg,
		DBDir:       dbDir,
		PatternsDir: "../../patterns",
		Full:        true,
		// This harness inspects only contract edges — the embedding pass is
		// ~80% of index time and irrelevant here, so skip it (5x faster).
		NoEmbed: true,
	})
	require.NoError(t, err)
	t.Logf("indexed chessleap: files=%d parsed=%d errors=%d nodes=%d edges=%d crosslinks=%d",
		stats.TotalFiles, stats.ParsedFiles, stats.ErrorFiles, stats.Nodes, stats.Edges, stats.CrossLinks)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)

	wanted := contractEdgeTypes(t)
	var edges []goldenEdge
	seen := make(map[string]bool)
	for _, out := range idx.OutEdges {
		for _, e := range out {
			if !wanted[e.Type] || seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			edges = append(edges, edgeToGolden(e))
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
	return edges
}

func edgeToGolden(e *graph.Edge) goldenEdge {
	return goldenEdge{
		ID:         e.ID,
		From:       e.From,
		To:         e.To,
		Type:       string(e.Type),
		Label:      e.Label,
		Confidence: e.Confidence,
	}
}

// TestGoldenChessleapParity asserts the contract engine's chessleap edge set
// is identical to the committed golden snapshot. Run with -update-golden to
// regenerate after an intentional change.
func TestGoldenChessleapParity(t *testing.T) {
	chessleap := chessleapCachePath()
	if _, err := os.Stat(chessleap); os.IsNotExist(err) {
		t.Skip("chessleap eval repo not available; clone to eval/.cache/chessleap")
	}

	edges := indexChessleapContractEdges(t, chessleap)
	require.NotEmpty(t, edges, "chessleap must produce contract edges — an empty set means indexing silently broke")

	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(goldenFile), 0o755))
		data, err := json.MarshalIndent(edges, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(goldenFile, append(data, '\n'), 0o644))
		t.Logf("golden snapshot updated: %d edges → %s", len(edges), goldenFile)
		return
	}

	data, err := os.ReadFile(goldenFile)
	if os.IsNotExist(err) {
		t.Fatalf("golden snapshot missing; generate it with:\n  go test ./internal/contract/ -run TestGoldenChessleapParity -update-golden")
	}
	require.NoError(t, err)
	var golden []goldenEdge
	require.NoError(t, json.Unmarshal(data, &golden))

	require.Equal(t, golden, edges,
		"contract edge set diverged from golden snapshot; if intentional, regenerate with -update-golden")
}

// TestGoldenChessleapDeterminism indexes chessleap twice and asserts the
// contract edge set is byte-identical — guards the deterministic-ordering
// invariant (wildcard tier previously iterated a Go map).
func TestGoldenChessleapDeterminism(t *testing.T) {
	if testing.Short() {
		t.Skip("two full index runs; skipped in -short mode")
	}
	chessleap := chessleapCachePath()
	if _, err := os.Stat(chessleap); os.IsNotExist(err) {
		t.Skip("chessleap eval repo not available; clone to eval/.cache/chessleap")
	}

	first := indexChessleapContractEdges(t, chessleap)
	second := indexChessleapContractEdges(t, chessleap)
	require.Equal(t, first, second, "contract edge set must be identical across runs")
}
