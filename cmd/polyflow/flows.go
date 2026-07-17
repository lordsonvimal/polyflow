package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/evidence/trace_ingest"
)

var (
	flowsSession string
	flowsFormat  string
)

var flowsCmd = &cobra.Command{
	Use:   "flows [<file>]",
	Short: "Debug view: spans parsed from an OTLP dump or capture session",
	Long: `Print the spans parsed from a trace dump or capture session.
Flow records (client→server pairs, channel keys) are empty until R.1 lands;
this command currently shows spans + ingest ledger for diagnosing parse issues.

Spans are sorted by (trace_id, start_time, span_id) — deterministic output.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runFlows,
}

func init() {
	flowsCmd.Flags().StringVar(&flowsSession, "session", "", "read from a named capture session")
	flowsCmd.Flags().StringVar(&flowsFormat, "format", "text", "output format: text or json")
	rootCmd.AddCommand(flowsCmd)
}

func runFlows(cmd *cobra.Command, args []string) error {
	if len(args) > 0 && flowsSession != "" {
		return fmt.Errorf("flows: provide either a file argument or --session, not both")
	}

	var spans []trace_ingest.Span
	var err error

	switch {
	case len(args) > 0:
		spans, err = trace_ingest.ParseOTLPFile(args[0])
		if err != nil {
			return fmt.Errorf("flows: parse %s: %w", args[0], err)
		}
	case flowsSession != "":
		spansFile := filepath.Join(capturesBase(), flowsSession, "spans.otlp.json")
		spans, err = trace_ingest.ReadSessionSpans(spansFile)
		if err != nil {
			return fmt.Errorf("flows: read session %q: %w", flowsSession, err)
		}
	default:
		return fmt.Errorf("flows: provide a file argument or --session <name>")
	}

	switch flowsFormat {
	case "json":
		return printFlowsJSON(spans)
	default:
		return printFlowsText(spans)
	}
}

// flowsOutput is the stable JSON shape for `flows --format json` (used by
// the two-run determinism tests — byte-identical across runs).
type flowsOutput struct {
	Spans   []trace_ingest.Span            `json:"spans"`
	Records []trace_ingest.FlowRecord      `json:"flow_records"`
	Ledger  []trace_ingest.IngestLedgerEntry `json:"ledger"`
}

func printFlowsJSON(spans []trace_ingest.Span) error {
	flows, ledger := trace_ingest.MapSpans(spans, "(file)", nil)
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

func printFlowsText(spans []trace_ingest.Span) error {
	flows, ledger := trace_ingest.MapSpans(spans, "(file)", nil)

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
