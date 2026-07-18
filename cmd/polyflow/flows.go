package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/evidence/trace_ingest"
)

var (
	flowsSession  string
	flowsFormat   string
	flowsCoverage bool
)

var flowsCmd = &cobra.Command{
	Use:   "flows [<file>]",
	Short: "Debug view: spans parsed from an OTLP dump or capture session",
	Long: `Print the spans parsed from a trace dump or capture session.
Add --coverage to compare what the session observed against the indexed static
edge baseline (requires a prior 'polyflow index' run).

Spans are sorted by (trace_id, start_time, span_id) — deterministic output.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runFlows,
}

func init() {
	flowsCmd.Flags().StringVar(&flowsSession, "session", "", "read from a named capture session")
	flowsCmd.Flags().StringVar(&flowsFormat, "format", "text", "output format: text or json")
	flowsCmd.Flags().BoolVar(&flowsCoverage, "coverage", false, "show channel coverage against the indexed static edge baseline")
	rootCmd.AddCommand(flowsCmd)
}

func runFlows(cmd *cobra.Command, args []string) error {
	if len(args) > 0 && flowsSession != "" {
		return fmt.Errorf("flows: provide either a file argument or --session, not both")
	}

	var spans []trace_ingest.Span
	var err error
	sessionLabel := "(file)"

	switch {
	case len(args) > 0:
		spans, err = trace_ingest.ParseOTLPFile(args[0])
		if err != nil {
			return fmt.Errorf("flows: parse %s: %w", args[0], err)
		}
	case flowsSession != "":
		sessionLabel = flowsSession
		spansFile := filepath.Join(capturesBase(), flowsSession, "spans.otlp.json")
		spans, err = trace_ingest.ReadSessionSpans(spansFile)
		if err != nil {
			return fmt.Errorf("flows: read session %q: %w", flowsSession, err)
		}
	default:
		return fmt.Errorf("flows: provide a file argument or --session <name>")
	}

	if flowsCoverage {
		return runFlowsCoverage(spans, sessionLabel)
	}

	switch flowsFormat {
	case "json":
		return printFlowsJSON(spans, sessionLabel)
	default:
		return printFlowsText(spans, sessionLabel)
	}
}

func runFlowsCoverage(spans []trace_ingest.Span, sessionLabel string) error {
	flows, ledger := trace_ingest.MapSpans(spans, sessionLabel, nil)

	store, err := openStore()
	if err != nil {
		// No index: show session-only counts without a static baseline.
		fmt.Fprintf(os.Stderr, "Note: no index found (run 'polyflow index' for %% coverage vs static baseline)\n\n")
		printSessionOnlyCoverage(flows, ledger, sessionLabel)
		return nil
	}
	defer store.Close()

	idx, err := store.BuildIndex(context.Background())
	if err != nil {
		return fmt.Errorf("flows --coverage: build index: %w", err)
	}

	edges := trace_ingest.RuntimeCoverageEdges(idx.AllEdges())
	report := trace_ingest.ComputeSessionCoverage(flows, edges, ledger)
	printCoverageReport(report, "Coverage for session \""+sessionLabel+"\"")
	return nil
}

func printSessionOnlyCoverage(flows []trace_ingest.FlowRecord, ledger []trace_ingest.IngestLedgerEntry, label string) {
	fmt.Printf("Session \"%s\" — flow records (%d):\n", label, len(flows))
	byKind := make(map[string]int)
	var kinds []string
	for _, f := range flows {
		k := string(f.Kind)
		if byKind[k] == 0 {
			kinds = append(kinds, k)
		}
		byKind[k]++
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Printf("  %-16s %d\n", k, byKind[k])
	}
	fmt.Printf("\nIngest ledger (%d):\n", len(ledger))
	byReason := make(map[string]int)
	var reasons []string
	for _, l := range ledger {
		if byReason[l.Reason] == 0 {
			reasons = append(reasons, l.Reason)
		}
		byReason[l.Reason]++
	}
	sort.Strings(reasons)
	for _, r := range reasons {
		fmt.Printf("  %s: %d\n", r, byReason[r])
	}
}

// printCoverageReport prints a coverage report in human-readable form.
// header is printed as the section title (e.g. "Coverage for session X").
func printCoverageReport(r trace_ingest.CoverageReport, header string) {
	fmt.Printf("%s:\n", header)
	if len(r.Rows) == 0 && r.GapChannels == 0 {
		fmt.Println("  (no edges in index)")
		return
	}
	fmt.Printf("  %-18s  %5s  %8s  %9s  %3s  %6s\n", "kind", "total", "verified", "candidate", "gap", "%")
	for _, row := range r.Rows {
		pctStr := fmt.Sprintf("%.1f%%", row.Pct)
		if row.Total == 0 {
			pctStr = "n/a"
		}
		fmt.Printf("  %-18s  %5d  %8d  %9d  %3d  %6s\n",
			row.Kind, row.Total, row.Verified, row.Candidate, row.Gap, pctStr)
	}
	fmt.Println("  " + repeatStr("─", 60))
	totalPct := "n/a"
	if r.TotalChannels > 0 {
		totalPct = fmt.Sprintf("%.1f%%", float64(r.VerifiedChannels)/float64(r.TotalChannels)*100)
	}
	fmt.Printf("  %-18s  %5d  %8d  %9d  %3d  %6s\n",
		"total", r.TotalChannels, r.VerifiedChannels, r.CandidateChannels, r.GapChannels, totalPct)

	if len(r.LedgerByReason) > 0 {
		fmt.Printf("\n  Ingest ledger:\n")
		// Sort reasons for determinism.
		reasons := make([]string, 0, len(r.LedgerByReason))
		for reason := range r.LedgerByReason {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		for _, reason := range reasons {
			fmt.Printf("    %s: %d\n", reason, r.LedgerByReason[reason])
		}
	}

	if len(r.ObservedOnlyGaps) > 0 {
		fmt.Printf("\n  Observed-only gaps (%d):\n", len(r.ObservedOnlyGaps))
		for _, g := range r.ObservedOnlyGaps {
			fmt.Printf("    %-16s  %-30s  %s → %s\n", g.Kind, g.Key, g.From, g.To)
		}
	}
}

func repeatStr(s string, n int) string {
	result := ""
	for i := 0; i < n; i++ {
		result += s
	}
	return result
}

// flowsOutput is the stable JSON shape for `flows --format json` (used by
// the two-run determinism tests — byte-identical across runs).
type flowsOutput struct {
	Spans   []trace_ingest.Span            `json:"spans"`
	Records []trace_ingest.FlowRecord      `json:"flow_records"`
	Ledger  []trace_ingest.IngestLedgerEntry `json:"ledger"`
}

func printFlowsJSON(spans []trace_ingest.Span, sessionLabel string) error {
	flows, ledger := trace_ingest.MapSpans(spans, sessionLabel, nil)
	out := flowsOutput{
		Spans:   spans,
		Records: flows,
		Ledger:  ledger,
	}
	if out.Spans == nil {
		out.Spans = []trace_ingest.Span{}
	}
	if out.Records == nil {
		out.Records = []trace_ingest.FlowRecord{}
	}
	if out.Ledger == nil {
		out.Ledger = []trace_ingest.IngestLedgerEntry{}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func printFlowsText(spans []trace_ingest.Span, sessionLabel string) error {
	flows, ledger := trace_ingest.MapSpans(spans, sessionLabel, nil)

	fmt.Printf("Spans (%d):\n", len(spans))
	for _, s := range spans {
		parent := ""
		if s.ParentSpanID != "" {
			parent = fmt.Sprintf(" parent=%s", s.ParentSpanID)
		}
		fmt.Printf("  trace=%-34s span=%-18s svc=%-20s kind=%-10s %s%s\n",
			s.TraceID, s.SpanID, s.Service, s.Kind, s.Name, parent)
	}
	fmt.Println()

	fmt.Printf("Flow records (%d):\n", len(flows))
	for _, f := range flows {
		fmt.Printf("  kind=%-8s key=%-30s from=%-15s to=%-15s causality=%s refs=%d\n",
			string(f.Kind), f.Key, f.FromService, f.ToService, f.Causality, len(f.Refs))
	}

	fmt.Printf("\nIngest ledger (%d):\n", len(ledger))
	for _, e := range ledger {
		fmt.Printf("  session=%-20s trace=%-34s span=%-18s reason=%s\n",
			e.Session, e.TraceID, e.SpanID, e.Reason)
	}
	return nil
}
