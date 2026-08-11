package agentbench_test

import (
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/agentbench"
)

// replay2026_07_30 reconstructs the shape of the juniper run that produced
// the "median token reduction 0.0%, FAIL" verdict: 7 with_polyflow trials that
// ran, 2 without_polyflow trials that ran, and 5 that died on a 429.
func replay2026_07_30() []agentbench.TaskResult {
	var out []agentbench.TaskResult
	ran := []struct {
		id       string
		withCtx  int
		woCtx    int
		withRec  float64
		woRec    float64
		bothRan  bool
		hardFail bool
	}{
		{id: "snapshot-manager-callers", withCtx: 53407, woCtx: 53251, withRec: 1, woRec: 1, bothRan: true},
		{id: "exec-config-yaml-blast", withCtx: 56149, woCtx: 53918, withRec: 1, woRec: 1, bothRan: true},
		{id: "container-lifecycle-event-reporting", withCtx: 37748, withRec: 1},
		{id: "pdv-package-download", withCtx: 92821, withRec: 0, hardFail: true},
		{id: "csv-vulnerability-export", withCtx: 55810, withRec: 0.667, hardFail: true},
		{id: "messaging-scan-event-publisher", withCtx: 56228, withRec: 1},
		{id: "user-sync-adapter-provider", withCtx: 57535, withRec: 0.8},
	}
	for _, r := range ran {
		out = append(out, agentbench.TaskResult{
			TaskID: r.id, Trial: 1, Arm: agentbench.ArmWithSemantics,
			ContextTokens: r.withCtx, Recall: r.withRec, HardFail: r.hardFail, NumTurns: 3,
		})
		wo := agentbench.TaskResult{TaskID: r.id, Trial: 1, Arm: agentbench.ArmNoPolyflow}
		if r.bothRan {
			wo.ContextTokens = r.woCtx
			wo.Recall = r.woRec
			wo.NumTurns = 3
		} else {
			wo.Error = "You've hit your session limit"
			wo.ErrorClass = agentbench.FailureQuota
		}
		out = append(out, wo)
	}
	return out
}

// The headline defect: five trials that never happened were averaged in as
// recall 0, producing a 0.286 control-arm recall out of two 1.000 measurements.
func TestSummarize_ExcludesUnmeasuredTrials(t *testing.T) {
	sums := agentbench.Summarize(replay2026_07_30())

	var without agentbench.ArmSummary
	for _, s := range sums {
		if s.Arm == agentbench.ArmNoPolyflow {
			without = s
		}
	}
	if without.Trials != 2 {
		t.Errorf("Trials = %d, want 2 (only the trials that produced a transcript)", without.Trials)
	}
	if without.Errors != 5 {
		t.Errorf("Errors = %d, want 5", without.Errors)
	}
	if without.AvgRecall != 1.0 {
		t.Errorf("AvgRecall = %.3f, want 1.000 — the old code reported 0.286 by averaging in "+
			"five trials that never ran", without.AvgRecall)
	}
	if without.AvgContextTok < 53000 {
		t.Errorf("AvgContextTok = %.0f, want ~53585; zeros from failed trials must not deflate it",
			without.AvgContextTok)
	}
}

func TestSummarize_AllTrialsFailed(t *testing.T) {
	sums := agentbench.Summarize([]agentbench.TaskResult{
		{Arm: agentbench.ArmNoPolyflow, Trial: 1, Error: "429", ErrorClass: agentbench.FailureQuota},
	})
	if len(sums) != 1 {
		t.Fatalf("want 1 summary, got %d", len(sums))
	}
	if sums[0].Trials != 0 || sums[0].Errors != 1 {
		t.Errorf("Trials=%d Errors=%d, want 0/1", sums[0].Trials, sums[0].Errors)
	}
	if sums[0].AvgRecall != 0 {
		t.Errorf("AvgRecall = %v, want 0 with no claim attached", sums[0].AvgRecall)
	}
}

// E.2 requires 100% valid A/B pairs. Nothing in the old report measured this,
// so a 2-of-7 comparison was presented as a 7-trial result.
func TestComputePairing_ReportsTheMissingArms(t *testing.T) {
	p := agentbench.ComputePairing(replay2026_07_30())
	if p.Keys != 7 {
		t.Errorf("Keys = %d, want 7", p.Keys)
	}
	if p.ValidPairs != 2 {
		t.Errorf("ValidPairs = %d, want 2", p.ValidPairs)
	}
	if p.Validity > 0.2858 || p.Validity < 0.2856 {
		t.Errorf("Validity = %.4f, want ~0.2857", p.Validity)
	}
	// Median over the two real pairs: both arms cost about the same, and
	// polyflow is slightly worse. That is the honest number.
	if p.MedianTokenReductionPct > 0 {
		t.Errorf("MedianTokenReductionPct = %.2f, want <= 0 on this data", p.MedianTokenReductionPct)
	}
}

func TestComputePairing_PerfectRun(t *testing.T) {
	p := agentbench.ComputePairing([]agentbench.TaskResult{
		{TaskID: "a", Trial: 1, Arm: agentbench.ArmWithSemantics, ContextTokens: 2000},
		{TaskID: "a", Trial: 1, Arm: agentbench.ArmNoPolyflow, ContextTokens: 10000},
	})
	if p.Keys != 1 || p.ValidPairs != 1 || p.Validity != 1.0 {
		t.Fatalf("pairing = %+v, want 1/1/1.0", p)
	}
	if p.MedianTokenReductionPct != 80 {
		t.Errorf("MedianTokenReductionPct = %.1f, want 80", p.MedianTokenReductionPct)
	}
}

func TestFormatMarkdown_FlagsIncompleteRun(t *testing.T) {
	tasks := replay2026_07_30()
	p := agentbench.ComputePairing(tasks)
	md := agentbench.FormatMarkdown(agentbench.BenchReport{
		RunDate: "2026-08-11",
		Tasks:   tasks,
		Summary: agentbench.Summarize(tasks),
		Pairing: &p,
		Aborted: "stopped on an API quota/session limit",
	})
	for _, want := range []string{"INCOMPLETE RUN", "A/B Pairing", "Pair validity is 29%", "ERR:quota"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n%s", want, md)
		}
	}
}

// A flow task that errored is not evidence that polyflow got the flow wrong.
func TestComputeFlowGate_SkipsUnmeasuredTrials(t *testing.T) {
	tasks := []agentbench.TaskResult{
		{TaskID: "f1", Trial: 1, Kind: "flow", Arm: agentbench.ArmWithSemantics, ContextTokens: 1000, Recall: 1},
		{TaskID: "f1", Trial: 1, Kind: "flow", Arm: agentbench.ArmNoPolyflow, ContextTokens: 10000, Recall: 1},
		{TaskID: "f2", Trial: 1, Kind: "flow", Arm: agentbench.ArmWithSemantics,
			Error: "429", ErrorClass: agentbench.FailureQuota},
	}
	g := agentbench.ComputeFlowGate(tasks)
	if g.TaskCount != 1 {
		t.Errorf("TaskCount = %d, want 1 (the errored flow trial is not a task result)", g.TaskCount)
	}
	if g.CorrectnessWithPolyflow != 1.0 {
		t.Errorf("CorrectnessWithPolyflow = %.3f, want 1.000", g.CorrectnessWithPolyflow)
	}
}
