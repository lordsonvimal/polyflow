package eval

// Internal tests for the D.1 precision half of the corpus: must_not_include
// (false positives are a hard failure) and exhaustive (the only condition under
// which a precision ratio means anything).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

// makeImpactTestDB builds a two-node graph rooted at dir: caller.go calls
// target.go. A backward impact query from the target therefore returns both.
func makeImpactTestDB(t *testing.T, dir string) (*graph.SQLiteStore, *graph.AdjacencyIndex) {
	t.Helper()
	store, err := graph.NewSQLiteStore(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	// The files have to exist: pathCanon resolves symlinks, and on macOS a
	// TempDir under /var only becomes comparable with the repo root's
	// /private/var spelling once EvalSymlinks can stat it.
	for _, name := range []string{"target.go", "caller.go"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("package p\n"), 0o644))
	}

	ctx := context.Background()
	target := &graph.Node{ID: "svc:target", Label: "Target", Type: graph.NodeTypeFunction, Service: "svc", File: filepath.Join(dir, "target.go"), Line: 1}
	caller := &graph.Node{ID: "svc:caller", Label: "Caller", Type: graph.NodeTypeFunction, Service: "svc", File: filepath.Join(dir, "caller.go"), Line: 1}
	require.NoError(t, store.UpsertNode(ctx, target))
	require.NoError(t, store.UpsertNode(ctx, caller))
	require.NoError(t, store.UpsertEdge(ctx, &graph.Edge{ID: "e1", From: caller.ID, To: target.ID, Type: graph.EdgeTypeCalls, Confidence: "static"}))

	idx, err := store.BuildIndex(ctx)
	require.NoError(t, err)
	return store, idx
}

// TestRunCase_MustNotIncludeCanonicalisesPaths is the end-to-end proof that the
// assertion can actually fire. A forbidden path is written absolute in the
// manifest while impact reports the repo-relative spelling (or the reverse,
// through the eval/.cache symlink); an assertion that silently never matches is
// worse than no assertion at all, because it reads green.
func TestRunCase_MustNotIncludeCanonicalisesPaths(t *testing.T) {
	dir := t.TempDir()
	store, idx := makeImpactTestDB(t, dir)
	pc := newPathCanon(dir)

	c := Case{
		ID:               "phantom",
		Kind:             "node",
		Target:           "Target",
		ExpectedImpacted: []string{"target.go"},
		MustNotMiss:      []string{"target.go"},
		// Absolute, as the plan's own example writes it — and as a hand audit
		// naturally produces, since that is what impact printed.
		MustNotInclude: []string{filepath.Join(dir, "caller.go")},
	}

	cr, err := runCase(context.Background(), store, idx, nil, c, pc)
	require.NoError(t, err)
	assert.Equal(t, []string{"caller.go"}, cr.ForbiddenHits, "the forbidden file must be recognised across path spellings")
	assert.True(t, cr.HardFail, "returning a hand-verified false positive is a failure even at recall 1.0")
	assert.InDelta(t, 1.0, cr.Recall, 1e-9, "recall is unaffected — this case is too broad, not too narrow")
	assert.Nil(t, cr.Precision, "case did not declare exhaustive")
}

// TestRunCase_ExhaustivePrecision — with a complete truth set the ratio is
// reported, and it is the ratio that exposes the extra file.
func TestRunCase_ExhaustivePrecision(t *testing.T) {
	dir := t.TempDir()
	store, idx := makeImpactTestDB(t, dir)
	pc := newPathCanon(dir)

	c := Case{
		ID:               "exhaustive",
		Kind:             "node",
		Target:           "Target",
		ExpectedImpacted: []string{"target.go"},
		MustNotMiss:      []string{"target.go"},
		Exhaustive:       true,
	}

	cr, err := runCase(context.Background(), store, idx, nil, c, pc)
	require.NoError(t, err)
	require.NotNil(t, cr.Precision)
	assert.InDelta(t, 0.5, *cr.Precision, 1e-9, "2 files returned, 1 of them expected")
	assert.True(t, cr.Exhaustive)
	assert.False(t, cr.HardFail, "a precision shortfall is measured, not fatal — only must_not_include hard-fails")
}

// TestApplyPrecision_NonExhaustiveLeavesPrecisionUnset pins the D.1 rule that
// the artifact the header section warns about can no longer be produced: a
// sampled truth set yields no number at all, not a small one.
func TestApplyPrecision_NonExhaustiveLeavesPrecisionUnset(t *testing.T) {
	cr := ApplyPrecision(CaseResult{CaseID: "x", Recall: 1}, []string{"a.go", "b.go"}, []string{"a.go"}, nil, false)
	assert.Nil(t, cr.Precision)
	assert.False(t, cr.Exhaustive)
	assert.Empty(t, cr.ForbiddenHits)
	assert.False(t, cr.HardFail)
}

func TestPrecision(t *testing.T) {
	assert.InDelta(t, 0.5, Precision([]string{"a.go", "b.go"}, []string{"a.go", "z.go"}), 1e-9)
	assert.InDelta(t, 0.0, Precision(nil, []string{"a.go"}), 1e-9, "an empty answer has no precision to speak of")
	assert.InDelta(t, 1.0, Precision([]string{"a.go", "a.go"}, []string{"a.go"}), 1e-9, "duplicates are one returned file")
}

// TestAggregateReport_PrecisionOverExhaustiveOnly — the repo average must not be
// diluted by cases that never claimed a complete truth set. Averaging every
// case is what made the old column a measure of corpus authoring rather than of
// the tool.
func TestAggregateReport_PrecisionOverExhaustiveOnly(t *testing.T) {
	half, one := 0.5, 1.0
	r := AggregateReport("repo", []CaseResult{
		{CaseID: "sampled", Recall: 1},
		{CaseID: "exh-a", Recall: 1, Exhaustive: true, Precision: &half},
		{CaseID: "exh-b", Recall: 1, Exhaustive: true, Precision: &one},
		{CaseID: "phantom", Recall: 1, HardFail: true, ForbiddenHits: []string{"x.rb", "y.rb"}},
	})
	require.NotNil(t, r.Precision)
	assert.InDelta(t, 0.75, *r.Precision, 1e-9)
	assert.Equal(t, 2, r.ExhaustiveCases)
	assert.Equal(t, 2, r.ForbiddenHits)
}

func TestAggregateReport_NoExhaustiveCasesHasNoPrecision(t *testing.T) {
	r := AggregateReport("repo", []CaseResult{{CaseID: "a", Recall: 1}})
	assert.Nil(t, r.Precision, "absent, not zero — a zero would read as total imprecision")
	assert.Equal(t, 0, r.ExhaustiveCases)
}

// TestCheckGate_ForbiddenHit covers the three shapes that matter: a fresh
// phantom fails, a recorded one does not, and a fresh one on a case that was
// ALREADY hard-failing still fails — the hole condition 1 leaves open.
func TestCheckGate_ForbiddenHit(t *testing.T) {
	baseline := &MultiReport{Reports: []Report{{Repo: "r", Recall: 1, Results: []CaseResult{
		{CaseID: "clean", Recall: 1},
		{CaseID: "known", Recall: 1, HardFail: true, ForbiddenHits: []string{"old.rb"}},
		{CaseID: "already-red", Recall: 0, HardFail: true},
	}}}}

	t.Run("fresh phantom is a regression", func(t *testing.T) {
		cur := &MultiReport{Reports: []Report{{Repo: "r", Recall: 1, Results: []CaseResult{
			{CaseID: "clean", Recall: 1, HardFail: true, ForbiddenHits: []string{"new.rb"}},
		}}}}
		g := CheckGate(cur, baseline)
		require.Len(t, g.Regressions, 1, "reported once, as forbidden_hit — not also as hard_fail")
		assert.Equal(t, "forbidden_hit", g.Regressions[0].Reason)
		assert.Equal(t, []string{"new.rb"}, g.Regressions[0].ForbiddenHits)
	})

	t.Run("recorded phantom does not block forever", func(t *testing.T) {
		cur := &MultiReport{Reports: []Report{{Repo: "r", Recall: 1, Results: []CaseResult{
			{CaseID: "known", Recall: 1, HardFail: true, ForbiddenHits: []string{"old.rb"}},
		}}}}
		assert.True(t, CheckGate(cur, baseline).OK)
	})

	t.Run("fresh phantom on an already-failing case still fires", func(t *testing.T) {
		cur := &MultiReport{Reports: []Report{{Repo: "r", Recall: 1, Results: []CaseResult{
			{CaseID: "already-red", Recall: 0, HardFail: true, ForbiddenHits: []string{"new.rb"}},
		}}}}
		g := CheckGate(cur, baseline)
		require.Len(t, g.Regressions, 1)
		assert.Equal(t, "forbidden_hit", g.Regressions[0].Reason,
			"condition 1 would skip this case as a pre-existing hard fail, hiding a brand-new false positive")
	})

	t.Run("a partly-new set reports only the new members", func(t *testing.T) {
		cur := &MultiReport{Reports: []Report{{Repo: "r", Recall: 1, Results: []CaseResult{
			{CaseID: "known", Recall: 1, HardFail: true, ForbiddenHits: []string{"old.rb", "new.rb"}},
		}}}}
		g := CheckGate(cur, baseline)
		require.Len(t, g.Regressions, 1)
		assert.Equal(t, []string{"new.rb"}, g.Regressions[0].ForbiddenHits)
	})
}

// TestAuthoredCorpusIsValid runs the schema and lint rules against the real
// manifests. ValidateManifest had no caller outside its own unit tests, so
// every rule it enforces — including the D.1 ones added here — was enforced
// only against fixtures. A rule nothing checks is a comment.
//
// Skips when the corpus is absent: eval/corpus is gitignored and only some
// manifests are force-tracked, so a clean checkout legitimately has fewer.
func TestAuthoredCorpusIsValid(t *testing.T) {
	dirs, err := FindCorpusDirs("../../eval/corpus")
	if err != nil {
		t.Skip("no local corpus:", err)
	}
	for _, dir := range dirs {
		m, err := LoadManifest(dir)
		require.NoError(t, err, dir)
		for _, e := range ValidateManifest(m) {
			t.Errorf("%s: %s", dir, e.Error())
		}
	}
}

func TestValidateManifest_PrecisionKeys(t *testing.T) {
	base := Manifest{Repo: RepoRef{Name: "r", SHA: "abc", Workspace: "w", Path: "."}}

	valid := base
	valid.Cases = []Case{{
		ID: "ok", Kind: "node", Target: "T",
		ExpectedImpacted: []string{"a.go"}, MustNotMiss: []string{"a.go"},
		MustNotInclude: []string{"b.go"}, Exhaustive: true,
	}}
	assert.Empty(t, ValidateManifest(&valid))

	for name, c := range map[string]Case{
		"must_not_include on a semantic case": {
			ID: "x", Kind: "semantic", Query: "q", Section: "nodes",
			ExpectAnyOf: []string{"F"}, MustNotMiss: []string{"F"}, MustNotInclude: []string{"b.go"},
		},
		"exhaustive on a rank1 case": {
			ID: "x", Kind: "rank1", Query: "q", Section: "nodes", ExpectRank1: "F", Exhaustive: true,
		},
		"file both expected and forbidden": {
			ID: "x", Kind: "node", Target: "T",
			ExpectedImpacted: []string{"a.go"}, MustNotMiss: []string{"a.go"}, MustNotInclude: []string{"a.go"},
		},
		"must_not_miss entry forbidden": {
			ID: "x", Kind: "node", Target: "T",
			ExpectedImpacted: []string{"a.go", "b.go"}, MustNotMiss: []string{"b.go"}, MustNotInclude: []string{"b.go"},
		},
	} {
		m := base
		m.Cases = []Case{c}
		assert.NotEmpty(t, ValidateManifest(&m), name+" must be rejected")
	}
}
