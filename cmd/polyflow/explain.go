package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/graph"
)

var explainFormat string

var explainCmd = &cobra.Command{
	Use:   "explain <edge-id>",
	Short: "Show why a graph edge exists — its evidence sources, layer/rule chain, and ledger rows",
	Long: `explain reads the already-indexed graph and prints the full provenance of one
edge (SA.1): every evidence SourceRef, the static layer + rule that minted it,
and any unresolved-ref ("ledger") rows recorded at the same source site.

The edge ID is the one shown by 'polyflow status --unknown-edges', the MCP
unknown_edges / trace tools, or a graph dump.`,
	Args: cobra.ExactArgs(1),
	RunE: runExplain,
}

func init() {
	explainCmd.Flags().StringVar(&explainFormat, "format", "text", "output format: text or json")
}

func runExplain(cmd *cobra.Command, args []string) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	defer store.Close()

	idx, err := store.BuildIndex(cmd.Context())
	if err != nil {
		return fmt.Errorf("build index: %w", err)
	}
	ledger, err := store.ListUnresolvedRefs(cmd.Context())
	if err != nil {
		return fmt.Errorf("list unresolved refs: %w", err)
	}

	ex, ok := graph.ExplainEdge(idx, ledger, args[0])
	if !ok {
		return fmt.Errorf("no edge with id %q in the graph", args[0])
	}

	if explainFormat == "json" {
		return json.NewEncoder(os.Stdout).Encode(ex)
	}
	printExplanation(ex)
	return nil
}

func printExplanation(ex *graph.EdgeExplanation) {
	e := ex.Edge
	fmt.Printf("edge %s\n", e.ID)
	fmt.Printf("  %s --%s--> %s\n", nodeLabel(ex.From, e.From), e.Type, nodeLabel(ex.To, e.To))
	if e.Label != "" {
		fmt.Printf("  label:      %s\n", e.Label)
	}
	if e.Method != "" || e.Path != "" {
		fmt.Printf("  http:       %s %s\n", e.Method, e.Path)
	}
	fmt.Printf("  confidence: %s\n", e.Confidence)
	if e.VerificationState != "" {
		fmt.Printf("  state:      %s\n", e.VerificationState)
	}
	if ex.From != nil && ex.From.File != "" {
		fmt.Printf("  site:       %s:%d\n", ex.From.File, ex.From.Line)
	}

	fmt.Printf("\n  rule chain:\n")
	if len(ex.RuleChain) == 0 {
		fmt.Printf("    (none — edge carries no layer/rule provenance)\n")
	}
	for _, r := range ex.RuleChain {
		layer := r.Layer
		if layer == "" {
			layer = "--"
		}
		fmt.Printf("    [%s] %-10s %s\n", layer, r.Provider, r.Rule)
	}

	fmt.Printf("\n  sources (%d):\n", len(ex.Sources))
	for _, s := range ex.Sources {
		fmt.Printf("    provider=%-8s confidence=%-10s layer=%-3s rule=%s\n",
			s.Provider, s.Confidence, dash(s.Layer), s.Rule)
		if s.Ref != "" {
			fmt.Printf("      ref: %s\n", s.Ref)
		}
	}

	if len(ex.LedgerRows) > 0 {
		fmt.Printf("\n  ledger rows at this site (%d):\n", len(ex.LedgerRows))
		for _, u := range ex.LedgerRows {
			fmt.Printf("    %-28s %s (line %d)\n", u.Kind, u.Name, u.Line)
		}
	}
}

func nodeLabel(n *graph.Node, fallback string) string {
	if n != nil && n.Label != "" {
		return n.Label
	}
	return fallback
}

func dash(s string) string {
	if s == "" {
		return "--"
	}
	return s
}
