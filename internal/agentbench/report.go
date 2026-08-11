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
	ArmNoPolyflow    = "without_polyflow"       // arm 3: no MCP at all
)

// armOrder is the canonical output order for arms (rule 2 determinism).
var armOrder = []string{ArmWithSemantics, ArmFTSOnly, ArmNoPolyflow}

// TaskResult holds metrics from one agent run (one arm × one task × one trial).
type TaskResult struct {
	TaskID         string   `json:"task_id"`
	Repo           string   `json:"repo"`
	CaseID         string   `json:"case_id"`
	Kind           string   `json:"kind"`
	Arm            string   `json:"arm"`
	Trial          int      `json:"trial"`
	InputTokens    int      `json:"input_tokens"`
	OutputTokens   int      `json:"output_tokens"`
	ContextTokens  int      `json:"context_tokens"`
	NumTurns       int      `json:"num_turns"`
	WallMs         int64    `json:"wall_ms"`
	TotalCostUSD   float64  `json:"total_cost_usd"`
	Recall         float64  `json:"recall"`
	Precision      float64  `json:"precision"`
	SilentMisses   int      `json:"silent_misses"`
	HardFail       bool     `json:"hard_fail"`
	ExtractedFiles []string `json:"extracted_files"`
	Error          string   `json:"error,omitempty"`
	// ErrorClass is why the trial failed, when it did. A trial with an
	// ErrorClass is not a measurement and is excluded from every average.
	ErrorClass FailureClass `json:"error_class,omitempty"`
}

// Measured reports whether this trial produced numbers that mean anything.
// A failed trial carries zeros in every metric field; averaging those in is
// how the 2026-07-30 run reported a control-arm recall of 0.286 that no agent
// ever produced.
func (t TaskResult) Measured() bool { return t.Error == "" && t.ErrorClass == FailureNone }

// measured filters a slice down to the trials that actually ran.
func measured(ts []TaskResult) []TaskResult {
	out := make([]TaskResult, 0, len(ts))
	for _, t := range ts {
		if t.Measured() {
			out = append(out, t)
		}
	}
	return out
}

// ArmSummary aggregates metrics across all trials for one arm.
type ArmSummary struct {
	Arm           string  `json:"arm"`
	Trials        int     `json:"trials"`
	AvgRecall     float64 `json:"avg_recall"`
	AvgInputTok   float64 `json:"avg_input_tokens"`
	AvgOutputTok  float64 `json:"avg_output_tokens"`
	AvgContextTok float64 `json:"avg_context_tokens"`
	AvgTurns      float64 `json:"avg_turns"`
	AvgWallMs     float64 `json:"avg_wall_ms"`
	TotalCostUSD  float64 `json:"total_cost_usd"`
	HardFails     int     `json:"hard_fails"`
	// Errors counts trials that never produced a transcript. They are reported
	// beside the averages, never inside them: Trials counts measurements only.
	Errors int `json:"errors"`
}

// BenchReport is the full benchmark run output persisted to
// eval/agent-bench/results/<date>.json.
type BenchReport struct {
	RunDate  string       `json:"run_date"`
	Model    string       `json:"model"`
	Note     string       `json:"note,omitempty"`
	Tasks    []TaskResult `json:"tasks"`
	Summary  []ArmSummary `json:"summary"`
	Pairing  *Pairing     `json:"pairing,omitempty"`
	FlowGate *FlowGate    `json:"flow_gate,omitempty"`
	// Aborted is set when the run stopped early (quota) rather than finishing
	// the task list, so a partial report can never be read as a complete one.
	Aborted string `json:"aborted,omitempty"`
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
		all, ok := byArm[arm]
		if !ok {
			continue
		}
		ts := measured(all)
		errs := len(all) - len(ts)
		if len(ts) == 0 {
			// Every trial failed: report the failures, claim no numbers.
			out = append(out, ArmSummary{Arm: arm, Errors: errs})
			continue
		}
		var sumR, sumP, sumIn, sumOut, sumCtx, sumTurns float64
		var sumWall int64
		var totalCost float64
		var hf int
		for _, t := range ts {
			sumR += t.Recall
			sumP += t.Precision
			sumIn += float64(t.InputTokens)
			sumOut += float64(t.OutputTokens)
			sumCtx += float64(t.ContextTokens)
			sumTurns += float64(t.NumTurns)
			sumWall += t.WallMs
			totalCost += t.TotalCostUSD
			if t.HardFail {
				hf++
			}
		}
		n := float64(len(ts))
		out = append(out, ArmSummary{
			Arm:           arm,
			Trials:        len(ts),
			AvgRecall:     sumR / n,
			AvgInputTok:   sumIn / n,
			AvgOutputTok:  sumOut / n,
			AvgContextTok: sumCtx / n,
			AvgTurns:      sumTurns / n,
			AvgWallMs:     float64(sumWall) / n,
			TotalCostUSD:  totalCost,
			HardFails:     hf,
			Errors:        errs,
		})
	}
	return out
}

// Pairing describes how much of the run is actually an A/B comparison.
//
// A token-reduction number is only meaningful over (task, trial) keys where
// *both* arms produced a transcript. On 2026-07-30 only 2 of 7 tasks had both
// arms run, and nothing in the report said so — the headline compared a
// 7-trial arm against a 2-trial arm wearing 5 zeros.
type Pairing struct {
	// Keys is the number of (task, trial) combinations attempted in both arms.
	Keys int `json:"keys"`
	// ValidPairs is how many of those produced a measurement on both sides.
	ValidPairs int `json:"valid_pairs"`
	// Validity is ValidPairs/Keys — E.2 requires 1.0.
	Validity float64 `json:"validity"`
	// MedianTokenReductionPct is computed over the valid pairs only.
	MedianTokenReductionPct float64 `json:"median_token_reduction_pct"`
}

// ComputePairing pairs with_polyflow_semantic against without_polyflow by
// (task_id, trial) and reports how much of the run survived as a comparison.
func ComputePairing(tasks []TaskResult) Pairing {
	type key struct {
		taskID string
		trial  int
	}
	with := make(map[key]TaskResult)
	without := make(map[key]TaskResult)
	for _, t := range tasks {
		switch t.Arm {
		case ArmWithSemantics:
			with[key{t.TaskID, t.Trial}] = t
		case ArmNoPolyflow:
			without[key{t.TaskID, t.Trial}] = t
		}
	}

	var p Pairing
	var reductions []float64
	for k, w := range with {
		wo, ok := without[k]
		if !ok {
			continue
		}
		p.Keys++
		if !w.Measured() || !wo.Measured() || wo.ContextTokens <= 0 {
			continue
		}
		p.ValidPairs++
		reductions = append(reductions,
			(float64(wo.ContextTokens)-float64(w.ContextTokens))/float64(wo.ContextTokens)*100)
	}
	if p.Keys > 0 {
		p.Validity = float64(p.ValidPairs) / float64(p.Keys)
	}
	if len(reductions) > 0 {
		sort.Float64s(reductions)
		p.MedianTokenReductionPct = medianFloat64(reductions)
	}
	return p
}

// FlowGate is X.4's own acceptance bar, computed only over kind=flow tasks:
// median token reduction of with_polyflow_semantic vs without_polyflow, and
// with_polyflow_semantic correctness (Recall==1.0 && !HardFail, no partial
// credit — matches eval.CaseResult's existing fields, no new scoring
// semantics).
type FlowGate struct {
	MedianTokenReductionPct float64 `json:"median_token_reduction_pct"` // must be >= 80
	CorrectnessWithPolyflow float64 `json:"correctness_with_polyflow"`  // must be >= 0.95
	Pass                    bool    `json:"pass"`
	TaskCount               int     `json:"task_count"`
}

// ComputeFlowGate computes the flow-subset token/correctness gate. Token
// reduction is measured per matching (task_id, trial) pair between
// with_polyflow_semantic and without_polyflow's context tokens; correctness
// is the fraction of with_polyflow_semantic runs with Recall==1.0 and no
// HardFail. Task order follows the input slice (rule 2 determinism — the
// caller already sorts by (repo, caseID)).
func ComputeFlowGate(tasks []TaskResult) FlowGate {
	type key struct {
		taskID string
		trial  int
	}
	withoutByKey := make(map[key]TaskResult)
	for _, t := range tasks {
		if t.Kind == "flow" && t.Arm == ArmNoPolyflow && t.Measured() {
			withoutByKey[key{t.TaskID, t.Trial}] = t
		}
	}

	var withTasks []TaskResult
	var reductions []float64
	for _, t := range tasks {
		if t.Kind != "flow" || t.Arm != ArmWithSemantics || !t.Measured() {
			continue
		}
		withTasks = append(withTasks, t)
		if wo, ok := withoutByKey[key{t.TaskID, t.Trial}]; ok && wo.ContextTokens > 0 {
			reductions = append(reductions, (float64(wo.ContextTokens)-float64(t.ContextTokens))/float64(wo.ContextTokens)*100)
		}
	}

	var g FlowGate
	g.TaskCount = len(withTasks)
	if len(reductions) > 0 {
		sort.Float64s(reductions)
		g.MedianTokenReductionPct = medianFloat64(reductions)
	}
	if len(withTasks) > 0 {
		var correct int
		for _, t := range withTasks {
			if t.Recall == 1.0 && !t.HardFail {
				correct++
			}
		}
		g.CorrectnessWithPolyflow = float64(correct) / float64(len(withTasks))
	}
	g.Pass = g.MedianTokenReductionPct >= 80 && g.CorrectnessWithPolyflow >= 0.95
	return g
}

// medianFloat64 returns the median of a pre-sorted slice.
func medianFloat64(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
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
	if r.Aborted != "" {
		fmt.Fprintf(&sb, "> **INCOMPLETE RUN — %s.** The task list was not finished; "+
			"these numbers describe only the trials below.\n\n", r.Aborted)
	}

	sb.WriteString("## Summary\n\n")
	sb.WriteString("_Context Tok = input + cache-creation + cache-read: the total tokens the model " +
		"processed across all tool round-trips (the real per-run context cost). Out Tok is the " +
		"final-answer size._\n\n")
	sb.WriteString("_Trials counts measurements only: a trial that never produced a transcript is " +
		"counted under Errors and excluded from every average._\n\n")
	sb.WriteString("| Arm | Trials | Errors | Avg Recall | Avg Context Tok | Avg Turns | Avg Out Tok | Avg Wall (ms) | Total Cost (USD) | Hard Fails |\n")
	sb.WriteString("|-----|--------|--------|------------|-----------------|-----------|-------------|---------------|------------------|------------|\n")
	for _, s := range r.Summary {
		fmt.Fprintf(&sb, "| %s | %d | %d | %.3f | %.0f | %.1f | %.0f | %.0f | $%.4f | %d |\n",
			s.Arm, s.Trials, s.Errors, s.AvgRecall, s.AvgContextTok, s.AvgTurns, s.AvgOutputTok, s.AvgWallMs,
			s.TotalCostUSD, s.HardFails)
	}

	if r.Pairing != nil {
		p := *r.Pairing
		sb.WriteString("\n## A/B Pairing\n\n")
		sb.WriteString("| Paired Keys | Valid Pairs | Validity | Median Token Reduction |\n")
		sb.WriteString("|-------------|-------------|----------|------------------------|\n")
		fmt.Fprintf(&sb, "| %d | %d | %.3f | %.1f%% |\n",
			p.Keys, p.ValidPairs, p.Validity, p.MedianTokenReductionPct)
		if p.Validity < 1.0 {
			fmt.Fprintf(&sb, "\n**Pair validity is %.0f%%, not 100%%.** %d of %d comparisons are "+
				"missing an arm, so the token-reduction figure describes a subset of the task "+
				"list and the arm averages are not measured over the same tasks.\n",
				p.Validity*100, p.Keys-p.ValidPairs, p.Keys)
		}
	}

	if r.FlowGate != nil {
		fg := *r.FlowGate
		verdict := "FAIL"
		if fg.Pass {
			verdict = "PASS"
		}
		sb.WriteString("\n## Flow Gate (X.4 acceptance bar)\n\n")
		fmt.Fprintf(&sb, "| Median Token Reduction | Correctness (with polyflow) | Task Count | Verdict |\n")
		fmt.Fprintf(&sb, "|------------------------|------------------------------|------------|---------|\n")
		fmt.Fprintf(&sb, "| %.1f%% | %.3f | %d | %s |\n",
			fg.MedianTokenReductionPct, fg.CorrectnessWithPolyflow, fg.TaskCount, verdict)
	}

	sb.WriteString("\n## Task Detail\n\n")
	sb.WriteString("| Task | Arm | Trial | Recall | Hard Fail | Context Tok | Turns | Out Tok | Wall (ms) |\n")
	sb.WriteString("|------|-----|-------|--------|-----------|-------------|-------|---------|----------|\n")

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
		if !t.Measured() {
			hf = "ERR"
			if t.ErrorClass != FailureNone {
				hf = "ERR:" + string(t.ErrorClass)
			}
		}
		fmt.Fprintf(&sb, "| %s | %s | %d | %.3f | %s | %d | %d | %d | %d |\n",
			t.TaskID, t.Arm, t.Trial, t.Recall, hf, t.ContextTokens, t.NumTurns, t.OutputTokens, t.WallMs)
	}
	return sb.String()
}
