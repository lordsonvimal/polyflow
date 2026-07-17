package eval_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/eval"
)

// makeReport builds a minimal MultiReport for gate tests.
func makeReport(cases ...eval.CaseResult) *eval.MultiReport {
	var results []eval.CaseResult
	results = append(results, cases...)
	rep := eval.Report{
		Repo:    "testrepo",
		Results: results,
	}
	// Compute aggregate recall.
	if len(results) > 0 {
		var sum float64
		for _, r := range results {
			sum += r.Recall
		}
		rep.Recall = sum / float64(len(results))
	}
	return &eval.MultiReport{
		GeneratedAt: time.Now().UTC(),
		Reports:     []eval.Report{rep},
	}
}

// TestCheckGate_NoRegressions: identical reports pass the gate.
func TestCheckGate_NoRegressions(t *testing.T) {
	baseline := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 1.0, SilentMisses: 0},
		eval.CaseResult{CaseID: "c2", Recall: 0.5, SilentMisses: 1},
	)
	current := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 1.0, SilentMisses: 0},
		eval.CaseResult{CaseID: "c2", Recall: 0.5, SilentMisses: 1},
	)
	gate := eval.CheckGate(current, baseline)
	assert.True(t, gate.OK, "identical reports must pass")
	assert.Empty(t, gate.Regressions)
}

// TestCheckGate_NewHardFail: a case gaining HardFail is a regression.
func TestCheckGate_NewHardFail(t *testing.T) {
	baseline := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 1.0, HardFail: false},
	)
	current := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 0.0, HardFail: true},
	)
	gate := eval.CheckGate(current, baseline)
	assert.False(t, gate.OK)
	require.Len(t, gate.Regressions, 2) // hard_fail + recall_drop
	reasons := make(map[string]bool)
	for _, r := range gate.Regressions {
		reasons[r.Reason] = true
	}
	assert.True(t, reasons["hard_fail"], "hard_fail regression expected")
	assert.True(t, reasons["recall_drop"], "recall_drop regression expected")
}

// TestCheckGate_ExistingHardFailNotNewRegression: pre-existing HardFail is NOT a regression.
func TestCheckGate_ExistingHardFailNotNewRegression(t *testing.T) {
	baseline := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 0.0, HardFail: true, SilentMisses: 1},
	)
	current := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 0.0, HardFail: true, SilentMisses: 1},
	)
	gate := eval.CheckGate(current, baseline)
	assert.True(t, gate.OK, "pre-existing HardFail must not be flagged as a new regression")
	assert.Empty(t, gate.Regressions)
}

// TestCheckGate_RecallDrop: aggregate recall drop triggers recall_drop regression.
func TestCheckGate_RecallDrop(t *testing.T) {
	baseline := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 1.0},
		eval.CaseResult{CaseID: "c2", Recall: 1.0},
	)
	current := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 0.5},
		eval.CaseResult{CaseID: "c2", Recall: 0.5},
	)
	gate := eval.CheckGate(current, baseline)
	assert.False(t, gate.OK)
	reasons := make(map[string]bool)
	for _, r := range gate.Regressions {
		reasons[r.Reason] = true
	}
	assert.True(t, reasons["recall_drop"])
	// Verify the regression records baseline vs current.
	for _, r := range gate.Regressions {
		if r.Reason == "recall_drop" {
			assert.InDelta(t, 1.0, r.BaselineRecall, 1e-9)
			assert.InDelta(t, 0.5, r.CurrentRecall, 1e-9)
		}
	}
}

// TestCheckGate_SilentMissRise: rising silent-miss count per case triggers regression.
func TestCheckGate_SilentMissRise(t *testing.T) {
	baseline := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 0.5, SilentMisses: 1},
	)
	current := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 0.5, SilentMisses: 3},
	)
	gate := eval.CheckGate(current, baseline)
	assert.False(t, gate.OK)
	require.Len(t, gate.Regressions, 1)
	assert.Equal(t, "silent_miss_rise", gate.Regressions[0].Reason)
	assert.Equal(t, "c1", gate.Regressions[0].CaseID)
	assert.Equal(t, 1, gate.Regressions[0].BaselineSilent)
	assert.Equal(t, 3, gate.Regressions[0].CurrentSilent)
}

// TestCheckGate_RatchetUp: recall improvement does NOT trigger a regression.
func TestCheckGate_RatchetUp(t *testing.T) {
	baseline := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 0.5, HardFail: true, SilentMisses: 2},
	)
	current := makeReport(
		// Improvement: recall went up, HardFail cleared, silent misses dropped.
		eval.CaseResult{CaseID: "c1", Recall: 1.0, HardFail: false, SilentMisses: 0},
	)
	gate := eval.CheckGate(current, baseline)
	assert.True(t, gate.OK, "improvement must pass the gate (ratchet up)")
	assert.Empty(t, gate.Regressions)
}

// TestCheckGate_RatchetNeverDown: regression from an improved baseline is caught.
// Scenario: baseline was updated to recall=1.0 (improvement committed), then
// recall drops back to 0.5 — the gate must catch this.
func TestCheckGate_RatchetNeverDown(t *testing.T) {
	baseline := makeReport(
		// Previously improved to perfect recall.
		eval.CaseResult{CaseID: "c1", Recall: 1.0, HardFail: false, SilentMisses: 0},
	)
	current := makeReport(
		// Regression back to old state.
		eval.CaseResult{CaseID: "c1", Recall: 0.5, HardFail: false, SilentMisses: 0},
	)
	gate := eval.CheckGate(current, baseline)
	assert.False(t, gate.OK, "regression from improved baseline must be caught (never down)")
	require.Len(t, gate.Regressions, 1)
	assert.Equal(t, "recall_drop", gate.Regressions[0].Reason)
}

// TestCheckGate_NewCaseWithHardFail: a case not in baseline that is HardFail IS a regression.
func TestCheckGate_NewCaseWithHardFail(t *testing.T) {
	baseline := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 1.0},
	)
	// current adds a new case with HardFail=true.
	current := &eval.MultiReport{
		GeneratedAt: time.Now().UTC(),
		Reports: []eval.Report{{
			Repo:    "testrepo",
			Recall:  0.5,
			Results: []eval.CaseResult{
				{CaseID: "c1", Recall: 1.0},
				{CaseID: "c2-new", Recall: 0.0, HardFail: true, SilentMisses: 1},
			},
		}},
	}
	gate := eval.CheckGate(current, baseline)
	assert.False(t, gate.OK)
	reasons := make(map[string]bool)
	for _, r := range gate.Regressions {
		reasons[r.Reason] = true
	}
	assert.True(t, reasons["hard_fail"])
}

// TestSummarizeForDoctor: aggregate counts are correct.
func TestSummarizeForDoctor(t *testing.T) {
	mr := &eval.MultiReport{
		GeneratedAt: time.Date(2026, 7, 15, 9, 52, 35, 0, time.UTC),
		Reports: []eval.Report{
			{
				Repo:   "r1",
				Recall: 1.0,
				Results: []eval.CaseResult{
					{CaseID: "a", Recall: 1.0, HardFail: false, SilentMisses: 0, HonestMisses: 1},
				},
			},
			{
				Repo:   "r2",
				Recall: 0.5,
				Results: []eval.CaseResult{
					{CaseID: "b", Recall: 0.5, HardFail: true, SilentMisses: 2, HonestMisses: 0},
				},
			},
		},
	}
	sum := eval.SummarizeForDoctor(mr, nil)
	assert.Equal(t, "2026-07-15", sum.GeneratedAt)
	assert.Equal(t, 2, sum.Repos)
	assert.Equal(t, 2, sum.TotalCases)
	assert.InDelta(t, 0.75, sum.AvgRecall, 1e-9)
	assert.Equal(t, 1, sum.HardFails)
	assert.Equal(t, 2, sum.SilentMiss)
	assert.Equal(t, 1, sum.HonestMiss)
	assert.Equal(t, 0, sum.Regressions)
}

// TestCheckGate_MissingRepo: a repo present in the baseline but absent from
// the current run is a regression — a repo that fails to clone or crashes
// during indexing must not read as a silent pass.
func TestCheckGate_MissingRepo(t *testing.T) {
	baseline := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 1.0},
	)
	current := &eval.MultiReport{
		GeneratedAt: time.Now().UTC(),
		Reports:     []eval.Report{}, // testrepo missing entirely
	}
	gate := eval.CheckGate(current, baseline)
	assert.False(t, gate.OK, "missing baseline repo must trip the gate")
	require.Len(t, gate.Regressions, 1)
	assert.Equal(t, "missing_repo", gate.Regressions[0].Reason)
	assert.Equal(t, "testrepo", gate.Regressions[0].Repo)
	assert.Equal(t, "*", gate.Regressions[0].CaseID)
}

// TestCheckGate_NewRepoNotMissing: a repo added in the current run (absent
// from baseline) is not a missing_repo regression.
func TestCheckGate_NewRepoNotMissing(t *testing.T) {
	baseline := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 1.0},
	)
	current := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 1.0},
	)
	current.Reports = append(current.Reports, eval.Report{
		Repo:    "brand-new-repo",
		Results: []eval.CaseResult{{CaseID: "n1", Recall: 0.5}},
		Recall:  0.5,
	})
	gate := eval.CheckGate(current, baseline)
	assert.True(t, gate.OK, "a newly added repo must not trip missing_repo")
}

// TestCheckGate_LocalOnlySkipExempt: a path-based (local-only) baseline repo
// that the current run explicitly skipped does not trip missing_repo — CI
// cannot clone a private local repo. A URL repo skip is NOT exempt.
func TestCheckGate_LocalOnlySkipExempt(t *testing.T) {
	baseline := makeReport(
		eval.CaseResult{CaseID: "c1", Recall: 1.0},
	)
	current := &eval.MultiReport{
		GeneratedAt: time.Now().UTC(),
		Skipped: []eval.SkippedCorpus{
			{Name: "testrepo", Reason: "graph DB absent", LocalOnly: true},
		},
	}
	gate := eval.CheckGate(current, baseline)
	assert.True(t, gate.OK, "local-only skip must be exempt from missing_repo")

	current.Skipped[0].LocalOnly = false // simulate a URL repo whose clone failed
	gate = eval.CheckGate(current, baseline)
	assert.False(t, gate.OK, "URL-repo skip must still trip missing_repo")
	require.Len(t, gate.Regressions, 1)
	assert.Equal(t, "missing_repo", gate.Regressions[0].Reason)
}
