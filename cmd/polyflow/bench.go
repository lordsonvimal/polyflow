package main

import (
	"context"
	"database/sql"
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
	"github.com/lordsonvimal/polyflow/internal/meta"
	_ "modernc.org/sqlite"
)

var (
	benchCorpus string
	benchModel  string
	benchTrials int
	benchArm    string
	benchRepo   string
	benchOutput string
	benchDryRun bool
)

var benchCmd = &cobra.Command{
	Use:   "bench",
	Short: "Run the P.1 agent outcome benchmark (manual-triggered; costs real tokens)",
	Long: `Run the agent outcome benchmark across three arms:
  1. with_polyflow_semantic — polyflow MCP + vector search active
  2. with_polyflow_fts_only — polyflow MCP, embeddings skipped (FTS only)
  3. without_polyflow        — no MCP; agent answers without the graph

Tasks are drawn from the eval corpus impact cases. Results are written to
eval/agent-bench/results/<date>.json and eval/agent-bench/results/<date>.md.

This command is MANUAL-TRIGGERED — each run costs real tokens and is never run in CI.`,
	RunE: runBench,
}

func init() {
	benchCmd.Flags().StringVar(&benchCorpus, "corpus", "eval/corpus", "path to eval corpus root")
	benchCmd.Flags().StringVar(&benchModel, "model", "claude-sonnet-4-6", "claude model to use")
	benchCmd.Flags().IntVar(&benchTrials, "trials", 1, "trials per task/arm")
	benchCmd.Flags().StringVar(&benchArm, "arm", "", "run only this arm (leave empty for all three)")
	benchCmd.Flags().StringVar(&benchRepo, "repo", "", "filter tasks to this corpus repo name (e.g. polyflow)")
	benchCmd.Flags().StringVar(&benchOutput, "output", "eval/agent-bench/results", "directory for result files")
	benchCmd.Flags().BoolVar(&benchDryRun, "dry-run", false, "print tasks and prompts without calling claude")
}

// benchTask is one task in the benchmark, derived from an eval corpus case.
type benchTask struct {
	TaskID      string
	Repo        string
	CaseID      string
	Prompt      string
	Expected    []string
	MustNotMiss []string
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
		return fmt.Errorf("no impact cases found under %s (need kind=node or kind=file)", benchCorpus)
	}
	fmt.Printf("Found %d tasks across corpus repos\n", len(tasks))

	if benchDryRun {
		for _, t := range tasks {
			fmt.Printf("\nTask: %s\nPrompt: %s\nExpected: %v\n", t.TaskID, t.Prompt, t.Expected)
		}
		return nil
	}

	// ── Determine which arms to run ───────────────────────────────────────────
	arms := []string{agentbench.ArmWithSemantics, agentbench.ArmFTSOnly, agentbench.ArmNoPolyflow}
	if benchArm != "" {
		arms = []string{benchArm}
	}

	// ── Write MCP config for arms 1 and 2 ────────────────────────────────────
	polyflowBin, err := os.Executable()
	if err != nil {
		polyflowBin = "polyflow"
	}
	mcpCfgPath, cleanup, err := writeMCPConfig(polyflowBin)
	if err != nil {
		return err
	}
	defer cleanup()

	// ── Run benchmark ──────────────────────────────────────────────────────────
	var results []agentbench.TaskResult

	for _, arm := range arms {
		armLabel := arm
		fmt.Printf("\n=== Arm: %s ===\n", armLabel)

		var restoreEmbed func()
		if arm == agentbench.ArmFTSOnly {
			var ferr error
			restoreEmbed, ferr = forceEmbedOff(ctx)
			if ferr != nil {
				fmt.Printf("  [WARN] could not set embed_status for FTS-only arm: %v\n", ferr)
				restoreEmbed = func() {}
			}
		}

		for _, task := range tasks {
			for trial := 1; trial <= benchTrials; trial++ {
				fmt.Printf("  %s trial %d ... ", task.TaskID, trial)
				start := time.Now()
				tr, err := callClaude(ctx, task.Prompt, arm, mcpCfgPath, benchModel)
				wall := time.Since(start).Milliseconds()

				tr.DurationMs = wall // prefer local wall time over claude's reported value
				r := agentbench.TaskResult{
					TaskID:  task.TaskID,
					Repo:    task.Repo,
					CaseID:  task.CaseID,
					Arm:     arm,
					Trial:   trial,
					WallMs:  wall,
				}
				if err != nil {
					r.Error = err.Error()
					fmt.Printf("ERROR: %v\n", err)
				} else {
					r.InputTokens = tr.InputTokens
					r.OutputTokens = tr.OutputTokens
					r.ContextTokens = tr.ContextTokens
					r.NumTurns = tr.NumTurns
					r.TotalCostUSD = tr.TotalCostUSD
					cr := agentbench.ScoreTranscript(task.CaseID, tr, task.Expected, task.MustNotMiss)
					r.Recall = cr.Recall
					r.Precision = cr.Precision
					r.SilentMisses = cr.SilentMisses
					r.HardFail = cr.HardFail
					r.ExtractedFiles = agentbench.ExtractFiles(tr.Result)
					fmt.Printf("recall=%.3f hard_fail=%v ctx=%d turns=%d out=%d wall=%dms\n",
						r.Recall, r.HardFail, r.ContextTokens, r.NumTurns, r.OutputTokens, r.WallMs)
				}
				results = append(results, r)
			}
		}

		if restoreEmbed != nil {
			restoreEmbed()
		}
	}

	// ── Produce report ────────────────────────────────────────────────────────
	report := agentbench.BenchReport{
		RunDate: time.Now().UTC().Format("2006-01-02"),
		Model:   benchModel,
		Tasks:   results,
		Summary: agentbench.Summarize(results),
	}

	if err := writeReport(benchOutput, report); err != nil {
		return err
	}
	fmt.Printf("\nReport written to %s/<date>.{json,md}\n", benchOutput)
	return nil
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
			if c.Kind != "node" && c.Kind != "file" {
				continue // skip semantic cases — they use a different prompt pattern
			}
			t := benchTask{
				TaskID:      m.Repo.Name + "/" + c.ID,
				Repo:        m.Repo.Name,
				CaseID:      c.ID,
				Expected:    c.ExpectedImpacted,
				MustNotMiss: c.MustNotMiss,
			}
			if c.Kind == "node" {
				t.Prompt = fmt.Sprintf(
					"In the %s codebase, if %s were modified or renamed, which source files "+
						"would need to be updated as a result? List each file path on its own line.",
					m.Repo.Name, c.Target)
			} else {
				t.Prompt = fmt.Sprintf(
					"In the %s codebase, if the file %s were modified, which other source files "+
						"would be affected? List each file path on its own line.",
					m.Repo.Name, c.Target)
			}
			tasks = append(tasks, t)
		}
	}
	return tasks, nil
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

// forceEmbedOff temporarily sets embed_status to the FTS-only degradation value
// in the local graph DB and returns a restore function.
func forceEmbedOff(ctx context.Context) (func(), error) {
	dbPath := filepath.Join(meta.DBDir, meta.DBFile)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return func() {}, err
	}
	var prev string
	_ = db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'embed_status'`).Scan(&prev)
	const off = "unavailable: embeddings skipped"
	if _, err := db.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES('embed_status',?)`, off); err != nil {
		db.Close()
		return func() {}, err
	}
	return func() {
		if prev == "" {
			_, _ = db.ExecContext(ctx, `DELETE FROM meta WHERE key = 'embed_status'`)
		} else {
			_, _ = db.ExecContext(ctx, `INSERT OR REPLACE INTO meta(key,value) VALUES('embed_status',?)`, prev)
		}
		db.Close()
	}, nil
}

// callClaude invokes `claude -p --output-format json` and returns the parsed transcript.
func callClaude(_ context.Context, prompt, arm, mcpCfgPath, model string) (agentbench.Transcript, error) {
	claudeArgs := []string{
		"-p", prompt,
		"--output-format", "json",
		"--model", model,
	}
	switch arm {
	case agentbench.ArmWithSemantics, agentbench.ArmFTSOnly:
		claudeArgs = append(claudeArgs, "--mcp-config", mcpCfgPath, "--strict-mcp-config")
	case agentbench.ArmNoPolyflow:
		claudeArgs = append(claudeArgs, "--strict-mcp-config")
	}

	out, err := exec.Command("claude", claudeArgs...).Output()
	if err != nil {
		// Include captured stderr when available.
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return agentbench.Transcript{}, fmt.Errorf("claude: %w\nstderr: %s", err, ee.Stderr)
		}
		return agentbench.Transcript{}, fmt.Errorf("claude: %w", err)
	}
	return agentbench.ParseTranscript(out)
}

// writeReport writes JSON and markdown files to outDir/<date>.{json,md}.
func writeReport(outDir string, r agentbench.BenchReport) error {
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return err
	}
	base := filepath.Join(outDir, r.RunDate)

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
