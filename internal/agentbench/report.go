package agentbench

import (
	"fmt"
	"sort"
	"strings"
)

// Arm identifiers — canonical names for the three benchmark arms.
const (
	ArmWithSemantics = "with_polyflow_semantic" // arm 1: polyflow MCP + vector search
	ArmFTSOnly       = "with_polyflow_fts_only" // arm 2: polyflow MCP, --no-embed (FTS only)
	ArmNoPolyflow    = "without_polyflow"        // arm 3: no MCP at all
)

// armOrder is the canonical output order for arms (rule 2 determinism).
var armOrder = []string{ArmWithSemantics, ArmFTSOnly, ArmNoPolyflow}

// TaskResult holds metrics from one agent run (one arm × one task × one trial).
type TaskResult struct {
	TaskID         string   `json:"task_id"`
	Repo           string   `json:"repo"`
	CaseID         string   `json:"case_id"`
	Arm            string   `json:"arm"`
	Trial          int      `json:"trial"`
	InputTokens    int      `json:"input_tokens"`
	OutputTokens   int      `json:"output_tokens"`
	WallMs         int64    `json:"wall_ms"`
	TotalCostUSD   float64  `json:"total_cost_usd"`
	Recall         float64  `json:"recall"`
	Precision      float64  `json:"precision"`
	SilentMisses   int      `json:"silent_misses"`
	HardFail       bool     `json:"hard_fail"`
	ExtractedFiles []string `json:"extracted_files"`
	Error          string   `json:"error,omitempty"`
}

// ArmSummary aggregates metrics across all trials for one arm.
type ArmSummary struct {
	Arm          string  `json:"arm"`
	Trials       int     `json:"trials"`
	AvgRecall    float64 `json:"avg_recall"`
	AvgInputTok  float64 `json:"avg_input_tokens"`
	AvgOutputTok float64 `json:"avg_output_tokens"`
	AvgWallMs    float64 `json:"avg_wall_ms"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	HardFails    int     `json:"hard_fails"`
}

// BenchReport is the full benchmark run output persisted to
// eval/agent-bench/results/<date>.json.
type BenchReport struct {
	RunDate  string       `json:"run_date"`
	Model    string       `json:"model"`
	Note     string       `json:"note,omitempty"`
	Tasks    []TaskResult `json:"tasks"`
	Summary  []ArmSummary `json:"summary"`
}

// Summarize computes per-arm summaries from task results.
// Output follows armOrder for determinism (rule 2).
func Summarize(tasks []TaskResult) []ArmSummary {
	byArm := make(map[string][]TaskResult)
	for _, t := range tasks {
		byArm[t.Arm] = append(byArm[t.Arm], t)
	}
	var out []ArmSummary
	for _, arm := range armOrder {
		ts, ok := byArm[arm]
		if !ok {
			continue
		}
		var sumR, sumP, sumIn, sumOut float64
		var sumWall int64
		var totalCost float64
		var hf int
		for _, t := range ts {
			sumR += t.Recall
			sumP += t.Precision
			sumIn += float64(t.InputTokens)
			sumOut += float64(t.OutputTokens)
			sumWall += t.WallMs
			totalCost += t.TotalCostUSD
			if t.HardFail {
				hf++
			}
		}
		n := float64(len(ts))
		out = append(out, ArmSummary{
			Arm:          arm,
			Trials:       len(ts),
			AvgRecall:    sumR / n,
			AvgInputTok:  sumIn / n,
			AvgOutputTok: sumOut / n,
			AvgWallMs:    float64(sumWall) / n,
			TotalCostUSD: totalCost,
			HardFails:    hf,
		})
	}
	return out
}

// FormatMarkdown renders a BenchReport as a human-readable markdown file.
// Task rows are sorted deterministically (rule 2).
func FormatMarkdown(r BenchReport) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Agent Benchmark Results — %s\n\n", r.RunDate)
	if r.Model != "" {
		fmt.Fprintf(&sb, "**Model:** %s\n\n", r.Model)
	}
	if r.Note != "" {
		fmt.Fprintf(&sb, "> %s\n\n", strings.ReplaceAll(r.Note, "\n", "\n> "))
	}

	sb.WriteString("## Summary\n\n")
	sb.WriteString("| Arm | Trials | Avg Recall | Avg In Tok | Avg Out Tok | Avg Wall (ms) | Total Cost (USD) | Hard Fails |\n")
	sb.WriteString("|-----|--------|------------|------------|-------------|---------------|------------------|------------|\n")
	for _, s := range r.Summary {
		fmt.Fprintf(&sb, "| %s | %d | %.3f | %.0f | %.0f | %.0f | $%.4f | %d |\n",
			s.Arm, s.Trials, s.AvgRecall, s.AvgInputTok, s.AvgOutputTok, s.AvgWallMs,
			s.TotalCostUSD, s.HardFails)
	}

	sb.WriteString("\n## Task Detail\n\n")
	sb.WriteString("| Task | Arm | Trial | Recall | Hard Fail | In Tok | Out Tok | Wall (ms) |\n")
	sb.WriteString("|------|-----|-------|--------|-----------|--------|---------|----------|\n")

	sorted := make([]TaskResult, len(r.Tasks))
	copy(sorted, r.Tasks)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].TaskID != sorted[j].TaskID {
			return sorted[i].TaskID < sorted[j].TaskID
		}
		if sorted[i].Arm != sorted[j].Arm {
			return sorted[i].Arm < sorted[j].Arm
		}
		return sorted[i].Trial < sorted[j].Trial
	})
	for _, t := range sorted {
		hf := ""
		if t.HardFail {
			hf = "YES"
		}
		if t.Error != "" {
			hf = "ERR"
		}
		fmt.Fprintf(&sb, "| %s | %s | %d | %.3f | %s | %d | %d | %d |\n",
			t.TaskID, t.Arm, t.Trial, t.Recall, hf, t.InputTokens, t.OutputTokens, t.WallMs)
	}
	return sb.String()
}
