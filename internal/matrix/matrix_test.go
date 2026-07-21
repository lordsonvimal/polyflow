// Package matrix_test is the V.4 versioned-toolchain matrix harness: every
// (tool, version) cell under testdata/matrix/<tool>@<ver>/ is a real fixture
// project indexed through the full pipeline, asserted against expected.json.
//
// Gate semantics (rule 4, docs/phases.md), both directions:
//   - every DefaultRegistry row must be exercised by at least one cell that
//     selects it without inference (registered version without a fixture → fail);
//   - every cell must land on a registry row, or explicitly declare itself an
//     inference-fallback cell (orphaned cell that tests nothing → fail).
//
// All violations from both directions are collected and reported together —
// no early exit hides a second failure. Cells iterate in sorted name order
// (rule 2); a two-run determinism test asserts byte-identical stored graphs.
package matrix_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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

func repoRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

func matrixDir() string { return filepath.Join(repoRoot(), "testdata", "matrix") }

var (
	sidecarBinDir string
	buildErr      error
)

// TestMain builds the real templ sidecar once so the templ/datastar cells
// exercise genuine sidecar dispatch, not the in-process fallback.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "polyflow-matrix-test")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mkdtemp: %v\n", err)
		os.Exit(1)
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, "polyflow-parse-templ"), "./cmd/polyflow-parse-templ")
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		buildErr = fmt.Errorf("build sidecar: %v\n%s", err, out)
	} else {
		sidecarBinDir = dir
		os.Setenv(sidecar.SidecarDirEnv, dir)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// nodePredicate matches stored nodes. Type is required; label / label_contains
// / meta narrow the match. Count, when set (>0), pins the exact match count;
// otherwise must-predicates require ≥1 and must-not-predicates require 0.
type nodePredicate struct {
	Type          string            `json:"type"`
	Label         string            `json:"label,omitempty"`
	LabelContains string            `json:"label_contains,omitempty"`
	Meta          map[string]string `json:"meta,omitempty"`
	Count         int               `json:"count,omitempty"`
}

// edgePredicate matches stored edges by type and endpoint node labels.
type edgePredicate struct {
	Type              string `json:"type"`
	FromLabel         string `json:"from_label,omitempty"`
	FromLabelContains string `json:"from_label_contains,omitempty"`
	ToLabel           string `json:"to_label,omitempty"`
	ToLabelContains   string `json:"to_label_contains,omitempty"`
	Count             int    `json:"count,omitempty"`
}

// cellExpectation is the schema of each cell's expected.json.
type cellExpectation struct {
	Language     string          `json:"language"`             // workspace service language
	Profile      string          `json:"profile"`              // expected profile stamp for the cell's tool
	Inferred     bool            `json:"inferred"`             // true only for explicit fail-safe (nearest-newest) cells
	MustNodes    []nodePredicate `json:"must_nodes,omitempty"` // ≥1 match each (or exact Count)
	MustNotNodes []nodePredicate `json:"must_not_nodes,omitempty"`
	MustEdges    []edgePredicate `json:"must_edges,omitempty"`
	MustNotEdges []edgePredicate `json:"must_not_edges,omitempty"`
}

type cell struct {
	Name    string // directory name, "<tool>@<version>"
	Dir     string
	Tool    toolchain.Tool
	Version string
	Exp     cellExpectation
}

// loadCells reads testdata/matrix in sorted order. Malformed cells are
// returned as errors, never skipped (a broken cell must fail the gate).
func loadCells(t *testing.T) []cell {
	t.Helper()
	entries, err := os.ReadDir(matrixDir())
	require.NoError(t, err, "read testdata/matrix")
	require.NotEmpty(t, entries, "testdata/matrix has no cells")

	var cells []cell
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		dir := filepath.Join(matrixDir(), name)
		tool, ver, ok := strings.Cut(name, "@")
		require.True(t, ok, "cell %q: name must be <tool>@<version>", name)
		require.NotEmpty(t, tool, "cell %q: empty tool", name)
		require.NotEmpty(t, ver, "cell %q: empty version", name)

		data, err := os.ReadFile(filepath.Join(dir, "expected.json"))
		require.NoError(t, err, "cell %q: expected.json is mandatory", name)
		var exp cellExpectation
		require.NoError(t, json.Unmarshal(data, &exp), "cell %q: expected.json", name)
		require.NotEmpty(t, exp.Profile, "cell %q: expected.json must pin profile", name)
		require.NotEmpty(t, exp.Language, "cell %q: expected.json must pin language", name)

		info, err := os.Stat(filepath.Join(dir, "project"))
		require.NoError(t, err, "cell %q: project/ fixture is mandatory", name)
		require.True(t, info.IsDir())

		cells = append(cells, cell{Name: name, Dir: dir, Tool: toolchain.Tool(tool), Version: ver, Exp: exp})
	}
	sort.Slice(cells, func(i, j int) bool { return cells[i].Name < cells[j].Name })
	return cells
}

// indexCell runs the full indexer on a cell's fixture project into dbDir and
// returns the opened store (caller closes).
func indexCell(t *testing.T, c cell, dbDir string) *graph.SQLiteStore {
	t.Helper()
	cfg := &workspace.WorkspaceConfig{
		Name: "matrix-" + c.Name, Version: "1",
		Services: []workspace.Service{{Name: "app", Path: filepath.Join(c.Dir, "project"), Language: c.Exp.Language}},
	}
	stats, err := indexer.Run(context.Background(), indexer.Options{
		Config: cfg, DBDir: dbDir, PatternsDir: filepath.Join(repoRoot(), "patterns"),
		Workers: 2, Full: true, Log: io.Discard,
	})
	require.NoError(t, err, "cell %s: indexer.Run", c.Name)
	assert.Zero(t, stats.ErrorFiles, "cell %s: no file may error", c.Name)
	assert.Positive(t, stats.Nodes, "cell %s: empty graph — fixture parses nothing", c.Name)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	return store
}

func matchMeta(node graph.Node, want map[string]string) bool {
	for k, v := range want {
		if node.Meta[k] != v {
			return false
		}
	}
	return true
}

func matchNodes(nodes []graph.Node, p nodePredicate) []graph.Node {
	var out []graph.Node
	for _, n := range nodes {
		if string(n.Type) != p.Type {
			continue
		}
		if p.Label != "" && n.Label != p.Label {
			continue
		}
		if p.LabelContains != "" && !strings.Contains(n.Label, p.LabelContains) {
			continue
		}
		if !matchMeta(n, p.Meta) {
			continue
		}
		out = append(out, n)
	}
	return out
}

func matchEdges(edges []graph.Edge, byID map[string]graph.Node, p edgePredicate) []graph.Edge {
	var out []graph.Edge
	for _, e := range edges {
		if string(e.Type) != p.Type {
			continue
		}
		from, to := byID[e.From], byID[e.To]
		if p.FromLabel != "" && from.Label != p.FromLabel {
			continue
		}
		if p.FromLabelContains != "" && !strings.Contains(from.Label, p.FromLabelContains) {
			continue
		}
		if p.ToLabel != "" && to.Label != p.ToLabel {
			continue
		}
		if p.ToLabelContains != "" && !strings.Contains(to.Label, p.ToLabelContains) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// dumpGraph renders the node/edge inventory for failure messages so a broken
// expectation is diagnosable from the test log alone.
func dumpGraph(nodes []graph.Node, edges []graph.Edge, byID map[string]graph.Node) string {
	var b strings.Builder
	b.WriteString("graph inventory:\n")
	for _, n := range nodes {
		fmt.Fprintf(&b, "  node %-16s %q meta=%v\n", n.Type, n.Label, n.Meta)
	}
	for _, e := range edges {
		fmt.Fprintf(&b, "  edge %-16s %q -> %q\n", e.Type, byID[e.From].Label, byID[e.To].Label)
	}
	return b.String()
}

// loadGraph reads all nodes and edges from the store, sorted by ID.
func loadGraph(t *testing.T, store *graph.SQLiteStore) ([]graph.Node, []graph.Edge, map[string]graph.Node) {
	t.Helper()
	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)
	byID := make(map[string]graph.Node, len(idx.Nodes))
	nodes := make([]graph.Node, 0, len(idx.Nodes))
	for id, n := range idx.Nodes {
		byID[id] = *n
		nodes = append(nodes, *n)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
	edges := idx.AllEdges() // already sorted by ID (AllEdges sorts before return)
	return nodes, edges, byID
}

func readProfiles(t *testing.T, store *graph.SQLiteStore) map[string]map[string]toolchain.ProfileStamp {
	t.Helper()
	profJSON, err := store.GetMeta(context.Background(), "toolchain_profiles")
	require.NoError(t, err, "toolchain_profiles meta must be stamped")
	var profiles map[string]map[string]toolchain.ProfileStamp
	require.NoError(t, json.Unmarshal([]byte(profJSON), &profiles))
	return profiles
}

func readNotes(t *testing.T, store *graph.SQLiteStore) []toolchain.CoverageNote {
	t.Helper()
	covJSON, err := store.GetMeta(context.Background(), "toolchain_coverage")
	require.NoError(t, err)
	var notes []toolchain.CoverageNote
	require.NoError(t, json.Unmarshal([]byte(covJSON), &notes))
	return notes
}

// TestMatrixCells runs every cell through the full pipeline and asserts the
// profile stamp, the resolved version, and the expected graph.
func TestMatrixCells(t *testing.T) {
	require.Empty(t, buildErr, "templ sidecar must build: %v", buildErr)
	for _, c := range loadCells(t) {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			store := indexCell(t, c, t.TempDir())
			defer store.Close()

			// Version-selection stamps: the manifest must genuinely resolve the
			// version in the cell's name, and the registry must pick the pinned
			// profile with the pinned inference flag.
			profiles := readProfiles(t, store)
			stamp, ok := profiles["app"][string(c.Tool)]
			require.True(t, ok, "cell %s: no profile stamp for tool %s (stamps: %v)", c.Name, c.Tool, profiles["app"])
			assert.Equal(t, c.Version, stamp.Version, "cell %s: fixture manifest does not pin the cell's version", c.Name)
			assert.Equal(t, c.Exp.Profile, stamp.Profile, "cell %s: wrong profile selected", c.Name)
			assert.Equal(t, c.Exp.Inferred, stamp.Inferred, "cell %s: inference flag mismatch", c.Name)

			// Fail-safe cells must ledger a coverage note; exact-match cells
			// must not (and no templ cell may fall back in-process — the
			// sidecar was built, so a fallback note means dispatch broke).
			notes := readNotes(t, store)
			var toolNotes []toolchain.CoverageNote
			for _, n := range notes {
				require.NotEqual(t, "in-process", n.UsedProfile,
					"cell %s: sidecar fallback occurred: %+v", c.Name, n)
				if n.Tool == c.Tool {
					toolNotes = append(toolNotes, n)
				}
			}
			if c.Exp.Inferred {
				require.NotEmpty(t, toolNotes, "cell %s: inferred selection must ledger a coverage note", c.Name)
				assert.Equal(t, c.Version, toolNotes[0].RequestedVersion)
				assert.Equal(t, c.Exp.Profile, toolNotes[0].UsedProfile)
			} else {
				assert.Empty(t, toolNotes, "cell %s: exact selection must not ledger a note", c.Name)
			}

			// Expected graph.
			nodes, edges, byID := loadGraph(t, store)
			for _, p := range c.Exp.MustNodes {
				got := matchNodes(nodes, p)
				if p.Count > 0 {
					assert.Len(t, got, p.Count, "cell %s: must_nodes %+v\n%s", c.Name, p, dumpGraph(nodes, edges, byID))
				} else {
					assert.NotEmpty(t, got, "cell %s: must_nodes %+v\n%s", c.Name, p, dumpGraph(nodes, edges, byID))
				}
			}
			for _, p := range c.Exp.MustNotNodes {
				assert.Empty(t, matchNodes(nodes, p),
					"cell %s: must_not_nodes matched %+v\n%s", c.Name, p, dumpGraph(nodes, edges, byID))
			}
			for _, p := range c.Exp.MustEdges {
				got := matchEdges(edges, byID, p)
				if p.Count > 0 {
					assert.Len(t, got, p.Count, "cell %s: must_edges %+v\n%s", c.Name, p, dumpGraph(nodes, edges, byID))
				} else {
					assert.NotEmpty(t, got, "cell %s: must_edges %+v\n%s", c.Name, p, dumpGraph(nodes, edges, byID))
				}
			}
			for _, p := range c.Exp.MustNotEdges {
				assert.Empty(t, matchEdges(edges, byID, p),
					"cell %s: must_not_edges matched %+v\n%s", c.Name, p, dumpGraph(nodes, edges, byID))
			}
		})
	}
}

// TestMatrixCoverage is the bidirectional CI gate. All violations are
// accumulated and reported together — exit order cannot hide a failure.
func TestMatrixCoverage(t *testing.T) {
	cells := loadCells(t)
	reg := toolchain.DefaultRegistry()
	var violations []string

	// Direction 2 first only in iteration order — both directions always run.
	// A cell must land on a registry row without inference, unless it declares
	// itself a fail-safe cell (inferred: true in expected.json).
	type rowKey struct {
		Tool toolchain.Tool
		Row  int
	}
	covered := map[rowKey]bool{}
	for _, c := range cells {
		rows := reg[c.Tool]
		if len(rows) == 0 {
			violations = append(violations, fmt.Sprintf(
				"orphaned cell %s: tool %q is not in the registry", c.Name, c.Tool))
			continue
		}
		sel := reg.Select(c.Tool, c.Version)
		if sel.Inferred && !c.Exp.Inferred {
			violations = append(violations, fmt.Sprintf(
				"orphaned cell %s: version %q satisfies no registry row and the cell does not declare inferred:true", c.Name, c.Version))
			continue
		}
		if !sel.Inferred && c.Exp.Inferred {
			violations = append(violations, fmt.Sprintf(
				"cell %s declares inferred:true but version %q matches a registry row exactly", c.Name, c.Version))
			continue
		}
		if !sel.Inferred {
			for i, row := range rows {
				if row == sel.Backend {
					covered[rowKey{c.Tool, i}] = true
					break
				}
			}
		}
	}

	// Direction 1: every registered row needs at least one exact-match cell.
	tools := make([]string, 0, len(reg))
	for tool := range reg {
		tools = append(tools, string(tool))
	}
	sort.Strings(tools)
	for _, toolName := range tools {
		tool := toolchain.Tool(toolName)
		for i, row := range reg[tool] {
			if covered[rowKey{tool, i}] {
				continue
			}
			variant := row.RuleVariant
			if variant == "" {
				variant = row.SidecarBackend
			}
			violations = append(violations, fmt.Sprintf(
				"registered version without a fixture: tool=%s range=%q variant=%s — add testdata/matrix/%s@<version>/",
				tool, row.VersionRange, variant, tool))
		}
	}

	for _, v := range violations {
		t.Error(v)
	}
}

// TestMatrixTwoRunDeterminism (rule 2): the same cell indexed twice must
// store a byte-identical graph.
func TestMatrixTwoRunDeterminism(t *testing.T) {
	require.Empty(t, buildErr, "templ sidecar must build: %v", buildErr)
	var target *cell
	for _, c := range loadCells(t) {
		if c.Name == "datastar@1.1.0" {
			c := c
			target = &c
			break
		}
	}
	require.NotNil(t, target, "determinism cell datastar@1.1.0 missing")

	dump := func() []byte {
		store := indexCell(t, *target, t.TempDir())
		defer store.Close()
		nodes, edges, _ := loadGraph(t, store)
		out, err := json.Marshal(struct {
			Nodes []graph.Node `json:"nodes"`
			Edges []graph.Edge `json:"edges"`
		}{nodes, edges})
		require.NoError(t, err)
		return out
	}
	first, second := dump(), dump()
	require.Equal(t, string(first), string(second), "two runs must store byte-identical graphs")
}
