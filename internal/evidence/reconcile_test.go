package evidence_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/evidence"
	"github.com/lordsonvimal/polyflow/internal/graph"
	"github.com/lordsonvimal/polyflow/internal/indexer"
	"github.com/lordsonvimal/polyflow/internal/meta"
	"github.com/lordsonvimal/polyflow/internal/workspace"
)

// chessleapPath returns the resolved path to the local chessleap clone used
// by the eval harness, or "" when it is not present.
func chessleapPath() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	// internal/evidence/reconcile_test.go → repo root is 2 levels up
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	p := filepath.Join(root, "eval", ".cache", "chessleap")
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// goldenContractEdgesPath returns the path to the committed contract-golden snapshot.
func goldenContractEdgesPath() string {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	root := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return filepath.Join(root, "internal", "contract", "testdata", "golden", "chessleap_contract_edges.json")
}

// goldenEdge is the stable projection used in the contract golden file.
type goldenEdge struct {
	ID         string `json:"id"`
	From       string `json:"from"`
	To         string `json:"to"`
	Type       string `json:"type"`
	Label      string `json:"label,omitempty"`
	Confidence string `json:"confidence"`
}

// --- provider name validation ---

func TestValidateProviderName_Valid(t *testing.T) {
	for _, name := range []string{"static", "contract", "runtime", "config", "llm"} {
		assert.NoError(t, evidence.ValidateProviderName(name), "name %q should be valid", name)
	}
}

func TestValidateProviderName_Invalid(t *testing.T) {
	for _, name := range []string{"", "unknown", "trace", "Static"} {
		assert.Error(t, evidence.ValidateProviderName(name), "name %q should be rejected", name)
	}
}

func TestNewReconciler_RejectsUnknownProvider(t *testing.T) {
	bad := &badProvider{name: "bogus"}
	_, err := evidence.NewReconciler(bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}

type badProvider struct{ name string }

func (b *badProvider) Name() string { return b.name }
func (b *badProvider) Collect(_ context.Context, _ *workspace.WorkspaceConfig) (evidence.Evidence, error) {
	return evidence.Evidence{}, nil
}

// --- StaticProvider stamps all edges ---

func TestStaticProviderStampsAllEdges(t *testing.T) {
	nodes := []graph.Node{
		{ID: "n1", Type: graph.NodeTypeHTTPHandler, Label: "h1", File: "main.go", Line: 10},
		{ID: "n2", Type: graph.NodeTypeHTTPClient, Label: "c1", File: "client.go", Line: 5},
	}
	edges := []graph.Edge{
		{ID: "e1", From: "n1", To: "n2", Type: graph.EdgeTypeHTTPCall, Label: "GET /api", Confidence: graph.ConfidenceStatic},
		{ID: "e2", From: "n2", To: "n1", Type: graph.EdgeTypeHTTPCall, Label: "POST /api", Confidence: graph.ConfidenceInferred},
	}

	sp := evidence.NewStaticProvider(nodes, edges, nil)
	ev, err := sp.Collect(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, ev.Edges, 2)

	for _, e := range ev.Edges {
		require.NotEmpty(t, e.Sources, "edge %s must have Sources populated", e.ID)
		assert.Equal(t, "static", e.Sources[0].Provider)
		assert.NotEmpty(t, e.Sources[0].Confidence)
		// VerificationState is unset by the provider; Reconciler sets it.
		assert.Empty(t, e.VerificationState)
	}

	// Provenance ref should contain the From node's file.
	assert.Contains(t, ev.Edges[0].Sources[0].Ref, "main.go")
	assert.Contains(t, ev.Edges[1].Sources[0].Ref, "client.go")
}

func TestStaticProvider_ReplacesSourcesOnRestamp(t *testing.T) {
	// If an edge already carries stale Sources from a previous run, the static
	// provider must REPLACE them (total recomputation), not append.
	nodes := []graph.Node{{ID: "n1", File: "a.go", Line: 1}}
	edges := []graph.Edge{
		{
			ID: "e1", From: "n1", To: "n1",
			Type: graph.EdgeTypeCalls,
			Sources: []graph.SourceRef{
				{Provider: "static", Confidence: "old"},
				{Provider: "runtime", Confidence: "observed"},
			},
		},
	}
	sp := evidence.NewStaticProvider(nodes, edges, nil)
	ev, err := sp.Collect(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, ev.Edges[0].Sources, 1, "total recomputation: only the new static source")
	assert.Equal(t, "static", ev.Edges[0].Sources[0].Provider)
}

// --- Multi-valued channel join (bug-class rule 1) ---

// TestReconcilerMultiChannelFanout asserts that N static edges sharing one
// channel key all receive the confirming source — never only the first found.
func TestReconcilerMultiChannelFanout(t *testing.T) {
	// Two different call sites (e1, e2) hitting the same logical channel.
	nodes := []graph.Node{
		{ID: "site-a", File: "a.go", Line: 1},
		{ID: "site-b", File: "b.go", Line: 2},
		{ID: "handler", File: "h.go", Line: 3},
	}
	sharedLabel := "GET /api/users"
	edges := []graph.Edge{
		{ID: "call-a", From: "site-a", To: "handler", Type: graph.EdgeTypeHTTPCall,
			Label: sharedLabel, Confidence: graph.ConfidenceStatic},
		{ID: "call-b", From: "site-b", To: "handler", Type: graph.EdgeTypeHTTPCall,
			Label: sharedLabel, Confidence: graph.ConfidenceStatic},
	}

	// A confirming "contract" edge for the same channel.
	contractEdge := graph.Edge{
		ID: "contract-1", From: "svc-a", To: "svc-b",
		Type:  graph.EdgeTypeHTTPCall,
		Label: sharedLabel,
		Sources: []graph.SourceRef{
			{Provider: "contract", Confidence: graph.ConfidenceDeclared, Ref: "openapi.yaml#getUsers"},
		},
	}
	contractEv := evidence.Evidence{Edges: []graph.Edge{contractEdge}}
	contractProv := &fakeProvider{name: "contract", ev: contractEv}

	sp := evidence.NewStaticProvider(nodes, edges, nil)
	rec, err := evidence.NewReconciler(sp, contractProv)
	require.NoError(t, err)

	result, err := rec.Reconcile(context.Background(), nil)
	require.NoError(t, err)

	edgeByID := make(map[string]graph.Edge)
	for _, e := range result.Edges {
		edgeByID[e.ID] = e
	}

	// Both call sites must receive the contract source (fan-out, not first-match).
	for _, id := range []string{"call-a", "call-b"} {
		e, ok := edgeByID[id]
		require.True(t, ok, "edge %s must be in result", id)
		require.Len(t, e.Sources, 2, "edge %s must have both static and contract sources", id)
		assert.Equal(t, graph.StateVerified, e.VerificationState,
			"edge %s must be verified when static + contract agree", id)
	}
}

// fakeProvider is a test double that always returns a fixed Evidence.
type fakeProvider struct {
	name string
	ev   evidence.Evidence
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Collect(_ context.Context, _ *workspace.WorkspaceConfig) (evidence.Evidence, error) {
	return f.ev, nil
}

// --- Determinism (bug-class rule 2) ---

// TestReconcilerDeterminism runs the reconciler twice on the same input and
// requires byte-identical JSON output.
func TestReconcilerDeterminism(t *testing.T) {
	nodes := []graph.Node{
		{ID: "n1", File: "a.go", Line: 1},
		{ID: "n2", File: "b.go", Line: 2},
		{ID: "n3", File: "c.go", Line: 3},
	}
	edges := []graph.Edge{
		{ID: "e1", From: "n1", To: "n2", Type: graph.EdgeTypeCalls, Label: "foo", Confidence: graph.ConfidenceStatic},
		{ID: "e2", From: "n2", To: "n3", Type: graph.EdgeTypeCalls, Label: "bar", Confidence: graph.ConfidenceInferred},
		{ID: "e3", From: "n3", To: "n1", Type: graph.EdgeTypeCalls, Label: "baz", Confidence: graph.ConfidencePartial},
	}

	reconcile := func() []byte {
		sp := evidence.NewStaticProvider(nodes, edges, nil)
		rec, err := evidence.NewReconciler(sp)
		require.NoError(t, err)
		result, err := rec.Reconcile(context.Background(), nil)
		require.NoError(t, err)
		b, err := json.Marshal(result.Edges)
		require.NoError(t, err)
		return b
	}

	first := reconcile()
	second := reconcile()
	assert.Equal(t, first, second, "reconciler output must be byte-identical across runs")
}

// --- Verification state transitions ---

func TestReconcilerVerificationStates(t *testing.T) {
	nodes := []graph.Node{{ID: "a", File: "a.go", Line: 1}, {ID: "b", File: "b.go", Line: 2}}
	staticEdge := graph.Edge{
		ID: "s1", From: "a", To: "b",
		Type: graph.EdgeTypeHTTPCall, Label: "GET /x",
		Confidence: graph.ConfidenceStatic,
	}
	sp := evidence.NewStaticProvider(nodes, []graph.Edge{staticEdge}, nil)

	// Without any confirming provider: candidate.
	rec, _ := evidence.NewReconciler(sp)
	result, _ := rec.Reconcile(context.Background(), nil)
	require.Len(t, result.Edges, 1)
	assert.Equal(t, graph.StateCandidate, result.Edges[0].VerificationState)

	// With a runtime confirming provider: verified.
	runtimeEv := evidence.Evidence{
		Edges: []graph.Edge{{
			ID: "r1", From: "a", To: "b",
			Type:  graph.EdgeTypeHTTPCall,
			Label: "GET /x",
			Sources: []graph.SourceRef{
				{Provider: "runtime", Confidence: graph.ConfidenceObserved, Ref: "sess1/span1"},
			},
		}},
	}
	runtimeProv := &fakeProvider{name: "runtime", ev: runtimeEv}
	sp2 := evidence.NewStaticProvider(nodes, []graph.Edge{staticEdge}, nil)
	rec2, _ := evidence.NewReconciler(sp2, runtimeProv)
	result2, _ := rec2.Reconcile(context.Background(), nil)
	require.Len(t, result2.Edges, 1)
	assert.Equal(t, graph.StateVerified, result2.Edges[0].VerificationState)
}

// TestReconcilerObservedOnlyGap asserts that a non-static edge with no
// matching static edge surfaces as observed_only_gap with a synthetic edge.
func TestReconcilerObservedOnlyGap(t *testing.T) {
	nodes := []graph.Node{{ID: "n1", File: "a.go"}}
	staticEdge := graph.Edge{
		ID: "s1", From: "n1", To: "n1",
		Type: graph.EdgeTypeCalls, Label: "known",
	}
	sp := evidence.NewStaticProvider(nodes, []graph.Edge{staticEdge}, nil)

	// A runtime edge on a channel static never saw.
	runtimeEv := evidence.Evidence{
		Edges: []graph.Edge{{
			ID: "r-gap", From: "svc-x", To: "svc-y",
			Type:  graph.EdgeTypeHTTPCall,
			Label: "GET /unknown-path",
			Sources: []graph.SourceRef{
				{Provider: "runtime", Confidence: graph.ConfidenceObserved},
			},
		}},
	}
	runtimeProv := &fakeProvider{name: "runtime", ev: runtimeEv}
	rec, _ := evidence.NewReconciler(sp, runtimeProv)
	result, _ := rec.Reconcile(context.Background(), nil)

	var gapEdges []graph.Edge
	for _, e := range result.Edges {
		if e.VerificationState == graph.StateObservedOnlyGap {
			gapEdges = append(gapEdges, e)
		}
	}
	require.Len(t, gapEdges, 1, "gap edge must surface")
	assert.Equal(t, graph.StateObservedOnlyGap, gapEdges[0].VerificationState)
	// Synthetic ID must be deterministic, not counter-based.
	assert.Contains(t, gapEdges[0].ID, "gap:")
}

// --- Chessleap static-baseline-unchanged guard ---

// TestChessleapF0StaticBaseline verifies that wrapping the static pipeline
// in the evidence reconciler leaves the edge core (ID/From/To/Type/Label/
// Confidence) unchanged against the committed golden snapshot, and that every
// edge now carries Sources[].
//
// This is the "static-baseline-unchanged guard" required by the F.0 spec.
func TestChessleapF0StaticBaseline(t *testing.T) {
	chessleap := chessleapPath()
	if _, err := os.Stat(chessleap); os.IsNotExist(err) {
		t.Skip("chessleap eval repo not available; clone to eval/.cache/chessleap")
	}
	goldenPath := goldenContractEdgesPath()
	if _, err := os.Stat(goldenPath); os.IsNotExist(err) {
		t.Skip("golden snapshot not found; generate it first with the contract golden test")
	}

	// Load the committed golden snapshot (contract edges before F.0).
	goldenData, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	var golden []goldenEdge
	require.NoError(t, json.Unmarshal(goldenData, &golden))
	require.NotEmpty(t, golden)

	// Index chessleap (F.0 reconciler now runs inside indexer.Run).
	cfg, err := workspace.Load(filepath.Join(chessleap, "workspace.yaml"))
	require.NoError(t, err)
	for i := range cfg.Services {
		cfg.Services[i].Path = filepath.Join(chessleap, cfg.Services[i].Path)
	}
	dbDir := t.TempDir()
	_, thisFile, _, _ := runtime.Caller(0)
	patternsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "patterns")
	stats, err := indexer.Run(context.Background(), indexer.Options{
		Config:      cfg,
		DBDir:       dbDir,
		PatternsDir: patternsDir,
		Full:        true,
	})
	require.NoError(t, err)
	t.Logf("chessleap F.0 index: files=%d nodes=%d edges=%d", stats.TotalFiles, stats.Nodes, stats.Edges)

	store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	idx, err := store.BuildIndex(context.Background())
	require.NoError(t, err)

	// Every edge in the store must have Sources populated (F.0 invariant).
	edgesWithoutSources := 0
	for _, out := range idx.OutEdges {
		for _, e := range out {
			if len(e.Sources) == 0 {
				edgesWithoutSources++
				t.Errorf("edge %s has no Sources (provider=%s, label=%q)", e.ID, "", e.Label)
			}
		}
	}
	assert.Equal(t, 0, edgesWithoutSources, "every edge must have Sources after F.0 reconciliation")

	// Contract-typed edges must match the golden snapshot's core fields.
	goldenByID := make(map[string]goldenEdge, len(golden))
	for _, ge := range golden {
		goldenByID[ge.ID] = ge
	}

	// Collect all edges from the index (deduped by ID).
	seen := make(map[string]bool)
	type coreEdge struct {
		ID, From, To, Type, Label, Confidence string
	}
	actual := make(map[string]coreEdge)
	for _, out := range idx.OutEdges {
		for _, e := range out {
			if seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			actual[e.ID] = coreEdge{e.ID, e.From, e.To, string(e.Type), e.Label, e.Confidence}
		}
	}

	for _, ge := range golden {
		ae, ok := actual[ge.ID]
		if !ok {
			t.Errorf("golden edge %s missing from F.0 output", ge.ID)
			continue
		}
		assert.Equal(t, ge.From, ae.From, "edge %s From changed", ge.ID)
		assert.Equal(t, ge.To, ae.To, "edge %s To changed", ge.ID)
		assert.Equal(t, ge.Type, ae.Type, "edge %s Type changed", ge.ID)
		assert.Equal(t, ge.Label, ae.Label, "edge %s Label changed", ge.ID)
		assert.Equal(t, ge.Confidence, ae.Confidence, "edge %s Confidence changed", ge.ID)
	}
}

// TestChessleapF0Determinism indexes chessleap twice and asserts the
// reconciled edge set (including Sources[]/VerificationState) is byte-identical.
func TestChessleapF0Determinism(t *testing.T) {
	if testing.Short() {
		t.Skip("two full index runs; skipped in -short mode")
	}
	chessleap := chessleapPath()
	if _, err := os.Stat(chessleap); os.IsNotExist(err) {
		t.Skip("chessleap eval repo not available; clone to eval/.cache/chessleap")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	patternsDir := filepath.Join(filepath.Dir(thisFile), "..", "..", "patterns")

	indexAndExport := func() []byte {
		cfg, err := workspace.Load(filepath.Join(chessleap, "workspace.yaml"))
		require.NoError(t, err)
		for i := range cfg.Services {
			cfg.Services[i].Path = filepath.Join(chessleap, cfg.Services[i].Path)
		}
		dbDir := t.TempDir()
		_, err = indexer.Run(context.Background(), indexer.Options{
			Config: cfg, DBDir: dbDir, PatternsDir: patternsDir, Full: true,
		})
		require.NoError(t, err)

		store, err := graph.NewSQLiteStore(filepath.Join(dbDir, meta.DBFile))
		require.NoError(t, err)
		defer store.Close()
		idx, err := store.BuildIndex(context.Background())
		require.NoError(t, err)

		// Collect all edges, sorted by ID.
		type exportEdge struct {
			ID                  string             `json:"id"`
			From                string             `json:"from"`
			To                  string             `json:"to"`
			Type                string             `json:"type"`
			Label               string             `json:"label,omitempty"`
			Confidence          string             `json:"confidence"`
			Sources             []graph.SourceRef  `json:"sources"`
			VerificationState   string             `json:"verification_state"`
			VerifiedGranularity string             `json:"verified_granularity,omitempty"`
		}
		var edges []exportEdge
		seen := make(map[string]bool)
		for _, out := range idx.OutEdges {
			for _, e := range out {
				if seen[e.ID] {
					continue
				}
				seen[e.ID] = true
				edges = append(edges, exportEdge{
					e.ID, e.From, e.To, string(e.Type), e.Label,
					e.Confidence, e.Sources, e.VerificationState, e.VerifiedGranularity,
				})
			}
		}
		sort.Slice(edges, func(i, j int) bool { return edges[i].ID < edges[j].ID })
		b, err := json.Marshal(edges)
		require.NoError(t, err)
		return b
	}

	// Pre-warm the Go build cache so both comparison runs start from identical
	// toolchain state.  Without this, run 1 (cold) and run 2 (warm) differ:
	// packages.Load(Tests:true) compiles test variants; on a cold cache some
	// test variants fail to build and collapseTestVariants falls back to the
	// plain variant, producing a different SSA than the warm run. We warm both
	// production and test-variant caches before the two comparison runs.
	for _, args := range [][]string{
		{"build", "./..."},
		{"test", "-run", "^$", "-count=1", "./..."},
	} {
		cmd := exec.CommandContext(t.Context(), "go", args...)
		cmd.Dir = chessleap
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("pre-warm %v warning (non-fatal): %v\n%s", args, err, out)
		}
	}

	first := indexAndExport()
	second := indexAndExport()
	assert.Equal(t, first, second, "F.0 reconciled edge set must be byte-identical across runs")
}
