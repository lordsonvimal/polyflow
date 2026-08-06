package eval_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lordsonvimal/polyflow/internal/eval"
)

// makeAgentReport builds a minimal AgentMultiReport for gate tests.
func makeAgentReport(cases ...eval.AgentCaseResult) *eval.AgentMultiReport {
	var results []eval.AgentCaseResult
	results = append(results, cases...)
	correct := 0
	for _, r := range results {
		if r.Correct {
			correct++
		}
	}
	correctness := 0.0
	if len(results) > 0 {
		correctness = float64(correct) / float64(len(results))
	}
	return &eval.AgentMultiReport{
		GeneratedAt: time.Now().UTC(),
		Reports: []eval.AgentReport{{
			Repo:        "testrepo",
			Correctness: correctness,
			Results:     results,
		}},
	}
}

func TestCheckAgentGate_NoRegressions(t *testing.T) {
	baseline := makeAgentReport(eval.AgentCaseResult{ID: "q1", Correct: true})
	current := makeAgentReport(eval.AgentCaseResult{ID: "q1", Correct: true})
	gate := eval.CheckAgentGate(current, baseline)
	assert.True(t, gate.OK)
	assert.Empty(t, gate.Regressions)
}

// TestCheckAgentGate_NowIncorrect: a case correct in baseline, incorrect now.
func TestCheckAgentGate_NowIncorrect(t *testing.T) {
	baseline := makeAgentReport(eval.AgentCaseResult{ID: "q1", Correct: true})
	current := makeAgentReport(eval.AgentCaseResult{ID: "q1", Correct: false})
	gate := eval.CheckAgentGate(current, baseline)
	assert.False(t, gate.OK)
	reasons := make(map[string]bool)
	for _, r := range gate.Regressions {
		reasons[r.Reason] = true
	}
	assert.True(t, reasons["now_incorrect"])
	assert.True(t, reasons["correctness_drop"])
}

// TestCheckAgentGate_NewCaseIncorrectNotFlagged: a case absent from baseline
// (new case) that is incorrect must not trip now_incorrect — new cases enter
// the baseline failing, then ratchet (CheckGate's pre-existing-failure
// precedent).
func TestCheckAgentGate_NewCaseIncorrectNotFlagged(t *testing.T) {
	baseline := makeAgentReport(eval.AgentCaseResult{ID: "q1", Correct: true})
	current := makeAgentReport(
		eval.AgentCaseResult{ID: "q1", Correct: true},
		eval.AgentCaseResult{ID: "q2-new", Correct: false},
	)
	gate := eval.CheckAgentGate(current, baseline)
	for _, r := range gate.Regressions {
		assert.NotEqual(t, "now_incorrect", r.Reason, "a brand-new incorrect case must not trip now_incorrect")
	}
}

// TestCheckAgentGate_CorrectnessDrop: aggregate correctness drop trips the gate.
func TestCheckAgentGate_CorrectnessDrop(t *testing.T) {
	baseline := makeAgentReport(
		eval.AgentCaseResult{ID: "q1", Correct: true},
		eval.AgentCaseResult{ID: "q2", Correct: true},
	)
	current := makeAgentReport(
		eval.AgentCaseResult{ID: "q1", Correct: true},
		eval.AgentCaseResult{ID: "q2", Correct: false},
	)
	gate := eval.CheckAgentGate(current, baseline)
	assert.False(t, gate.OK)
	var found bool
	for _, r := range gate.Regressions {
		if r.Reason == "correctness_drop" {
			found = true
			assert.InDelta(t, 1.0, r.BaselineCorrectness, 1e-9)
			assert.InDelta(t, 0.5, r.CurrentCorrectness, 1e-9)
		}
	}
	assert.True(t, found)
}

// TestCheckAgentGate_RatchetUp: improvement never trips the gate.
func TestCheckAgentGate_RatchetUp(t *testing.T) {
	baseline := makeAgentReport(eval.AgentCaseResult{ID: "q1", Correct: false})
	current := makeAgentReport(eval.AgentCaseResult{ID: "q1", Correct: true})
	gate := eval.CheckAgentGate(current, baseline)
	assert.True(t, gate.OK)
	assert.Empty(t, gate.Regressions)
}

func TestCheckAgentGate_MissingRepo(t *testing.T) {
	baseline := makeAgentReport(eval.AgentCaseResult{ID: "q1", Correct: true})
	current := &eval.AgentMultiReport{GeneratedAt: time.Now().UTC()}
	gate := eval.CheckAgentGate(current, baseline)
	assert.False(t, gate.OK)
	require.Len(t, gate.Regressions, 1)
	assert.Equal(t, "missing_repo", gate.Regressions[0].Reason)
	assert.Equal(t, "testrepo", gate.Regressions[0].Repo)
}

// TestCheckAgentGate_LocalOnlySkipExempt mirrors CheckGate's LocalOnly exemption.
func TestCheckAgentGate_LocalOnlySkipExempt(t *testing.T) {
	baseline := makeAgentReport(eval.AgentCaseResult{ID: "q1", Correct: true})
	current := &eval.AgentMultiReport{
		GeneratedAt: time.Now().UTC(),
		Skipped: []eval.SkippedCorpus{
			{Name: "testrepo", Reason: "agent CLI unavailable", LocalOnly: true},
		},
	}
	gate := eval.CheckAgentGate(current, baseline)
	assert.True(t, gate.OK, "local-only skip must be exempt from missing_repo")

	current.Skipped[0].LocalOnly = false
	gate = eval.CheckAgentGate(current, baseline)
	assert.False(t, gate.OK, "URL-repo skip must still trip missing_repo")
}

// TestLoadAgentBaseline_RoundTrip: marshal then reload is byte-for-byte
// semantically identical (determinism, bug-class rule 2).
func TestLoadAgentBaseline_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	original := &eval.AgentMultiReport{
		GeneratedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		Reports: []eval.AgentReport{
			{Repo: "chessleap", Correctness: 0.95, Results: []eval.AgentCaseResult{
				{ID: "q1", Correct: true, MissingFacts: []string{}, ForbiddenHit: []string{}, Answer: "x"},
			}},
		},
	}
	data, err := json.MarshalIndent(original, "", "  ")
	require.NoError(t, err)
	path := dir + "/agent-baseline.json"
	require.NoError(t, os.WriteFile(path, data, 0o644))

	loaded, err := eval.LoadAgentBaseline(path)
	require.NoError(t, err)
	assert.Equal(t, original.GeneratedAt, loaded.GeneratedAt)
	assert.Equal(t, original.Reports, loaded.Reports)
}

func TestSummarizeAgentForDoctor_Unmeasured(t *testing.T) {
	sum := eval.SummarizeAgentForDoctor(nil, "chessleap")
	assert.False(t, sum.Measured)

	baseline := makeAgentReport(eval.AgentCaseResult{ID: "q1", Correct: true})
	sum = eval.SummarizeAgentForDoctor(baseline, "not-in-baseline")
	assert.False(t, sum.Measured)
}

func TestSummarizeAgentForDoctor_Measured(t *testing.T) {
	baseline := makeAgentReport(
		eval.AgentCaseResult{ID: "q1", Correct: true},
		eval.AgentCaseResult{ID: "q2", Correct: false},
	)
	sum := eval.SummarizeAgentForDoctor(baseline, "testrepo")
	assert.True(t, sum.Measured)
	assert.Equal(t, 2, sum.Cases)
	assert.InDelta(t, 0.5, sum.Correctness, 1e-9)
	assert.Equal(t, baseline.GeneratedAt.Format("2006-01-02"), sum.MeasuredAt)
}
