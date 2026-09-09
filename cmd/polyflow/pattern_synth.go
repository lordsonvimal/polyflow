package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lordsonvimal/polyflow/internal/patternsynth"
)

// Tier PS (SA.4): `polyflow pattern synth` proposes L1 code patterns from a
// corpus, validates each candidate against the grammar and the corpus, and
// writes only the survivors. The gate is the point — see internal/patternsynth.

var (
	synthPackage      string
	synthCorpus       string
	synthLanguage     string
	synthName         string
	synthOut          string
	synthDryRun       bool
	synthAgainst      string
	synthMaxExtra     float64
	synthFanoutCap    int
	synthMinPrecision float64
	synthLabels       string
	synthJSON         bool
)

func initPatternSynthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "synth",
		Short: "Synthesize L1 code patterns for a package from a corpus (Tier PS)",
		Long: `synth samples the corpus for call sites attributable to --package, clusters
them by callee shape, proposes a candidate pattern per cluster, and validates
each candidate by compiling it and running it back over the same corpus.

A candidate is rejected when its query does not compile, when it contains an
unanchored bare '_' inside an argument_list (which binds anonymous tokens and
silently shifts every argument left), when it fires on zero real sites, when it
exceeds the fan-out cap, or when it captures no string literal and no callable
and so yields nothing a linker can join on.

Survivors are written to patterns/<language>/<name>.yaml with a header
recording the package, the corpus and the corpus SHA. The roles in that file
are inferred, not reviewed, and every pattern file needs the fixture directory
TestPatternFixtures requires before it can be trusted.

  polyflow pattern synth --package github.com/go-chi/chi/v5 --corpus ./corpus

--against runs a hand-written pattern file over the same corpus and diffs the
hit sites, which is how synthesis is held to reproducing patterns that already
work:

  polyflow pattern synth --package github.com/go-chi/chi/v5 \
      --corpus patterns/go/chi_routes_test \
      --against patterns/go/chi_routes.yaml --dry-run`,
		RunE: runPatternSynth,
	}
	f := cmd.Flags()
	f.StringVar(&synthPackage, "package", "", "import path the patterns are attributed to (required)")
	f.StringVar(&synthCorpus, "corpus", "", "directory to sample call sites from (required)")
	f.StringVar(&synthLanguage, "language", "go", "pattern language")
	f.StringVar(&synthName, "name", "", "output slug; defaults to the package's last path element")
	f.StringVar(&synthOut, "out", "", "output file; defaults to patterns/<language>/<name>.yaml")
	f.BoolVar(&synthDryRun, "dry-run", false, "print the rendered file instead of writing it")
	f.StringVar(&synthAgainst, "against", "", "hand-written pattern file to compare hit sites against")
	f.Float64Var(&synthMaxExtra, "max-extra", 0.10, "with --against: fraction of extra hits tolerated over the baseline")
	f.IntVar(&synthFanoutCap, "fanout-cap", 500, "reject a candidate matching more sites than this (0 disables)")
	f.Float64Var(&synthMinPrecision, "min-precision", 0, "reject a candidate whose manually labelled precision is below this (needs --labels)")
	f.StringVar(&synthLabels, "labels", "", "JSON file of manual labels: {\"<pattern>@<file>:<line>\": true|false}")
	f.BoolVar(&synthJSON, "json", false, "emit the report as JSON")
	return cmd
}

func runPatternSynth(cmd *cobra.Command, args []string) error {
	labels, err := loadSynthLabels(synthLabels)
	if err != nil {
		return err
	}
	opts := patternsynth.Options{
		Package:      synthPackage,
		Corpus:       synthCorpus,
		Language:     synthLanguage,
		Name:         synthName,
		FanoutCap:    synthFanoutCap,
		MinPrecision: synthMinPrecision,
		Labels:       labels,
	}
	res, err := patternsynth.Run(opts)
	if err != nil {
		return err
	}

	var cmp *patternsynth.Comparison
	if synthAgainst != "" {
		c, err := patternsynth.Compare(res, synthAgainst)
		if err != nil {
			return err
		}
		cmp = &c
	}

	out := synthOut
	if out == "" {
		out = filepath.Join("patterns", res.Options.Language, res.Options.Slug()+".yaml")
	}

	if synthJSON {
		if err := printSynthJSON(res, cmp, out); err != nil {
			return err
		}
	} else {
		printSynthReport(res, cmp, out)
	}

	if len(res.Accepted()) == 0 {
		return fmt.Errorf("no candidate survived the gate — nothing written")
	}
	if cmp != nil {
		if !cmp.Superset() {
			return fmt.Errorf("%d of %d baseline sites are not covered by the synthesized patterns",
				len(cmp.Missing), len(cmp.BaseSites))
		}
		if cmp.ExtraFrac > synthMaxExtra {
			return fmt.Errorf("%d extra hits over the baseline (%.1f%%) exceeds --max-extra %.1f%%",
				len(cmp.Extra), cmp.ExtraFrac*100, synthMaxExtra*100)
		}
	}
	if synthDryRun {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(out, []byte(res.YAML), 0o644); err != nil {
		return err
	}
	fmt.Printf("\nWrote %s\n", out)
	fmt.Printf("Next: add the fixture dir %s_test/ (input, negative, expected.json) — TestPatternFixtures requires it.\n",
		strings.TrimSuffix(out, ".yaml"))
	return nil
}

func loadSynthLabels(path string) (map[string]bool, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read labels: %w", err)
	}
	var labels map[string]bool
	if err := json.Unmarshal(data, &labels); err != nil {
		return nil, fmt.Errorf("parse labels %s: %w", path, err)
	}
	return labels, nil
}

func printSynthReport(res patternsynth.Result, cmp *patternsynth.Comparison, out string) {
	fmt.Printf("Package  %s\n", res.Options.Package)
	fmt.Printf("Corpus   %s  (%d files, %s)\n", res.Options.Corpus, len(res.Files), res.CorpusSHA)
	fmt.Printf("Clusters %d\n\n", len(res.Clusters))

	for _, v := range res.Verdicts {
		mark := "REJECT"
		if v.Accepted {
			mark = "accept"
		}
		fmt.Printf("  %s  %-28s %3d hits in %d files\n", mark, v.Proposal.Pattern.Name, len(v.Hits), v.Files)
		for _, r := range v.Rejects {
			fmt.Printf("            reason: %s\n", r)
		}
		if v.Precision >= 0 {
			fmt.Printf("            manual precision: %.2f over %d labelled\n", v.Precision, v.Labeled)
		}
	}

	// The sample is printed for the survivors only: labelling a rejected
	// candidate's hits is work nobody can spend.
	for _, v := range res.Accepted() {
		fmt.Printf("\n  sample for %s (label these to use --min-precision):\n", v.Proposal.Pattern.Name)
		for _, h := range v.Sample {
			fmt.Printf("    %-40s %s\n", patternsynth.LabelKey(h), h.Text)
		}
	}

	if cmp != nil {
		fmt.Printf("\nAgainst %s over the same corpus:\n", cmp.Baseline)
		fmt.Printf("  baseline sites: %d\n", len(cmp.BaseSites))
		fmt.Printf("  synthesized:    %d\n", len(cmp.SynthHits))
		fmt.Printf("  missing:        %d%s\n", len(cmp.Missing), firstFew(cmp.Missing))
		fmt.Printf("  extra:          %d (%.1f%%)%s\n", len(cmp.Extra), cmp.ExtraFrac*100, firstFew(cmp.Extra))
	}

	fmt.Printf("\n%d of %d candidates survived → %s", len(res.Accepted()), len(res.Verdicts), out)
	if synthDryRun {
		fmt.Printf(" (dry run, not written)\n\n%s", res.YAML)
	}
	fmt.Println()
}

func firstFew(sites []string) string {
	if len(sites) == 0 {
		return ""
	}
	const n = 5
	shown := sites
	suffix := ""
	if len(shown) > n {
		shown, suffix = shown[:n], ", …"
	}
	return "  (" + strings.Join(shown, ", ") + suffix + ")"
}

func printSynthJSON(res patternsynth.Result, cmp *patternsynth.Comparison, out string) error {
	type verdictJSON struct {
		Name      string   `json:"name"`
		Accepted  bool     `json:"accepted"`
		Hits      int      `json:"hits"`
		Files     int      `json:"files"`
		Rejects   []string `json:"rejects,omitempty"`
		Precision float64  `json:"precision,omitempty"`
	}
	payload := struct {
		Package   string                   `json:"package"`
		Corpus    string                   `json:"corpus"`
		CorpusSHA string                   `json:"corpus_sha"`
		Out       string                   `json:"out"`
		Verdicts  []verdictJSON            `json:"verdicts"`
		Compare   *patternsynth.Comparison `json:"compare,omitempty"`
		YAML      string                   `json:"yaml,omitempty"`
	}{
		Package: res.Options.Package, Corpus: res.Options.Corpus,
		CorpusSHA: res.CorpusSHA, Out: out, Compare: cmp, YAML: res.YAML,
	}
	for _, v := range res.Verdicts {
		payload.Verdicts = append(payload.Verdicts, verdictJSON{
			Name: v.Proposal.Pattern.Name, Accepted: v.Accepted,
			Hits: len(v.Hits), Files: v.Files, Rejects: v.Rejects, Precision: v.Precision,
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}
