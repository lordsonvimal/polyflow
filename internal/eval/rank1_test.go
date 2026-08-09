package eval

// Internal tests for the rank-1 identity case runner (C.6).
// Uses package eval (not eval_test) to reach the unexported runner and the
// shared semantic test DB helper.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/semantic"
)

// TestRank1Case_ExpectedWins — the expected entity comes back first, so the
// case passes and both ranks are recorded for the margin.
func TestRank1Case_ExpectedWins(t *testing.T) {
	store := makeSemanticTestDB(t, []semantic.Entity{
		entityFTS("svc:file.go:function:ApplyMoveChecked:1", "ApplyMoveChecked", "engine/move.go"),
		entityFTS("svc:other.go:function:Unrelated:9", "Unrelated", "other.go"),
	})

	cr, err := runRank1Case(context.Background(), store, Case{
		ID:          "wins",
		Kind:        "rank1",
		Query:       "ApplyMoveChecked",
		Section:     "nodes",
		ExpectRank1: "ApplyMoveChecked",
	})
	require.NoError(t, err)
	assert.Equal(t, "rank1", cr.Kind)
	assert.Equal(t, "ApplyMoveChecked", cr.Rank1)
	assert.InDelta(t, 1.0, cr.Recall, 1e-9)
	assert.False(t, cr.HardFail)
}

// TestRank1Case_PresentButNotFirstIsAHardFail is the whole point of the kind:
// a semantic case scores this same result as a pass because the entity is in
// the top 10. Only rank-1 identity sees the demotion.
func TestRank1Case_PresentButNotFirstIsAHardFail(t *testing.T) {
	store := makeSemanticTestDB(t, []semantic.Entity{
		// "handler" appears twice in the decoy's text and once in the target's,
		// so FTS ranks the decoy first for a query naming both.
		entityFTS("svc:error/handler.go:function:Handler:1", "Handler handler", "error/handler.go"),
		entityFTS("svc:api/application.go:function:CreateApplication:92", "CreateApplication", "api/application.go"),
	})

	c := Case{
		ID:          "demoted",
		Kind:        "rank1",
		Query:       "CreateApplication handler",
		Section:     "nodes",
		ExpectRank1: "CreateApplication",
	}

	cr, err := runRank1Case(context.Background(), store, c)
	require.NoError(t, err)
	require.NotEqual(t, "CreateApplication", cr.Rank1, "fixture must reproduce the demotion")
	assert.True(t, cr.HardFail)
	assert.InDelta(t, 0.0, cr.Recall, 1e-9)
	assert.Equal(t, "CreateApplication", cr.Rank2, "the demoted answer is named so the failure is legible")

	// Same DB, same query, scored as a semantic case: it passes.
	sem := c
	sem.Kind = "semantic"
	sem.ExpectAnyOf = []string{"CreateApplication"}
	semResult, err := runSemanticCase(context.Background(), store, sem)
	require.NoError(t, err)
	assert.False(t, semResult.HardFail, "top-10 presence cannot detect a rank-1 loss — that is why rank1 exists")
}

// TestRank1Case_TargetFilePin — a label alone is not an identity when the label
// is shared, so the pin has to be able to fail a case the label would pass.
func TestRank1Case_TargetFilePin(t *testing.T) {
	store := makeSemanticTestDB(t, []semantic.Entity{
		entityFTS("svc:api/application.go:function:CreateApplication:92", "CreateApplication", "api/application.go"),
	})

	base := Case{
		ID:          "pin",
		Kind:        "rank1",
		Query:       "CreateApplication",
		Section:     "nodes",
		ExpectRank1: "CreateApplication",
	}

	right := base
	right.TargetFile = "api/application.go"
	cr, err := runRank1Case(context.Background(), store, right)
	require.NoError(t, err)
	assert.False(t, cr.HardFail, "pin naming the winning declaration's file must pass")

	wrong := base
	wrong.TargetFile = "database/application.go"
	cr, err = runRank1Case(context.Background(), store, wrong)
	require.NoError(t, err)
	assert.True(t, cr.HardFail, "a matching label in the wrong file is not the expected declaration")
	assert.Equal(t, "CreateApplication", cr.Rank1)
}

// TestRank1Case_NoHitsIsAHardFail — an empty result set is a failure, not a
// vacuous pass. There is no unresolved ledger here that could make it honest.
func TestRank1Case_NoHitsIsAHardFail(t *testing.T) {
	store := makeSemanticTestDB(t, []semantic.Entity{
		entityFTS("svc:file.go:function:Foo:1", "Foo", "file.go"),
	})

	cr, err := runRank1Case(context.Background(), store, Case{
		ID:          "empty",
		Kind:        "rank1",
		Query:       "zzzzznomatchzzzzz",
		Section:     "nodes",
		ExpectRank1: "Foo",
	})
	require.NoError(t, err)
	assert.True(t, cr.HardFail)
	assert.Empty(t, cr.Rank1)
	assert.InDelta(t, 0.0, cr.ScoreGap(), 1e-9, "no hits means no margin")
}

// TestScoreGap_NegativeWhenTierOrderingCarriesTheAnswer records the shape the
// fleet actually exhibits: the correct hit sits on top while scoring *below*
// rank 2, held there only by the exact-before-fused tier ordering.
func TestScoreGap_NegativeWhenTierOrderingCarriesTheAnswer(t *testing.T) {
	cr := CaseResult{Kind: "rank1", Rank1: "AdjacencyIndex", Rank1Score: 0.016, Rank2: "buildErr", Rank2Score: 0.026}
	assert.InDelta(t, -0.010, cr.ScoreGap(), 1e-9)

	lone := CaseResult{Kind: "rank1", Rank1: "OnlyHit", Rank1Score: 0.016}
	assert.InDelta(t, 0.0, lone.ScoreGap(), 1e-9, "a single hit has no rank 2 to be measured against")
}

// TestAggregateReport_Rank1 — accuracy covers every rank1 case, but the minimum
// gap covers only the passing ones: the margin of a case that lost is the
// usurper's margin, and averaging it into "how safe are we" would be nonsense.
func TestAggregateReport_Rank1(t *testing.T) {
	r := AggregateReport("repo", []CaseResult{
		{CaseID: "impact", Recall: 1},
		{CaseID: "pass-wide", Kind: "rank1", Recall: 1, Rank1: "A", Rank1Score: 0.03, Rank2: "B", Rank2Score: 0.01},
		{CaseID: "pass-thin", Kind: "rank1", Recall: 1, Rank1: "C", Rank1Score: 0.016, Rank2: "D", Rank2Score: 0.026},
		{CaseID: "fail", Kind: "rank1", Recall: 0, HardFail: true, Rank1: "X", Rank1Score: 0.9, Rank2: "Y", Rank2Score: 0.1},
	})
	assert.InDelta(t, 2.0/3.0, r.Rank1Accuracy, 1e-9)
	assert.InDelta(t, -0.010, r.Rank1MinGap, 1e-9, "min gap is the thinnest PASSING margin")
	assert.InDelta(t, 0.75, r.Recall, 1e-9, "rank1 cases participate in overall recall like semantic ones")
}

// TestAggregateReport_Rank1AllFailed — no passing case means no margin at all,
// and 0.0 must not be mistaken for one.
func TestAggregateReport_Rank1AllFailed(t *testing.T) {
	r := AggregateReport("repo", []CaseResult{
		{CaseID: "fail", Kind: "rank1", Recall: 0, HardFail: true, Rank1: "X", Rank1Score: 0.9, Rank2: "Y", Rank2Score: 0.1},
	})
	assert.InDelta(t, 0.0, r.Rank1Accuracy, 1e-9)
	assert.InDelta(t, 0.0, r.Rank1MinGap, 1e-9, "omitempty drops it from the baseline JSON entirely")
}

// TestValidateManifest_Rank1 pins the schema rules, including the two that stop
// a rank1 case from quietly becoming something else.
func TestValidateManifest_Rank1(t *testing.T) {
	base := Manifest{Repo: RepoRef{Name: "r", SHA: "abc", Workspace: "w", Path: "."}}

	valid := base
	valid.Cases = []Case{{ID: "ok", Kind: "rank1", Query: "q", Section: "nodes", ExpectRank1: "Foo"}}
	assert.Empty(t, ValidateManifest(&valid), "a rank1 case needs no must_not_miss")

	for name, c := range map[string]Case{
		"no expect_rank1": {ID: "x", Kind: "rank1", Query: "q", Section: "nodes"},
		"no query":        {ID: "x", Kind: "rank1", Section: "nodes", ExpectRank1: "Foo"},
		"bad section":     {ID: "x", Kind: "rank1", Query: "q", Section: "sources", ExpectRank1: "Foo"},
		"expect_any_of":   {ID: "x", Kind: "rank1", Query: "q", Section: "nodes", ExpectRank1: "Foo", ExpectAnyOf: []string{"Foo"}},
		"must_not_miss":   {ID: "x", Kind: "rank1", Query: "q", Section: "nodes", ExpectRank1: "Foo", MustNotMiss: []string{"Foo"}},
	} {
		m := base
		m.Cases = []Case{c}
		assert.NotEmpty(t, ValidateManifest(&m), name+" must be rejected")
	}
}
