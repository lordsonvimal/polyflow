package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/agentbench"
	"github.com/lordsonvimal/polyflow/internal/eval"
)

//go:embed hookscripts/polyflow-first.py
var hookScriptGrepNudge []byte

//go:embed hookscripts/polyflow-read-gate.py
var hookScriptReadGate []byte

var (
	benchCorpus     string
	benchModel      string
	benchTrials     int
	benchArm        string
	benchRepo       string
	benchOutput     string
	benchDryRun     bool
	benchMaxPerRepo int
	benchFresh      bool
)

var benchCmd = &cobra.Command{
	Use:   "bench",
	Short: "Run the P.1 agent outcome benchmark (manual-triggered; costs real tokens)",
	Long: `Run the agent outcome benchmark across two arms:
  1. with_polyflow_semantic — polyflow MCP + vector search active
  2. without_polyflow        — no MCP; agent answers without the graph

Tasks are drawn from the eval corpus impact cases. Results are written to
eval/agent-bench/results/<date>.json and eval/agent-bench/results/<date>.md.

This command is MANUAL-TRIGGERED — each run costs real tokens and is never run in CI.`,
	RunE: runBench,
}

func init() {
	benchCmd.Flags().StringVar(&benchCorpus, "corpus", "eval/corpus", "path to eval corpus root")
	benchCmd.Flags().StringVar(&benchModel, "model", "claude-sonnet-4-6", "claude model to use")
	benchCmd.Flags().IntVar(&benchTrials, "trials", 1, "trials per task/arm")
	benchCmd.Flags().StringVar(&benchArm, "arm", "", "run only this arm (leave empty for both)")
	benchCmd.Flags().StringVar(&benchRepo, "repo", "", "filter tasks to this corpus repo name (e.g. polyflow)")
	benchCmd.Flags().StringVar(&benchOutput, "output", "eval/agent-bench/results", "directory for result files")
	benchCmd.Flags().BoolVar(&benchDryRun, "dry-run", false, "print tasks and prompts without calling claude")
	benchCmd.Flags().IntVar(&benchMaxPerRepo, "max-per-repo", 10,
		"cap tasks taken from each corpus repo (0 = no cap); the whole corpus is 186 tasks, which no single session's budget survives")
	benchCmd.Flags().BoolVar(&benchFresh, "fresh", false,
		"discard any checkpoint and re-run every task from scratch (re-pays for trials already bought)")
}

// benchTask is one task in the benchmark, derived from an eval corpus case.
type benchTask struct {
	TaskID         string
	Repo           string
	CaseID         string
	Kind           string
	Prompt         string
	Expected       []string
	MustNotMiss    []string
	MustNotInclude []string
}

func runBench(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// ── Collect tasks from corpus ─────────────────────────────────────────────
	tasks, err := collectBenchTasks(benchCorpus)
	if err != nil {
		return fmt.Errorf("collect tasks: %w", err)
	}
	if benchRepo != "" {
		filtered := tasks[:0]
		for _, t := range tasks {
			if t.Repo == benchRepo {
				filtered = append(filtered, t)
			}
		}
		tasks = filtered
	}
	if len(tasks) == 0 {
		return fmt.Errorf("no impact cases found under %s (need kind=node, kind=file, or kind=flow)", benchCorpus)
	}
	found := len(tasks)
	tasks = capPerRepo(tasks, benchMaxPerRepo)
	if len(tasks) < found {
		fmt.Printf("Found %d tasks; capped to %d at --max-per-repo=%d\n", found, len(tasks), benchMaxPerRepo)
	} else {
		fmt.Printf("Found %d tasks across corpus repos\n", len(tasks))
	}

	if benchDryRun {
		for _, t := range tasks {
			fmt.Printf("\nTask: %s\nPrompt: %s\nExpected: %v\n", t.TaskID, t.Prompt, t.Expected)
		}
		return nil
	}

	// ── Determine which arms to run ───────────────────────────────────────────
	arms := []string{agentbench.ArmWithSemantics, agentbench.ArmNoPolyflow}
	if benchArm != "" {
		arms = []string{benchArm}
	}

	// ── Resume anything already bought ────────────────────────────────────────
	//
	// Every trial is appended to a checkpoint the moment it returns, so an
	// interruption of any kind — quota, Ctrl-C, a dropped connection — costs at
	// most the one call in flight. Re-running the same command picks up where it
	// stopped rather than re-paying for the tasks that already succeeded.
	ckpt, err := loadCheckpoint(benchOutput, benchRepo, checkpointHeader{
		Model:  benchModel,
		Corpus: benchCorpus,
		Repo:   benchRepo,
		Arms:   arms,
		Trials: benchTrials,
	}, benchFresh)
	if err != nil {
		return err
	}

	// Say what this run will spend before it spends it. Every invocation is a
	// real paid `claude -p` call, and a run that outlives the session budget
	// aborts partway through with nothing but a partial report to show for it.
	total := len(tasks) * len(arms) * benchTrials
	if resumed := ckpt.count(); resumed > 0 {
		fmt.Printf("Resuming %s: %d of %d trials already recorded, %d left to buy\n",
			ckpt.path, resumed, total, total-resumed)
	} else {
		fmt.Printf("Plan: %d tasks × %d arms × %d trial(s) = %d claude invocations\n",
			len(tasks), len(arms), benchTrials, total)
	}

	// ── Write MCP config for arm 1 ────────────────────────────────────────────
	polyflowBin, err := os.Executable()
	if err != nil {
		polyflowBin = "polyflow"
	}
	mcpCfgPath, cleanup, err := writeMCPConfig(polyflowBin)
	if err != nil {
		return err
	}
	defer cleanup()

	// polyflow-first.py / polyflow-read-gate.py (embedded from
	// cmd/polyflow/hookscripts, the same source this repo's own
	// .claude/settings.json wires for interactive sessions) only fire when
	// the *target* repo's own settings wire them — which none of
	// orion/juniper/etc. do. --settings injects them by absolute path
	// regardless of which repo `claude -p` runs in, so the
	// with_polyflow_semantic arm gets the same grep/Read nudges a local
	// polyflow-repo session already gets for free.
	hookSettingsJSON, hookCleanup, err := writeHookScripts()
	if err != nil {
		return err
	}
	defer hookCleanup()

	// ── Run benchmark ──────────────────────────────────────────────────────────
	//
	// Task-outer, arm-inner. Running arm-outer means the account's budget is
	// spent entirely on arm 1 before arm 2 starts, so a quota stop deletes the
	// *control arm* — which is exactly what happened on 2026-07-30 and made the
	// apparent recall win an artifact. Interleaving the arms of one task makes
	// an early stop drop whole tasks, leaving complete A/B pairs behind.
	var results []agentbench.TaskResult
	var aborted string

	attempted := 0

	for _, task := range tasks {
		if aborted != "" {
			break
		}
		attempted++
		for trial := 1; trial <= benchTrials && aborted == ""; trial++ {
			for _, arm := range arms {
				if prev, ok := ckpt.completed(task.TaskID, arm, trial); ok {
					fmt.Printf("  %s [%s] trial %d ... cached (recall=%.3f)\n",
						task.TaskID, arm, trial, prev.Recall)
					results = append(results, prev)
					continue
				}
				fmt.Printf("  %s [%s] trial %d ... ", task.TaskID, arm, trial)
				r, class := runTrial(ctx, task, arm, mcpCfgPath, hookSettingsJSON, trial)
				results = append(results, r)
				ckpt.record(r)
				if class == agentbench.FailureQuota {
					aborted = "stopped on an API quota/session limit"
					break
				}
			}
		}
	}

	// ── Produce report ────────────────────────────────────────────────────────
	pairing := agentbench.ComputePairing(results)
	report := agentbench.BenchReport{
		RunDate: time.Now().UTC().Format("2006-01-02"),
		Model:   benchModel,
		Tasks:   results,
		Summary: agentbench.Summarize(results),
		Pairing: &pairing,
		Aborted: aborted,
	}
	if hasFlowTask(tasks) {
		fg := agentbench.ComputeFlowGate(results)
		report.FlowGate = &fg
	}

	if err := writeReport(benchOutput, report); err != nil {
		return err
	}
	fmt.Printf("\nReport written to %s/<date>[-repo].{json,md}\n", benchOutput)

	// An incomplete run is not a result. Exit non-zero so a wrapper script or a
	// reader cannot mistake a partial report for the benchmark having run.
	if aborted != "" {
		return fmt.Errorf("benchmark incomplete: %s — %d of %d tasks attempted; "+
			"the partial report is written but is not a measurement. "+
			"Re-run the same command to resume from %s; completed trials will not be re-bought",
			aborted, attempted, len(tasks), ckpt.path)
	}

	// A finished run's checkpoint would otherwise replay itself forever, so the
	// next run of this repo measures rather than reprinting this one.
	ckpt.clear()
	return nil
}

// benchRetries is how many extra attempts a transient failure gets. Quota and
// fatal failures get none: the first retries into the same wall, the second
// retries into the same bad flag.
const benchRetries = 2

// runTrial performs one (task, arm, trial) invocation with classified retries
// and returns the scored result plus the failure class, if any.
func runTrial(ctx context.Context, task benchTask, arm, mcpCfgPath, hookSettingsJSON string, trial int) (agentbench.TaskResult, agentbench.FailureClass) {
	start := time.Now()

	var tr agentbench.Transcript
	var class agentbench.FailureClass
	var detail string
	for attempt := 0; ; attempt++ {
		tr, class, detail = callClaude(ctx, task.Prompt, arm, mcpCfgPath, hookSettingsJSON, benchModel)
		if class == agentbench.FailureNone || !class.Retryable() || attempt >= benchRetries {
			break
		}
		backoff := time.Duration(5*(attempt+1)) * time.Second
		fmt.Printf("transient failure (%s), retrying in %s ... ", detail, backoff)
		time.Sleep(backoff)
	}

	wall := time.Since(start).Milliseconds()
	tr.DurationMs = wall // prefer local wall time over claude's reported value
	r := agentbench.TaskResult{
		TaskID: task.TaskID,
		Repo:   task.Repo,
		CaseID: task.CaseID,
		Kind:   task.Kind,
		Arm:    arm,
		Trial:  trial,
		WallMs: wall,
	}

	if class != agentbench.FailureNone {
		r.ErrorClass = class
		r.Error = detail
		fmt.Printf("ERROR [%s]: %s\n", class, detail)
		return r, class
	}

	r.InputTokens = tr.InputTokens
	r.OutputTokens = tr.OutputTokens
	r.ContextTokens = tr.ContextTokens
	r.NumTurns = tr.NumTurns
	r.TotalCostUSD = tr.TotalCostUSD
	cr := agentbench.ScoreTranscript(task.CaseID, tr, task.Expected, task.MustNotMiss, task.MustNotInclude)
	r.Recall = cr.Recall
	r.Precision = agentbench.TranscriptPrecision(tr, task.Expected)
	r.SilentMisses = cr.SilentMisses
	r.HardFail = cr.HardFail
	r.ExtractedFiles = agentbench.ExtractFiles(tr.Result)
	fmt.Printf("recall=%.3f hard_fail=%v ctx=%d turns=%d out=%d wall=%dms\n",
		r.Recall, r.HardFail, r.ContextTokens, r.NumTurns, r.OutputTokens, r.WallMs)
	return r, agentbench.FailureNone
}

// collectBenchTasks loads impact (node/file kind) eval cases as benchmark tasks.
// Tasks are sorted by (repo, caseID) for determinism (rule 2).
func collectBenchTasks(corpusRoot string) ([]benchTask, error) {
	dirs, err := eval.FindCorpusDirs(corpusRoot)
	if err != nil {
		return nil, err
	}
	var tasks []benchTask
	for _, dir := range dirs {
		m, err := eval.LoadManifest(dir)
		if err != nil {
			continue
		}
		for _, c := range m.Cases {
			switch c.Kind {
			case "node", "file", "flow", "feature_add", "test_impact", "regression":
			default:
				continue // skip semantic/diff cases — they use a different prompt pattern
			}
			t := benchTask{
				TaskID:         m.Repo.Name + "/" + c.ID,
				Repo:           m.Repo.Name,
				CaseID:         c.ID,
				Kind:           c.Kind,
				Expected:       c.ExpectedImpacted,
				MustNotMiss:    c.MustNotMiss,
				MustNotInclude: c.MustNotInclude,
			}
			switch c.Kind {
			case "node":
				t.Prompt = fmt.Sprintf(
					"I need to change %s in the %s codebase — what else do I need to touch? "+
						"List each file path on its own line.",
					c.Target, m.Repo.Name)
			case "file":
				t.Prompt = fmt.Sprintf(
					"I need to change the file %s in the %s codebase — what else do I need to "+
						"touch? List each file path on its own line.",
					c.Target, m.Repo.Name)
			case "flow":
				t.Prompt = fmt.Sprintf(
					"Walk me through what happens when %s in the %s codebase — which files are "+
						"involved, and in what order? List each file involved on its own line, "+
						"in the order they participate.",
					c.Target, m.Repo.Name)
			case "feature_add":
				t.Prompt = fmt.Sprintf(
					"I want to add %s alongside the existing %s functionality in the %s "+
						"codebase. Which existing files do I need to read or extend to do "+
						"this well? List each file path on its own line.",
					c.NewCapability, c.Target, m.Repo.Name)
			case "test_impact":
				t.Prompt = fmt.Sprintf(
					"I changed %s in the %s codebase. Which test files should CI run to "+
						"cover this change? List each test file path on its own line.",
					c.Target, m.Repo.Name)
			case "regression":
				t.Prompt = fmt.Sprintf(
					"I'm about to change %s in the %s codebase. Does that risk breaking "+
						"%s? Answer yes or no, then list the file paths that justify your "+
						"answer, one per line.",
					c.Target, m.Repo.Name, c.RegressionSubject)
			}
			tasks = append(tasks, t)
		}
	}
	return tasks, nil
}

// capPerRepo limits how many tasks each corpus repo contributes, preserving the
// deterministic (repo, caseID) order collectBenchTasks produced. A cap of 0
// means no cap.
//
// The protocol has documented a 10-per-repo cap since P.1, but nothing
// enforced it: the full corpus is 186 tasks, so the default run was 372 paid
// invocations — far past any session's budget, and therefore guaranteed to
// abort partway through.
func capPerRepo(tasks []benchTask, max int) []benchTask {
	if max <= 0 {
		return tasks
	}
	seen := make(map[string]int)
	out := tasks[:0:0]
	for _, t := range tasks {
		if seen[t.Repo] >= max {
			continue
		}
		seen[t.Repo]++
		out = append(out, t)
	}
	return out
}

// hasFlowTask reports whether any collected task is kind=flow, gating whether
// a FlowGate is worth computing/reporting for this run.
func hasFlowTask(tasks []benchTask) bool {
	for _, t := range tasks {
		if t.Kind == "flow" {
			return true
		}
	}
	return false
}

// writeMCPConfig writes a temporary MCP config JSON for arms 1 and 2.
// Returns the path and a cleanup function.
func writeMCPConfig(polyflowBin string) (string, func(), error) {
	cfg := map[string]interface{}{
		"mcpServers": map[string]interface{}{
			"polyflow": map[string]interface{}{
				"command": polyflowBin,
				"args":    []string{"mcp"},
				"env":     map[string]string{},
			},
		},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	f, err := os.CreateTemp("", "polyflow-mcp-bench-*.json")
	if err != nil {
		return "", func() {}, fmt.Errorf("write mcp config: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", func() {}, err
	}
	f.Close()
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}

// writeHookScripts stages the embedded polyflow-first.py / polyflow-read-gate.py
// hooks to temp files and returns a --settings JSON blob wiring them as
// PreToolUse hooks for Bash and Read — the same pair this repo's own
// .claude/settings.json wires for interactive sessions, but portable to
// whatever repo the with_polyflow_semantic arm's `claude -p` runs in. Without
// this, the hooks are dead weight outside this checkout: Claude Code loads
// PreToolUse hooks from the *target* repo's own settings, and orion/
// juniper/etc. don't carry a copy.
func writeHookScripts() (string, func(), error) {
	grepHook, err := os.CreateTemp("", "polyflow-hook-grep-*.py")
	if err != nil {
		return "", func() {}, fmt.Errorf("write grep-nudge hook: %w", err)
	}
	if _, err := grepHook.Write(hookScriptGrepNudge); err != nil {
		grepHook.Close()
		os.Remove(grepHook.Name())
		return "", func() {}, err
	}
	grepHook.Close()

	readHook, err := os.CreateTemp("", "polyflow-hook-read-*.py")
	if err != nil {
		os.Remove(grepHook.Name())
		return "", func() {}, fmt.Errorf("write read-gate hook: %w", err)
	}
	if _, err := readHook.Write(hookScriptReadGate); err != nil {
		readHook.Close()
		os.Remove(grepHook.Name())
		os.Remove(readHook.Name())
		return "", func() {}, err
	}
	readHook.Close()

	cleanup := func() {
		os.Remove(grepHook.Name())
		os.Remove(readHook.Name())
	}

	settings := map[string]interface{}{
		"hooks": map[string]interface{}{
			"PreToolUse": []map[string]interface{}{
				{
					"matcher": "Bash",
					"hooks": []map[string]interface{}{
						{"type": "command", "command": "python3 " + grepHook.Name()},
					},
				},
				{
					"matcher": "Read",
					"hooks": []map[string]interface{}{
						{"type": "command", "command": "python3 " + readHook.Name()},
					},
				},
			},
		},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("marshal hook settings: %w", err)
	}
	return string(data), cleanup, nil
}

// polyflowNudge tells the with_polyflow_semantic arm to reach for the MCP
// server directly. On its own this did not stop the agent from delegating to
// an Explore subagent — the system prompt does not propagate to a spawned
// subagent's own instructions, so the subagent still defaulted to Bash/Read
// and never called mcp__polyflow__*. --disallowedTools Agent (below) is what
// actually forces the top-level agent to use its own tools; this nudge just
// steers which ones it reaches for once it can't delegate.
const polyflowNudge = "A `polyflow` MCP server is registered for this session. " +
	"For questions about code structure, call graph, or blast radius " +
	"(\"what would need to change\", \"trace this flow\", \"what calls this\"), " +
	"call the polyflow MCP tools (search, node lookup, flows, impact) directly " +
	"instead of searching with Bash/Grep/Read."

// callClaude invokes `claude -p --output-format json` and returns the parsed
// transcript, or the class and detail of the failure.
//
// The CLI writes a well-formed result envelope to stdout even when it exits 1,
// so the reason for a failure is read structurally out of that envelope rather
// than scraped from an error string.
func callClaude(_ context.Context, prompt, arm, mcpCfgPath, hookSettingsJSON, model string) (agentbench.Transcript, agentbench.FailureClass, string) {
	claudeArgs := []string{
		"-p", prompt,
		"--output-format", "json",
		"--model", model,
		"--disallowedTools", "Agent",
		// Headless -p mode has no one to answer a permission prompt: without
		// this, mcp__polyflow__* and even Bash return "requires approval" and
		// the agent gives up with an empty answer (recall 0, hard_fail true)
		// instead of ever running the query. Scoped to this benchmark's own
		// calls only, and already narrowed by --strict-mcp-config (polyflow
		// only) and --disallowedTools Agent above. PreToolUse hooks (below)
		// run independently of this flag — it only skips the interactive
		// approval prompt, not hook evaluation.
		"--dangerously-skip-permissions",
	}
	switch arm {
	case agentbench.ArmWithSemantics:
		claudeArgs = append(claudeArgs, "--mcp-config", mcpCfgPath, "--strict-mcp-config",
			"--append-system-prompt", polyflowNudge,
			"--settings", hookSettingsJSON)
	case agentbench.ArmNoPolyflow:
		claudeArgs = append(claudeArgs, "--strict-mcp-config")
	}

	out, err := exec.Command("claude", claudeArgs...).Output()

	class, detail := agentbench.ClassifyFailure(out, err)
	if class != agentbench.FailureNone {
		if detail == "" {
			detail = "claude failed with no reported reason"
		}
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			detail += "; stderr: " + strings.TrimSpace(string(ee.Stderr))
		}
		return agentbench.Transcript{}, class, truncate(detail, 500)
	}

	tr, perr := agentbench.ParseTranscript(out)
	if perr != nil {
		return agentbench.Transcript{}, agentbench.FailureTransient, perr.Error()
	}
	return tr, agentbench.FailureNone, ""
}

// writeReport writes JSON and markdown files to outDir/<date>.{json,md}.
func writeReport(outDir string, r agentbench.BenchReport) error {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	name := r.RunDate
	if benchRepo != "" {
		name += "-" + benchRepo
	}
	base := filepath.Join(outDir, name)

	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(base+".json", data, 0644); err != nil {
		return err
	}
	md := agentbench.FormatMarkdown(r)
	if err := os.WriteFile(base+".md", []byte(md), 0644); err != nil {
		return err
	}
	// Print summary to stdout.
	fmt.Println()
	fmt.Println(strings.TrimRight(agentbench.FormatMarkdown(r), "\n"))
	return nil
}
