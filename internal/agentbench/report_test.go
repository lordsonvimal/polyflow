package agentbench_test

import (
	"strings"
	"testing"

	"github.com/lordsonvimal/polyflow/internal/agentbench"
	"github.com/lordsonvimal/polyflow/internal/eval"
)

func TestScoreTranscript(t *testing.T) {
	tr := agentbench.Transcript{
		Result: "internal/impact/impact.go and internal/trace/trace.go are affected.",
	}
	expected := []string{
		"internal/impact/impact.go",
		"internal/impact/file.go", // not in result — a miss
		"internal/trace/trace.go",
	}
	mustNotMiss := []string{"internal/impact/impact.go"}
	cr := agentbench.ScoreTranscript("test-case", tr, expected, mustNotMiss)

	if cr.HardFail {
		t.Error("HardFail should be false (must_not_miss was found)")
	}
	// Recall = 2/3
	if cr.Recall < 0.66 || cr.Recall > 0.67 {
		t.Errorf("Recall = %.3f, want ~0.667", cr.Recall)
	}
}

func TestScoreTranscript_MustNotMiss_HardFail(t *testing.T) {
	tr := agentbench.Transcript{
		Result: "internal/trace/trace.go was changed",
	}
	cr := agentbench.ScoreTranscript("x", tr,
		[]string{"internal/impact/impact.go", "internal/trace/trace.go"},
		[]string{"internal/impact/impact.go"},
	)
	if !cr.HardFail {
		t.Error("HardFail should be true: must_not_miss file was not mentioned")
	}
}

func TestSummarize_Empty(t *testing.T) {
	s := agentbench.Summarize(nil)
	if len(s) != 0 {
		t.Errorf("expected empty summary, got %v", s)
	}
}

func TestSummarize_ArmOrder(t *testing.T) {
	tasks := []agentbench.TaskResult{
		{Arm: agentbench.ArmNoPolyflow, Recall: 0.5, Trial: 1},
		{Arm: agentbench.ArmFTSOnly, Recall: 0.8, Trial: 1},
		{Arm: agentbench.ArmWithSemantics, Recall: 1.0, Trial: 1},
	}
	s := agentbench.Summarize(tasks)
	if len(s) != 3 {
		t.Fatalf("expected 3 summaries, got %d", len(s))
	}
	// Canonical order: semantic first, fts second, no-mcp third.
	if s[0].Arm != agentbench.ArmWithSemantics {
		t.Errorf("s[0].Arm = %q, want %q", s[0].Arm, agentbench.ArmWithSemantics)
	}
	if s[1].Arm != agentbench.ArmFTSOnly {
		t.Errorf("s[1].Arm = %q, want %q", s[1].Arm, agentbench.ArmFTSOnly)
	}
	if s[2].Arm != agentbench.ArmNoPolyflow {
		t.Errorf("s[2].Arm = %q, want %q", s[2].Arm, agentbench.ArmNoPolyflow)
	}
}

func TestSummarize_Math(t *testing.T) {
	tasks := []agentbench.TaskResult{
		{Arm: agentbench.ArmWithSemantics, Recall: 1.0, InputTokens: 100, OutputTokens: 20, WallMs: 1000},
		{Arm: agentbench.ArmWithSemantics, Recall: 0.5, InputTokens: 200, OutputTokens: 40, WallMs: 2000},
	}
	s := agentbench.Summarize(tasks)
	if len(s) != 1 {
		t.Fatalf("expected 1 arm summary")
	}
	as := s[0]
	if as.AvgRecall != 0.75 {
		t.Errorf("AvgRecall = %.3f, want 0.75", as.AvgRecall)
	}
	if as.AvgInputTok != 150 {
		t.Errorf("AvgInputTok = %.0f, want 150", as.AvgInputTok)
	}
	if as.AvgWallMs != 1500 {
		t.Errorf("AvgWallMs = %.0f, want 1500", as.AvgWallMs)
	}
}

func TestSummarize_HardFailCount(t *testing.T) {
	tasks := []agentbench.TaskResult{
		{Arm: agentbench.ArmWithSemantics, HardFail: true},
		{Arm: agentbench.ArmWithSemantics, HardFail: false},
		{Arm: agentbench.ArmWithSemantics, HardFail: true},
	}
	s := agentbench.Summarize(tasks)
	if s[0].HardFails != 2 {
		t.Errorf("HardFails = %d, want 2", s[0].HardFails)
	}
}

func TestFormatMarkdown_RequiredSections(t *testing.T) {
	r := agentbench.BenchReport{
		RunDate: "2026-07-21",
		Model:   "claude-sonnet-4-6",
		Tasks: []agentbench.TaskResult{
			{TaskID: "polyflow/case1", Arm: agentbench.ArmWithSemantics, Trial: 1, Recall: 1.0},
		},
		Summary: []agentbench.ArmSummary{
			{Arm: agentbench.ArmWithSemantics, Trials: 1, AvgRecall: 1.0},
		},
	}
	md := agentbench.FormatMarkdown(r)
	for _, want := range []string{
		"# Agent Benchmark Results — 2026-07-21",
		"claude-sonnet-4-6",
		"## Summary",
		"## Task Detail",
		agentbench.ArmWithSemantics,
		"polyflow/case1",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
}

func TestFormatMarkdown_Determinism(t *testing.T) {
	r := agentbench.BenchReport{
		RunDate: "2026-07-21",
		Tasks: []agentbench.TaskResult{
			{TaskID: "b", Arm: agentbench.ArmNoPolyflow, Trial: 1},
			{TaskID: "a", Arm: agentbench.ArmWithSemantics, Trial: 1},
			{TaskID: "a", Arm: agentbench.ArmFTSOnly, Trial: 1},
		},
	}
	a := agentbench.FormatMarkdown(r)
	b := agentbench.FormatMarkdown(r)
	if a != b {
		t.Error("FormatMarkdown is not deterministic")
	}
}

// TestScoreTranscript_ReusesEvalScorer ensures ScoreTranscript delegates to eval.Score
// (same quadrant behaviour: honest vs silent misses).
func TestScoreTranscript_ReusesEvalScorer(t *testing.T) {
	tr := agentbench.Transcript{Result: ""}
	cr := agentbench.ScoreTranscript("x",
		tr,
		[]string{"internal/a/b.go"},
		[]string{"internal/a/b.go"},
	)
	// Expect eval.CaseResult with HardFail=true (silent miss of must_not_miss)
	var _ eval.CaseResult = cr // compile-time type check
	if !cr.HardFail {
		t.Error("expected HardFail=true when must_not_miss is absent from empty result")
	}
}
