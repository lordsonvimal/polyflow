package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/evidence"
)

var (
	reconcileFormat    string
	reconcilePropose   string
	reconcileListCands bool
	reconcileListGaps  bool
)

var reconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Show evidence-fusion coverage report (verified / candidate / gap / conflicting)",
	Long: `reconcile reads the already-indexed graph and summarises the fusion of all
evidence providers (static, contract, runtime, config).

Outputs:
  - % verified edges per kind (static ∩ confirming evidence)
  - candidate list (static-only, unconfirmed)
  - observed_only_gap list (runtime/contract saw channels static missed)
  - conflicting edges (gap on an otherwise-verified channel)

Use --propose-dir to emit candidate contract rule YAML files for each gap channel.
The files are sorted and named from the channel key — never a counter.`,
	RunE: runReconcile,
}

func init() {
	reconcileCmd.Flags().StringVar(&reconcileFormat, "format", "text", "output format: text or json")
	reconcileCmd.Flags().StringVar(&reconcilePropose, "propose-dir", "", "write proposed contract YAML files to this directory (one per gap channel)")
	reconcileCmd.Flags().BoolVar(&reconcileListCands, "list-candidates", false, "print the full candidate (static-only) list")
	reconcileCmd.Flags().BoolVar(&reconcileListGaps, "list-gaps", false, "print the full observed_only_gap list")
}

func runReconcile(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	idx, err := store.BuildIndex(cmd.Context())
	if err != nil {
		return fmt.Errorf("build index: %w", err)
	}

	edges := idx.AllEdges()
	report := evidence.BuildReport(edges)

	if reconcileFormat == "json" {
		return json.NewEncoder(os.Stdout).Encode(report)
	}

	printReconcileReport(report)

	if reconcileListCands && len(report.CandidateList) > 0 {
		fmt.Println()
		fmt.Printf("  Candidate edges (%d — static-only, no confirming evidence):\n", len(report.CandidateList))
		for _, e := range report.CandidateList {
			fmt.Printf("    %-18s  %-40s  %s → %s\n", e.Kind, truncate(e.Key, 40), e.From, e.To)
		}
	}

	if reconcileListGaps && len(report.GapList) > 0 {
		fmt.Println()
		fmt.Printf("  Observed-only gaps (%d — channel seen, not in static graph):\n", len(report.GapList))
		for _, e := range report.GapList {
			fmt.Printf("    %-18s  %-40s  %s → %s  [%s]\n",
				e.Kind, truncate(e.Key, 40), e.From, e.To, joinStrings(e.Sources))
		}
	}

	if reconcilePropose != "" {
		if err := emitProposals(report.GapList, reconcilePropose); err != nil {
			return err
		}
	}

	return nil
}

func printReconcileReport(r evidence.ReconcileReport) {
	pctStr := "n/a"
	if r.TotalEdges > 0 {
		pctStr = fmt.Sprintf("%.1f%%", r.VerifiedPct)
	}
	fmt.Printf("  Fusion coverage:  %s verified  (%d/%d edges)  candidate=%d  gap=%d  conflicting=%d\n",
		pctStr, r.VerifiedEdges, r.TotalEdges, r.CandidateEdges, r.GapEdges, r.ConflictingEdges)
	fmt.Println()

	if len(r.ByKind) == 0 {
		fmt.Println("  No cross-service edges in graph (run 'polyflow index' first).")
		return
	}

	fmt.Printf("  %-20s  %6s  %8s  %9s  %3s  %5s  %6s\n",
		"kind", "total", "verified", "candidate", "gap", "conf", "%")
	for _, row := range r.ByKind {
		pct := "n/a"
		if row.Total > 0 {
			pct = fmt.Sprintf("%.1f%%", row.Pct)
		}
		fmt.Printf("  %-20s  %6d  %8d  %9d  %3d  %5d  %6s\n",
			row.Kind, row.Total, row.Verified, row.Candidate, row.Gap, row.Conflicting, pct)
	}

	if len(r.ConflictingList) > 0 {
		fmt.Println()
		fmt.Printf("  Conflicting edges (%d — gap on an otherwise-verified channel):\n", len(r.ConflictingList))
		for _, e := range r.ConflictingList {
			fmt.Printf("    %-18s  %-40s  %s → %s\n", e.Kind, truncate(e.Key, 40), e.From, e.To)
		}
	}

	if r.GapEdges > 0 {
		fmt.Println()
		fmt.Printf("  %d observed-only gap channel(s) not in static graph.\n", r.GapEdges)
		fmt.Println("  Run with --list-gaps to see them, or --propose-dir to generate candidate rules.")
	}
}

func emitProposals(gaps []evidence.EdgeSummary, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create propose-dir %s: %w", dir, err)
	}
	proposals := evidence.ProposeRules(gaps)
	for _, p := range proposals {
		path := filepath.Join(dir, p.Filename)
		if err := os.WriteFile(path, []byte(p.Content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		fmt.Printf("  Proposed: %s\n", path)
	}
	if len(proposals) > 0 {
		fmt.Printf("  %d candidate rule file(s) written to %s\n", len(proposals), dir)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func joinStrings(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	out := ss[0]
	for _, s := range ss[1:] {
		out += "," + s
	}
	return out
}
